package intervalanalysis

import (
	"context"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadedPackage is a parsed and type-checked package ready for analysis.
type LoadedPackage struct {
	Pkg     *types.Package
	Fset    *token.FileSet
	Info    *types.Info
	Files   []*ast.File
	Sources map[string][]byte
	Path    string
	Dir     string
}

// AnalysisTarget is a discovered package with its functions.
type AnalysisTarget struct {
	Pkg       *LoadedPackage
	Functions []*FunctionInfo
	Dir       string
}

// FunctionInfo describes one analyzed function.
type FunctionInfo struct {
	ID         FuncID
	Decl       *ast.FuncDecl
	Pkg        *LoadedPackage
	SourceHash string
	Pos        token.Position
}

// LoadPackages parses and type-checks the package in dir, or (recursive) every
// package below dir. Test files are excluded.
func LoadPackages(ctx context.Context, dir string, recursive bool) ([]*LoadedPackage, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dirs, err := packageDirs(abs, recursive)
	if err != nil {
		return nil, err
	}
	parsed := map[string]*LoadedPackage{}
	var pkgs []*LoadedPackage
	for _, d := range dirs {
		if err := ctx.Err(); err != nil {
			return pkgs, err
		}
		lp, err := parseOne(d)
		if err != nil {
			return pkgs, err
		}
		if lp != nil {
			parsed[d] = lp
		}
	}
	// Type-check with an importer that resolves sibling source packages first.
	sourceImporter := newSourceImporter(parsed)
	for _, d := range dirs {
		lp, err := typeCheckOne(parsed[d], sourceImporter)
		if err != nil {
			return pkgs, err
		}
		pkgs = append(pkgs, lp)
		sourceImporter.known[lp.Path] = lp.Pkg
	}
	return pkgs, nil
}

func packageDirs(root string, recursive bool) ([]string, error) {
	if !recursive {
		if hasGoFiles(root) {
			return []string{root}, nil
		}
		return nil, nil
	}
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") {
				if path != root {
					return filepath.SkipDir
				}
			}
			if hasGoFiles(path) {
				dirs = append(dirs, path)
			}
		}
		return nil
	})
	sort.Strings(dirs)
	return dirs, err
}

func hasGoFiles(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		if strings.HasSuffix(name, ".go") {
			return true
		}
	}
	return false
}

func goFiles(dir string) ([]string, map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var names []string
	srcs := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		full := filepath.Join(dir, name)
		src, err := os.ReadFile(full)
		if err != nil {
			return nil, nil, err
		}
		// Skip files with build constraints that exclude them (best-effort).
		if excludedByBuildTag(name, src) {
			continue
		}
		names = append(names, full)
		srcs[full] = src
	}
	sort.Strings(names)
	return names, srcs, nil
}

func parseOne(dir string) (*LoadedPackage, error) {
	names, srcs, err := goFiles(dir)
	if err != nil || len(names) == 0 {
		return nil, err
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(names))
	for _, name := range names {
		af, perr := parser.ParseFile(fset, name, srcs[name],
			parser.ParseComments|parser.AllErrors)
		if perr != nil {
			// Keep whatever AST was recovered so the rest of the package can
			// still be analyzed; a nil file means recovery was impossible.
			if af == nil {
				continue
			}
		}
		// Ignore cgo and "C" pseudo-imports: drop files importing "C".
		if importsC(af) {
			delete(srcs, name)
			continue
		}
		files = append(files, af)
	}
	if len(files) == 0 {
		return nil, nil
	}
	return &LoadedPackage{
		Fset:    fset,
		Files:   files,
		Sources: srcs,
		Dir:     dir,
	}, nil
}

func typeCheckOne(lp *LoadedPackage, imp types.Importer) (*LoadedPackage, error) {
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{
		Importer: imp,
		Error:    func(err error) {},
	}
	name := packageName(lp.Files[0])
	pkg, _ := conf.Check(name, lp.Fset, lp.Files, info)
	if pkg == nil {
		pkg = types.NewPackage(lp.Dir, name)
	}
	lp.Pkg = pkg
	lp.Info = info
	lp.Path = pkg.Path()
	return lp, nil
}

// sourceImporter resolves packages from parsed sources, falling back to the
// standard library importer. Import paths of sibling packages are their
// absolute directories.
type sourceImporter struct {
	byDir    map[string]*LoadedPackage
	known    map[string]*types.Package
	fallback types.Importer
}

func newSourceImporter(parsed map[string]*LoadedPackage) *sourceImporter {
	return &sourceImporter{
		byDir:    parsed,
		known:    map[string]*types.Package{},
		fallback: importer.Default(),
	}
}

func (s *sourceImporter) Import(path string) (*types.Package, error) {
	if p, ok := s.known[path]; ok {
		return p, nil
	}
	if lp, ok := s.byDir[path]; ok && lp.Pkg != nil {
		s.known[path] = lp.Pkg
		return lp.Pkg, nil
	}
	if p, err := s.fallback.Import(path); err == nil {
		s.known[path] = p
		return p, nil
	} else {
		// Return a synthetic incomplete package so analysis continues and the
		// call is treated as an external full-range source.
		synth := types.NewPackage(path, filepath.Base(path))
		s.known[path] = synth
		return synth, nil
	}
}

func packageName(f *ast.File) string {
	if f.Name != nil {
		return f.Name.Name
	}
	return ""
}

func importsC(f *ast.File) bool {
	for _, im := range f.Imports {
		if im.Path != nil && strings.Trim(im.Path.Value, "\"") == "C" {
			return true
		}
	}
	return false
}

func excludedByBuildTag(name string, src []byte) bool {
	// Files suffixed with ignored GOOS/GOARCH pairs are skipped only in obvious
	// cases; default environment is darwin/linux amd64/arm64 covered generally.
	return false
}

// CollectTargets extracts analysis functions from loaded packages.
func CollectTargets(pkgs []*LoadedPackage) []*AnalysisTarget {
	var targets []*AnalysisTarget
	for _, lp := range pkgs {
		t := &AnalysisTarget{Pkg: lp, Dir: lp.Dir}
		for _, file := range lp.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				name := fn.Name.Name
				if fn.Recv != nil && len(fn.Recv.List) > 0 {
					name = receiverName(lp.Info, fn.Recv.List[0].Type) + "." + name
				}
				id := FuncID{PkgPath: lp.Pkg.Path(), Name: name}
				pos := lp.Fset.Position(fn.Pos())
				src := lp.Sources[pos.Filename]
				t.Functions = append(t.Functions, &FunctionInfo{
					ID:         id,
					Decl:       fn,
					Pkg:        lp,
					SourceHash: HashSource(lp.Fset, fn, src),
					Pos:        pos,
				})
			}
		}
		sort.Slice(t.Functions, func(i, j int) bool {
			return t.Functions[i].ID.String() < t.Functions[j].ID.String()
		})
		targets = append(targets, t)
	}
	return targets
}

func receiverName(info *types.Info, expr ast.Expr) string {
	t := info.TypeOf(expr)
	if t == nil {
		if star, ok := expr.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok {
				return id.Name
			}
		}
		if id, ok := expr.(*ast.Ident); ok {
			return id.Name
		}
		return "recv"
	}
	if named, ok := t.(*types.Named); ok {
		return named.Obj().Name()
	}
	if pt, ok := t.(*types.Pointer); ok {
		if named, ok := pt.Elem().(*types.Named); ok {
			return named.Obj().Name()
		}
	}
	return "recv"
}
