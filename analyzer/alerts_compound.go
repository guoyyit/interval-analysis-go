package analyzer

import (
	"go/ast"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

func (fa *funcAnalyzer) maybeArithmeticOverflow(at ast.Node, ovf bool, a, b domain.Interval, bnd domain.Bounds, op string) {
	if !ovf {
		return
	}
	fa.reportAlert(report.KindIntegerOverflow, at,
		"compound operation may exceed the representable range",
		"left in "+a.String()+", right in "+b.String()+", type range "+bnd.Min.String()+".."+bnd.Max.String(),
		"integer arithmetic ("+op+") may overflow")
}

func (fa *funcAnalyzer) reportCompoundDivZero(at ast.Node, divisor, dividend domain.Interval, op string) {
	fa.reportAlert(report.KindDivByZero, at,
		"divisor range "+divisor.String()+" contains zero",
		"dividend in "+dividend.String()+", divisor in "+divisor.String(),
		"integer compound operation ("+op+") may divide by zero")
}
