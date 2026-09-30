package analyzer

import (
	"go/ast"
	"go/types"
	"math/big"

	"github.com/guoyyit/interval-analysis-go/domain"
)

// registerArrayLiteral records constant integer elements of an array
// composite literal bound to obj. Non-constant or missing elements are stored
// as the element type's full range, so reads remain sound.
func (fa *funcAnalyzer) registerArrayLiteral(obj types.Object, lit *ast.CompositeLit, st *State) {
	if obj == nil || lit == nil {
		return
	}
	at, ok := fa.exprType(lit).Underlying().(*types.Array)
	if !ok {
		return
	}
	elemBnd, isInt := boundsOf(at.Elem())
	els := make(map[int64]domain.Interval, at.Len())
	var idx int64
	for _, el := range lit.Elts {
		if kv, ok := el.(*ast.KeyValueExpr); ok {
			if bl, ok := kv.Key.(*ast.BasicLit); ok {
				if v, ok := parseLit(bl); ok {
					idx = v.Int64()
				}
			}
			el = kv.Value
		}
		var iv domain.Interval
		if isInt {
			if r := fa.evalExpr(el, st); r.ok {
				iv = domain.IntersectKeepFact(r.iv, domain.TypeTop(elemBnd))
			} else {
				iv = domain.TypeTop(elemBnd)
			}
		}
		if isInt {
			els[idx] = iv
		}
		idx++
	}
	fa.arrayEls[obj] = els
}

// evalArrayElement returns the joined possible element values for x := arr[i].
// A constant index selects one element; an unknown index joins every tracked
// element and, if the array length exceeds the tracked count, full range.
func (fa *funcAnalyzer) evalArrayElement(x *ast.IndexExpr, st *State, bnd domain.Bounds) (domain.Interval, bool) {
	id, ok := x.X.(*ast.Ident)
	if !ok {
		return domain.Interval{}, false
	}
	obj := fa.objectOf(id)
	if obj == nil {
		return domain.Interval{}, false
	}
	els, tracked := fa.arrayEls[obj]
	if !tracked {
		return domain.Interval{}, false
	}
	at, ok := obj.Type().Underlying().(*types.Array)
	if !ok {
		return domain.Interval{}, false
	}
	idx := fa.evalExpr(x.Index, st)
	// Constant index: select exact element.
	if idx.ok && !idx.iv.Empty && idx.iv.Lo.Cmp(idx.iv.Hi) == 0 {
		k := idx.iv.Lo.Int64()
		if iv, ok := els[k]; ok {
			return iv, true
		}
		// Index known in range but element untracked (non-constant literal):
		// fall through to full-range element.
		return domain.TypeTop(bnd), true
	}
	// Unknown index: join all tracked elements.
	var acc domain.Interval
	first := true
	for k := int64(0); k < at.Len(); k++ {
		var iv domain.Interval
		if e, ok := els[k]; ok {
			iv = e
		} else {
			iv = domain.TypeTop(bnd)
		}
		if first {
			acc = iv
			first = false
		} else {
			acc = domain.Union(acc, iv)
		}
	}
	if first {
		return domain.TypeTop(bnd), true
	}
	return acc, true
}

var _ = big.NewInt
