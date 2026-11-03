package main

import (
	"testing"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/analysistest"
	"golang.org/x/tools/go/analysis/passes/inspect"
	. "gopkg.in/check.v1"
)

var testingT *testing.T

func TestPackage(t *testing.T) {
	testingT = t
	TestingT(t)
}

type AnalyzeSuite struct{}

var _ = Suite(&AnalyzeSuite{})

func (s *AnalyzeSuite) TestMapIteration(c *C) {
	a := &analysis.Analyzer{
		Name:     "map_iteration",
		Doc:      "fails on uncommented map iterations",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run:      MapIteration,
	}
	testdata := analysistest.TestData()
	analysistest.Run(testingT, testdata, a, "mapiter")
}

func (s *AnalyzeSuite) TestRand(c *C) {
	a := &analysis.Analyzer{
		Name:     "rand",
		Doc:      "fails on use of functions with \"Rand\" in name",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run:      Rand,
	}
	testdata := analysistest.TestData()
	analysistest.Run(testingT, testdata, a, "randuse")
}

func (s *AnalyzeSuite) TestFloatComparison(c *C) {
	a := &analysis.Analyzer{
		Name:     "float_comparison",
		Doc:      "fails on float comparisons",
		Requires: []*analysis.Analyzer{inspect.Analyzer},
		Run:      FloatComparison,
	}
	testdata := analysistest.TestData()
	analysistest.Run(testingT, testdata, a, "floatcmp")
}
