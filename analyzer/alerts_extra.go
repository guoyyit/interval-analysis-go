package analyzer

import (
	"go/ast"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

func alertFromConversion(fa *funcAnalyzer, x *ast.CallExpr, src domain.Interval, dstBnd domain.Bounds) {
	fa.reportAlert(report.KindIntegerOverflow, x,
		"converted value may be outside the destination type range",
		"source in "+src.String()+", destination range "+dstBnd.Min.String()+".."+dstBnd.Max.String(),
		"integer conversion may overflow or wrap")
}
