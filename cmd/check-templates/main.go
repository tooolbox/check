package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template/parse"

	"golang.org/x/tools/go/packages"

	"github.com/tooolbox/check"
	"github.com/tooolbox/check/tsextract"
)

var version = "(dev)"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("check-templates " + version)
		return
	}
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalln(err)
	}
	os.Exit(run(wd, os.Args[1:], os.Stdout, os.Stderr))
}

func run(dir string, args []string, stdout, stderr io.Writer) int {
	var (
		verbose      bool
		warn         bool
		outputFormat string
		tsOutDir     string
	)

	flagSet := flag.NewFlagSet("check-templates", flag.ContinueOnError)
	flagSet.BoolVar(&verbose, "v", false, "show all calls")
	flagSet.BoolVar(&warn, "w", false, "enable warnings (e.g. unguarded pointer access, unused templates)")
	flagSet.StringVar(&dir, "C", dir, "change directory")
	flagSet.StringVar(&outputFormat, "o", "tsv", "output format: tsv, jsonl, or ts-extract")
	flagSet.StringVar(&tsOutDir, "ts-outdir", "", "output directory for ts-extract (default: next to template)")
	if err := flagSet.Parse(args); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	switch outputFormat {
	case "tsv", "jsonl", "ts-extract":
	default:
		_, _ = fmt.Fprintf(stderr, "unsupported output format: %s\n", outputFormat)
		return 1
	}

	if outputFormat == "ts-extract" {
		return runTSExtract(dir, flagSet.Args(), tsOutDir, warn, stdout, stderr)
	}

	if !verbose {
		stdout = io.Discard
	}
	writeCall := writeCallFunc(outputFormat, stdout)

	loadArgs := []string{"."}
	if args := flagSet.Args(); len(args) > 0 {
		loadArgs = flagSet.Args()
	}

	fset := token.NewFileSet()
	pkgs, err := packages.Load(&packages.Config{
		Fset: fset,
		Mode: packages.NeedTypesInfo | packages.NeedName | packages.NeedFiles |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedEmbedPatterns |
			packages.NeedEmbedFiles | packages.NeedImports | packages.NeedModule,
		Dir: dir,
	}, loadArgs...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to load packages: %v\n", err)
		return 1
	}

	exitCode := 0

	// Collect deferred calls from each package so they can be resolved
	// by importing packages (cross-package call-graph tracing).
	var allDeferred []check.DeferredCall

	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			_, _ = fmt.Fprintln(stderr, e)
			exitCode = 1
		}
		deferred, err := check.PackageWithDeferred(pkg, func(node *ast.CallExpr, t *parse.Tree, tp types.Type) {
			writeCall(fset.Position(node.Pos()), t.Name, tp)
		}, func(node *parse.TemplateNode, t *parse.Tree, tp types.Type) {
			loc, _ := t.ErrorContext(node)
			writeCall(parseLocation(loc), t.Name, tp)
		}, func() check.PackageWarningFunc {
			if !warn {
				return nil
			}
			return func(cat check.WarningCategory, pos token.Position, message string) {
				_, _ = fmt.Fprintf(stderr, "%s: %s (%s)\n", pos, message, cat.Code())
			}
		}(), allDeferred)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			exitCode = 1
		}
		allDeferred = append(allDeferred, deferred...)
	}
	return exitCode
}

