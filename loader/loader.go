package loader

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Package is a loaded, type-checked Go package ready for analysis.
type Package struct {
	Path      string
	Dir       string
	Fset      *token.FileSet
	FsetFiles []*token.File
	Info      *types.Info
	Types     *types.Package
	Files     []*ast.File
	Hash      string
}

// Errors holds non-fatal loading/type errors as strings. The package may
// still be partially analyzable; callers decide whether to treat these as
// fatal. They never cause the analyzer to silently drop a package.
type Errors []packages.Error

func (e Errors) Error() string {
	var b strings.Builder
	for i, err := range e {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(err.Error())
	}
	return b.String()
}

type Loader struct {
	includeTests bool
}

func New(includeTests bool) *Loader { return &Loader{includeTests: includeTests} }

// LoadPath loads a single import path or a directory. When path is a
// directory (or recursive is set) local source layout is used.
func (l *Loader) LoadPath(path string, recursive bool) ([]*Package, error) {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		if recursive {
			return l.LoadRecursive(path)
		}
		return l.LoadDir(path)
	}
	return l.loadImportPaths([]string{path})
}

// LoadDir loads all packages in a single directory (non-recursive).
func (l *Loader) LoadDir(dir string) ([]*Package, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return l.loadPatterns([]string{abs})
}

// LoadRecursive loads every package directory under root, including root.
func (l *Loader) LoadRecursive(root string) ([]*Package, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dirs := map[string]bool{abs: true}
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			if path != abs {
				dirs[path] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var patterns []string
	for d := range dirs {
		patterns = append(patterns, d)
	}
	sort.Strings(patterns)
	// Load each directory independently. Loading many absolute directory
	// patterns in one packages.Load call can drop packages depending on the
	// working directory; per-directory loads are reliable and the batches are
	// de-duplicated by import path afterwards.
	var out []*Package
	for _, pat := range patterns {
		ps, err := l.loadPatterns([]string{pat})
		if err != nil {
			continue
		}
		out = append(out, ps...)
	}
	dedup := make(map[string]*Package, len(out))
	for _, p := range out {
		dedup[p.Path] = p
	}
	out = out[:0]
	for _, p := range dedup {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (l *Loader) loadImportPaths(paths []string) ([]*Package, error) {
	var patterns []string
	patterns = append(patterns, paths...)
	return l.loadPatterns(patterns)
}

func (l *Loader) loadPatterns(patterns []string) ([]*Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedDeps | packages.NeedModule,
		Tests: l.includeTests,
	}
	loaded, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, err
	}
	var out []*Package
	var typeErrors []packages.Error
	for _, pk := range loaded {
		if len(pk.Syntax) == 0 || pk.Types == nil {
			continue
		}
		if len(pk.Errors) > 0 {
			typeErrors = append(typeErrors, pk.Errors...)
		}
		out = append(out, convertPackage(pk))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if len(out) == 0 && len(typeErrors) > 0 {
		return nil, Errors(typeErrors)
	}
	return out, nil
}

func convertPackage(pk *packages.Package) *Package {
	info := pk.TypesInfo
	if info == nil {
		info = &types.Info{}
	}
	files := pk.Syntax
	dir := ""
	if len(pk.GoFiles) > 0 {
		dir = filepath.Dir(pk.GoFiles[0])
	}
	var fsetFiles []*token.File
	for _, f := range files {
		if tf := pk.Fset.File(f.Pos()); tf != nil {
			fsetFiles = append(fsetFiles, tf)
		}
	}
	return &Package{
		Path:      pkgPath(pk),
		Dir:       dir,
		Fset:      pk.Fset,
		FsetFiles: fsetFiles,
		Info:      info,
		Types:     pk.Types,
		Files:     files,
		Hash:      packageHash(pk),
	}
}

func pkgPath(pk *packages.Package) string {
	if pk.Types != nil && pk.Types.Name() != "" {
		if pk.PkgPath != "" {
			return pk.PkgPath
		}
		return pk.Types.Name()
	}
	return pk.ID
}

// packageHash fingerprints the exact Go source text of the package, so an
// unchanged package reuses cached summaries in incremental runs.
func packageHash(pk *packages.Package) string {
	names := append([]string{}, pk.CompiledGoFiles...)
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		h.Write([]byte(filepath.Base(name)))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

var _ = types.Universe
