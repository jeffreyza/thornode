package main //nolint:govet

import (
	"errors"
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/multichecker"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// -------------------------------------------------------------------------------------
// MapIteration
// -------------------------------------------------------------------------------------

func MapIteration(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	// track lines with the expected ignore comment
	type ignorePos struct {
		file string
		line int
	}
	ignore := map[ignorePos]bool{}

	// one pass to find all comments
	inspect.Preorder([]ast.Node{(*ast.File)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.File)
		if !ok {
			panic("node was not *ast.File")
		}
		for _, c := range n.Comments {
			if strings.Contains(c.Text(), "analyze-ignore(map-iteration)") {
				p := pass.Fset.Position(c.Pos())
				ignore[ignorePos{p.Filename, p.Line + strings.Count(c.Text(), "\n")}] = true
			}
		}
	})

	inspect.Preorder([]ast.Node{(*ast.RangeStmt)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.RangeStmt)
		if !ok {
			panic("node was not *ast.RangeStmt")
		}
		// skip if this is not a range over a map
		if !strings.HasPrefix(pass.TypesInfo.TypeOf(n.X).String(), "map") {
			return
		}

		// skip if this is a test file
		p := pass.Fset.Position(n.Pos())
		if strings.HasSuffix(p.Filename, "_test.go") {
			return
		}

		// skip if the previous line contained the ignore comment
		if ignore[ignorePos{p.Filename, p.Line}] {
			return
		}

		pass.Reportf(node.Pos(), "found map iteration")
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// Rand
// -------------------------------------------------------------------------------------

func Rand(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	allow := func(pos token.Pos) bool {
		p := pass.Fset.Position(pos)
		if strings.HasSuffix(p.Filename, "_test.go") {
			return true
		}
		if strings.HasSuffix(p.Filename, "querier_quotes.go") {
			return true
		}
		if strings.HasSuffix(p.Filename, "test_common.go") {
			return true
		}
		return false
	}

	inspect.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.CallExpr)
		if !ok {
			return
		}
		if id, ok := n.Fun.(*ast.Ident); ok {
			if strings.Contains(id.Name, "Rand") && !allow(n.Pos()) {
				pass.Reportf(n.Pos(), "use of functions with \"Rand\" in name is prohibited")
			}
		}
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// FloatComparison
// -------------------------------------------------------------------------------------

func FloatComparison(pass *analysis.Pass) (interface{}, error) {
	inspect, ok := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	if !ok {
		return nil, errors.New("analyzer is not type *inspector.Inspector")
	}

	// track lines with the expected ignore comment
	type ignorePos struct {
		file string
		line int
	}
	ignore := map[ignorePos]bool{}

	// one pass to find all comments
	inspect.Preorder([]ast.Node{(*ast.File)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.File)
		if !ok {
			panic("node was not *ast.File")
		}
		for _, c := range n.Comments {
			if strings.Contains(c.Text(), "analyze-ignore(float-comparison)") {
				p := pass.Fset.Position(c.Pos())
				ignore[ignorePos{p.Filename, p.Line + strings.Count(c.Text(), "\n")}] = true
			}
		}
	})

	inspect.Preorder([]ast.Node{(*ast.BinaryExpr)(nil)}, func(node ast.Node) {
		n, ok := node.(*ast.BinaryExpr)
		if !ok {
			panic("node was not *ast.BinaryExpr")
		}

		// only check comparison operators
		switch n.Op {
		case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
			// continue with check
		default:
			return
		}

		// check if both operands are floats
		xType := pass.TypesInfo.TypeOf(n.X)
		yType := pass.TypesInfo.TypeOf(n.Y)

		if xType == nil || yType == nil {
			return
		}

		xBasic, xOk := xType.Underlying().(*types.Basic)
		yBasic, yOk := yType.Underlying().(*types.Basic)

		if !xOk || !yOk {
			return
		}

		// check if both are float types
		xIsFloat := xBasic.Info()&types.IsFloat != 0
		yIsFloat := yBasic.Info()&types.IsFloat != 0

		if !xIsFloat || !yIsFloat {
			return
		}

		// skip if this is a test file
		p := pass.Fset.Position(n.Pos())
		if strings.HasSuffix(p.Filename, "_test.go") {
			return
		}

		// skip if the previous line contained the ignore comment
		if ignore[ignorePos{p.Filename, p.Line}] {
			return
		}

		pass.Reportf(node.Pos(), "found float comparison")
	})

	return nil, nil
}

// -------------------------------------------------------------------------------------
// Main
// -------------------------------------------------------------------------------------

func main() {
	multichecker.Main(
		&analysis.Analyzer{
			Name:     "map_iteration",
			Doc:      "fails on uncommented map iterations",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      MapIteration,
		},
		&analysis.Analyzer{
			Name:     "rand",
			Doc:      "fails on use of functions with \"Rand\" in name",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      Rand,
		},
		&analysis.Analyzer{
			Name:     "float_comparison",
			Doc:      "fails on float comparisons",
			Requires: []*analysis.Analyzer{inspect.Analyzer},
			Run:      FloatComparison,
		},
	)
}