func runTSExtract(dir string, extraArgs []string, tsOutDir string, enableWarn bool, stdout, stderr io.Writer) int {
	loadArgs := []string{"."}
	if len(extraArgs) > 0 {
		loadArgs = extraArgs
	}

	fset := token.NewFileSet()
	pkgs, err := packages.Load(&packages.Config{
		Fset: fset,
		Mode: packages.NeedTypesInfo | packages.NeedName | packages.NeedFiles |
			packages.NeedTypes | packages.NeedSyntax | packages.NeedEmbedPatterns |
			packages.NeedEmbedFiles | packages.NeedImports | packages.NeedModule,
		Dir: dir,
	}, loadArgs...)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "failed to load packages: %v\n", err)
		return 1
	}

	exitCode := 0

	// Collect {tree → map[*ActionNode]ActionTypes} during the Execute walk.
	treeActions := make(map[*parse.Tree]map[*parse.ActionNode]tsextract.ActionTypes)
	var allDeferred []check.DeferredCall

	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			_, _ = fmt.Fprintln(stderr, e)
			exitCode = 1
		}

		var warnFunc check.PackageWarningFunc
		if enableWarn {
			warnFunc = func(cat check.WarningCategory, pos token.Position, message string) {
				_, _ = fmt.Fprintf(stderr, "%s: %s (%s)\n", pos, message, cat.Code())
			}
		}

		deferred, err := check.PackageWithOptions(pkg, check.PackageOptions{
			InspectAction: func(node *parse.ActionNode, tree *parse.Tree, inputType, resolvedType types.Type) {
				m, ok := treeActions[tree]
				if !ok {
					m = make(map[*parse.ActionNode]tsextract.ActionTypes)
					treeActions[tree] = m
				}
				m[node] = tsextract.ActionTypes{InputType: inputType, ResolvedType: resolvedType}
			},
			Warn:     warnFunc,
			Imported: allDeferred,
		})
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			exitCode = 1
		}
		allDeferred = append(allDeferred, deferred...)
	}

	// Extract script blocks from each tree.
	filesWritten := 0
	for tree, actions := range treeActions {
		result, err := tsextract.Extract(tree, actions)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "ts-extract: %s: %v\n", tree.ParseName, err)
			exitCode = 1
			continue
		}
		if result == nil || (len(result.ScriptBlocks) == 0 && len(result.JSONBlocks) == 0) {
			continue
		}

		// Emit warnings to stderr.
		for _, block := range result.ScriptBlocks {
			for _, w := range block.Warnings {
				_, _ = fmt.Fprintf(stderr, "%s:%d: %s\n", tree.ParseName, block.StartLine, w)
			}
		}

		// Only write a .ts file if there are JS script blocks to check.
		if len(result.ScriptBlocks) == 0 {
			continue
		}

		content := tsextract.FormatTSFile(filepath.Base(tree.ParseName), tree.ParseName, result)

		// Determine output path.
		outPath := tree.ParseName + ".ts"
		if tsOutDir != "" {
			outPath = filepath.Join(tsOutDir, filepath.Base(tree.ParseName)+".ts")
		}

		if tsOutDir != "" {
			if err := os.MkdirAll(tsOutDir, 0o755); err != nil {
				_, _ = fmt.Fprintf(stderr, "ts-extract: %v\n", err)
				exitCode = 1
				continue
			}
		}

		if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "ts-extract: %v\n", err)
			exitCode = 1
			continue
		}
		filesWritten++
		_, _ = fmt.Fprintf(stdout, "%s\n", outPath)
	}

	if filesWritten > 0 {
		_, _ = fmt.Fprintf(stderr, "ts-extract: wrote %d file(s)\n", filesWritten)
	}

	return exitCode
}

type callRecord struct {
	Filename     string `json:"filename"`
	Line         int    `json:"line"`
	Column       int    `json:"column"`
	Offset       int    `json:"offset"`
	TemplateName string `json:"template_name"`
	DataType     string `json:"data_type"`
}

func writeCallFunc(outputFormat string, stdout io.Writer) func(pos token.Position, templateName string, dataType types.Type) {
	switch outputFormat {
	case "jsonl":
		enc := json.NewEncoder(stdout)
		return func(pos token.Position, templateName string, dataType types.Type) {
			_ = enc.Encode(callRecord{
				Filename:     pos.Filename,
				Line:         pos.Line,
				Column:       pos.Column,
				Offset:       pos.Offset,
				TemplateName: templateName,
				DataType:     dataType.String(),
			})
		}
	default:
		return func(pos token.Position, templateName string, dataType types.Type) {
			_, _ = fmt.Fprintf(stdout, "%s\t%q\t%s\n", pos, templateName, dataType)
		}
	}
}

// parseLocation parses a "filename:line:col" string into a token.Position.
func parseLocation(loc string) token.Position {
	// ErrorContext returns "filename:line:col" format.
	// The filename may contain colons (e.g., Windows paths), so split from the right.
	var pos token.Position
	if i := strings.LastIndex(loc, ":"); i >= 0 {
		pos.Column, _ = strconv.Atoi(loc[i+1:])
		loc = loc[:i]
	}
	if i := strings.LastIndex(loc, ":"); i >= 0 {
		pos.Line, _ = strconv.Atoi(loc[i+1:])
		loc = loc[:i]
	}
	pos.Filename = loc
	return pos
}
