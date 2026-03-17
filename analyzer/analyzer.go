// Package analyzer provides a golang.org/x/tools/go/analysis.Analyzer that
// wraps the go-template-check type-checker. It can be used with go vet via
// -vettool, with gopls for in-editor squiggles, or as a golangci-lint plugin.
package analyzer

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"strings"
	"text/template/parse"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/packages"

	"github.com/typelate/check"
)

// Analyzer is the go/analysis.Analyzer for template type-checking.
var Analyzer = &analysis.Analyzer{
	Name:  "templatecheck",
	Doc:   "checks html/template and text/template calls for type safety",
	URL:   "https://github.com/typelate/check",
	Run:   run,
	Flags: flags(),
}

var enableWarnings bool

func flags() flag.FlagSet {
	var fs flag.FlagSet
	fs.BoolVar(&enableWarnings, "w", false, "enable non-fatal warnings (W001–W007)")
	return fs
}

func run(pass *analysis.Pass) (any, error) {
	if len(pass.Files) == 0 {
		return nil, nil
	}

	// Derive the package directory from the first Go source file.
	firstFile := pass.Fset.File(pass.Files[0].Pos())
	if firstFile == nil {
		return nil, nil
	}
	dir := filepath.Dir(firstFile.Name())

	// Collect absolute Go source file paths from the FileSet.
	goFiles := make([]string, 0, len(pass.Files))
	for _, f := range pass.Files {
		tf := pass.Fset.File(f.Pos())
		if tf != nil {
			goFiles = append(goFiles, tf.Name())
		}
	}

	// Discover embedded files by scanning //go:embed directives in AST comments.
	embedFiles := collectEmbedFiles(dir, pass.Files)

	// Build a packages.Package from the Pass fields so check.Package can run
	// without a separate packages.Load call. This reuses the already
	// type-checked AST and avoids any dependency re-resolution.
	pkg := &packages.Package{
		Fset:       pass.Fset,
		Syntax:     pass.Files,
		Types:      pass.Pkg,
		TypesInfo:  pass.TypesInfo,
		GoFiles:    goFiles,
		EmbedFiles: embedFiles,
	}

	var warnFn check.PackageWarningFunc
	if enableWarnings {
		warnFn = func(cat check.WarningCategory, pos token.Position, message string) {
			p := resolvePos(pass.Fset, pos)
			if !p.IsValid() {
				return
			}
			pass.Report(analysis.Diagnostic{
				Pos:      p,
				Category: cat.Code(),
				Message:  fmt.Sprintf("%s (%s)", message, cat.Code()),
			})
		}
	}

	// Track the most recently seen ExecuteTemplate call position so we can
	// report diagnostics at the Go call site rather than inside the template
	// file (which gopls cannot yet display).
	var lastCallPos token.Pos

	checkErr := check.Package(pkg, func(node *ast.CallExpr, t *parse.Tree, tp types.Type) {
		lastCallPos = node.Pos()
	}, nil, warnFn)
	if checkErr != nil {
		// Use the call site position if available, otherwise parse from the error.
		p := lastCallPos
		if !p.IsValid() {
			p = errorPos(pass.Fset, checkErr.Error())
		}
		if !p.IsValid() {
			p = pass.Files[0].Pos()
		}
		pass.Report(analysis.Diagnostic{
			Pos:      p,
			Category: "E001",
			Message:  stripLocation(checkErr.Error()),
		})
	}

	return nil, nil
}

// collectEmbedFiles scans AST files for //go:embed directives and expands
// the patterns against files on disk in dir. Returns absolute file paths.
func collectEmbedFiles(dir string, files []*ast.File) []string {
	seen := make(map[string]struct{})
	var result []string

	for _, f := range files {
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if !strings.HasPrefix(c.Text, "//go:embed ") {
					continue
				}
				patterns := strings.Fields(strings.TrimPrefix(c.Text, "//go:embed "))
				for _, pat := range patterns {
					// Expand glob patterns relative to the package directory.
					abs := filepath.Join(dir, pat)
					matches, err := filepath.Glob(abs)
					if err != nil {
						continue
					}
					for _, m := range matches {
						if _, dup := seen[m]; !dup {
							seen[m] = struct{}{}
							result = append(result, m)
						}
					}
				}
			}
		}
	}
	return result
}

// stripLocation removes the leading "filename:line:col: " prefix from a
// check error message so the Diagnostic.Message is clean.
func stripLocation(msg string) string {
	rest := msg
	// Strip column (rightmost numeric segment after last colon).
	if i := lastColon(rest); i >= 0 {
		if _, err := fmt.Sscanf(rest[i+1:], "%d", new(int)); err == nil {
			rest = rest[:i]
		} else {
			return msg
		}
	}
	// Strip line.
	if i := lastColon(rest); i >= 0 {
		if _, err := fmt.Sscanf(rest[i+1:], "%d", new(int)); err == nil {
			rest = rest[:i]
		} else {
			return msg
		}
	}
	// rest is the filename; the message follows the next ": ".
	if i := strings.Index(msg[len(rest):], ": "); i >= 0 {
		return strings.TrimSpace(msg[len(rest)+i+2:])
	}
	return msg
}

// resolvePos converts a token.Position (file/line/col) back to a token.Pos
// by scanning the FileSet for a file with a matching name or base name.
func resolvePos(fset *token.FileSet, pos token.Position) token.Pos {
	var found token.Pos
	fset.Iterate(func(f *token.File) bool {
		if f.Name() == pos.Filename || filepath.Base(f.Name()) == filepath.Base(pos.Filename) {
			if pos.Line > 0 && pos.Line <= f.LineCount() {
				found = f.LineStart(pos.Line)
				if pos.Column > 1 {
					found += token.Pos(pos.Column - 1)
				}
			}
			return false
		}
		return true
	})
	return found
}

// errorPos extracts a token.Pos from a check error string of the form
// "filename:line:col: ...".
func errorPos(fset *token.FileSet, msg string) token.Pos {
	rest := msg

	var col int
	if i := lastColon(rest); i >= 0 {
		if _, err := fmt.Sscanf(rest[i+1:], "%d", &col); err == nil {
			rest = rest[:i]
		}
	}

	var line int
	if i := lastColon(rest); i >= 0 {
		if _, err := fmt.Sscanf(rest[i+1:], "%d", &line); err == nil {
			rest = rest[:i]
		}
	}

	filename := rest
	if filename == "" || line == 0 {
		return token.NoPos
	}
	return resolvePos(fset, token.Position{Filename: filename, Line: line, Column: col})
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}
