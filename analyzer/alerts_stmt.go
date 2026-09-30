package analyzer

import (
	"go/ast"

	"github.com/guoyyit/interval-analysis-go/domain"
	"github.com/guoyyit/interval-analysis-go/report"
)

func (fa *funcAnalyzer) reportIncDecOverflow(s *ast.IncDecStmt, cur domain.Interval, bnd domain.Bounds) {
	fa.reportAlert(report.KindIntegerOverflow, s,
		"increment/decrement may exceed the representable range",
		"current in "+cur.String()+", type range "+bnd.Min.String()+".."+bnd.Max.String(),
		"integer increment/decrement may overflow")
}
