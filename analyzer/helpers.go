package analyzer

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"math/big"
	"strconv"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/loader"
	"github.com/guoyyit/interval-analysis-go/report"
	"go/constant"
)

// pkgView is the subset of a loaded package the analyzer needs. It exists so
// the analyzer package does not import loader everywhere and so tests can
// build a package programmatically without a full module on disk.
type pkgView struct {
	path  string
	fset  *token.FileSet
	info  *types.Info
	pkg   *types.Package
	files []*ast.File
	hash  string
}

func viewOf(p *loader.Package) *pkgView {
	return &pkgView{
		path:  p.Path,
		fset:  p.Fset,
		info:  p.Info,
		pkg:   p.Types,
		files: p.Files,
		hash:  p.Hash,
	}
}

// basicBits returns the bit width of a predeclared integer type, matching the
// sizes used by the gc compiler on 64-bit targets (int/uint = 64).
func basicBits(b *types.Basic) uint {
	switch b.Kind() {
	case types.Int8, types.Uint8:
		return 8
	case types.Int16, types.Uint16:
		return 16
	case types.Int32, types.Uint32:
		return 32
	case types.Int64, types.Uint64:
		return 64
	case types.Int, types.Uint, types.Uintptr, types.UnsafePointer:
		return 64
	}
	return 64
}

func constInterval(x constant.Value, bnd domain.Bounds) (domain.Interval, bool) {
	if x == nil {
		return domain.Interval{}, false
	}
	z, ok := new(big.Int).SetString(x.ExactString(), 0)
	if !ok {
		return domain.Interval{}, false
	}
	if z.Cmp(bnd.Min) < 0 || z.Cmp(bnd.Max) > 0 {
		// Typed constant out of range should not type-check; stay sound.
		return domain.TypeTop(bnd), true
	}
	return domain.Point(z), true
}

func parseLit(lit *ast.BasicLit) (*big.Int, bool) {
	switch lit.Kind {
	case token.INT:
		s := lit.Value
		base := 0
		if len(s) > 1 && (s[0] == '0') && (s[1] == 'x' || s[1] == 'X') {
			base = 16
		} else if len(s) > 1 && (s[0] == '0') && (s[1] == 'o' || s[1] == 'O') {
			base = 8
		} else if len(s) > 1 && (s[0] == '0') && (s[1] == 'b' || s[1] == 'B') {
			base = 2
		} else if len(s) > 1 && s[0] == '0' {
			s = s[1:]
			base = 8
		}
		z, ok := new(big.Int).SetString(s, base)
		return z, ok
	}
	return nil, false
}

// indexTargetLen conservatively bounds the length of the indexed container.
// Arrays are exact; slices, strings and maps are unconstrained (top >= 0).
func (fa *funcAnalyzer) indexTargetLen(e ast.Expr, st *State) (domain.Interval, bool, domain.Bounds) {
	tv, ok := fa.pkg.info.Types[e]
	if !ok || tv.Type == nil {
		return domain.TopValue(), false, domain.IntBounds(64, false)
	}
	indexBnd := domain.IntBounds(64, false)
	switch t := tv.Type.Underlying().(type) {
	case *types.Array:
		n := big.NewInt(t.Len())
		return domain.Point(n), true, indexBnd
	case *types.Slice, *types.Basic, *types.Map, *types.Pointer:
		return domain.BigRange(big.NewInt(0), indexBnd.Max), false, indexBnd
	}
	return domain.TopValue(), false, indexBnd
}

func (fa *funcAnalyzer) checkIndex(x *ast.IndexExpr, st *State) {
	tv, ok := fa.pkg.info.Types[x.X]
	if !ok || tv.Type == nil {
		_ = fa.evalExpr(x.Index, st)
		return
	}
	idx := fa.evalExpr(x.Index, st)
	if !idx.ok {
		return
	}
	// Negative index always panics; non-negative index into a slice/map/string
	// of unknown length cannot be disproven, but is only reported when the
	// index is provably negative OR an exact array length proves OOB.
	switch tv.Type.Underlying().(type) {
	case *types.Array:
		length, exact, _ := fa.indexTargetLen(x.X, st)
		if exact && !idx.iv.Empty {
			limit := new(big.Int).Sub(length.Hi, big.NewInt(1))
			if idx.iv.Lo.Sign() < 0 || idx.iv.Hi.Cmp(limit) > 0 {
				fa.reportAlert(report.KindIndexOutOfBounds, x,
					fmt.Sprintf("index range %s not within [0, %s]", idx.iv, limit),
					fmt.Sprintf("array length %s, index in %s", length, idx.iv),
					"array index can be out of range")
			}
		}
	case *types.Slice, *types.Basic, *types.Map:
		if !idx.iv.Empty && idx.iv.Lo.Sign() < 0 {
			fa.reportAlert(report.KindIndexOutOfBounds, x,
				fmt.Sprintf("index range %s includes negative values", idx.iv),
				fmt.Sprintf("index in %s", idx.iv),
				"negative index always panics")
		}
	}
}

func (fa *funcAnalyzer) checkSlice(x *ast.SliceExpr, st *State) {
	for _, bound := range []ast.Expr{x.Low, x.High, x.Max} {
		if bound != nil {
			r := fa.evalExpr(bound, st)
			if r.ok && !r.iv.Empty && r.iv.Lo.Sign() < 0 {
				fa.reportAlert(report.KindIndexOutOfBounds, bound,
					fmt.Sprintf("slice bound range %s includes negative values", r.iv),
					fmt.Sprintf("bound in %s", r.iv),
					"negative slice bound always panics")
			}
		}
	}
}

var _ = strconv.Itoa
