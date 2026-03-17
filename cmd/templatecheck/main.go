// The templatecheck command runs the templatecheck analyzer as a standalone
// go vet-compatible tool.
//
// Usage:
//
//	go vet -vettool=$(which templatecheck) ./...
//	templatecheck ./...
package main

import (
	"golang.org/x/tools/go/analysis/singlechecker"

	"github.com/typelate/check/analyzer"
)

func main() { singlechecker.Main(analyzer.Analyzer) }
