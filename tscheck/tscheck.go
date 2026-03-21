// Package tscheck type-checks generated TypeScript files in-process using
// the Go port of the TypeScript compiler, with an in-memory virtual filesystem.
package tscheck

import (
	"context"
	"strings"

	"github.com/buke/typescript-go-internal/pkg/ast"
	"github.com/buke/typescript-go-internal/pkg/bundled"
	"github.com/buke/typescript-go-internal/pkg/compiler"
	"github.com/buke/typescript-go-internal/pkg/core"
	"github.com/buke/typescript-go-internal/pkg/diagnostics"
	"github.com/buke/typescript-go-internal/pkg/tsoptions"
	"github.com/buke/typescript-go-internal/pkg/tspath"
	"github.com/buke/typescript-go-internal/pkg/vfs/vfstest"
	"github.com/tooolbox/check/tsextract"
)

// Diagnostic represents a single TypeScript error mapped back to template source.
type Diagnostic struct {
	TemplateFile string // absolute path to the .gohtml/.tmpl file
	TemplateName string // the template name
	Line         int    // 1-based line in template source
	Col          int    // 1-based column in template source
	Length       int    // length of the error span in template source (0 if unknown)
	TSLine       int    // 1-based line in generated .ts (for debugging)
	TSCol        int    // 1-based column in generated .ts (for debugging)
	Code         int    // TypeScript error code (e.g. 2339)
	Category     string // "error", "warning", "suggestion", "message"
	Message      string // human-readable diagnostic message
}

// CheckResult holds all diagnostics from a type-check run.
type CheckResult struct {
	Diagnostics []Diagnostic
}

// FileEntry pairs a generated .ts file with the extract result that produced it.
type FileEntry struct {
	// VirtualPath is the POSIX path for the .ts file in the VFS
	// (e.g. "/src/index.gohtml.ts"). Must use forward slashes.
	VirtualPath string
	// Content is the full .ts file content from tsextract.FormatTSFile.
	Content string
	// Result is the ExtractResult that produced the content,
	// used to map diagnostics back to template source locations.
	Result *tsextract.ExtractResult
}

// Check type-checks the generated TypeScript content in-process using an
// in-memory virtual filesystem. It returns diagnostics mapped back to the
// original Go template source locations.
func Check(files []FileEntry) (*CheckResult, error) {
	if len(files) == 0 {
		return &CheckResult{}, nil
	}

	// Build the VFS file map.
	fileMap := make(map[string]string, len(files))
	for _, f := range files {
		fileMap[f.VirtualPath] = f.Content
	}

	// Create in-memory VFS and wrap with bundled lib.d.ts files.
	baseFS := vfstest.FromMap(fileMap, true)
	fs := bundled.WrapFS(baseFS)

	// Create compiler host.
	host := compiler.NewCompilerHost(
		"/",
		fs,
		bundled.LibPath(),
		nil, // no extended config cache
		nil, // no tracing
	)

	// Build compiler options.
	opts := &core.CompilerOptions{
		Target:       core.ScriptTargetES2020,
		Module:       core.ModuleKindES2020,
		Strict:       core.TSTrue,
		NoEmit:       core.TSTrue,
		SkipLibCheck: core.TSTrue,
		Lib:          []string{"lib.es2020.d.ts", "lib.dom.d.ts"},
	}

	// Collect root file names.
	rootFiles := make([]string, 0, len(files))
	for _, f := range files {
		rootFiles = append(rootFiles, f.VirtualPath)
	}

	// Create parsed command line config.
	config := tsoptions.NewParsedCommandLine(
		opts,
		rootFiles,
		tspath.ComparePathsOptions{
			UseCaseSensitiveFileNames: true,
			CurrentDirectory:          "/",
		},
	)

	// Create and run the program.
	program := compiler.NewProgram(compiler.ProgramOptions{
		Host:           host,
		Config:         config,
		SingleThreaded: core.TSTrue,
	})

	ctx := context.Background()

	// Collect syntactic and semantic diagnostics.
	var tsDiags []*ast.Diagnostic
	tsDiags = append(tsDiags, program.GetSyntacticDiagnostics(ctx, nil)...)
	tsDiags = append(tsDiags, program.GetSemanticDiagnostics(ctx, nil)...)

	// Pre-compute block line offsets for each file entry.
	blockOffsets := make(map[string][]blockOffset, len(files))
	for _, f := range files {
		blockOffsets[f.VirtualPath] = computeBlockOffsets(f.Content, f.Result)
	}

	// Build the file entry lookup.
	entryMap := make(map[string]*FileEntry, len(files))
	for i := range files {
		entryMap[files[i].VirtualPath] = &files[i]
	}

	// Map diagnostics back to template source locations.
	result := &CheckResult{}
	for _, d := range tsDiags {
		sourceFile := d.File()
		if sourceFile == nil {
			continue // global diagnostic, skip
		}
		fileName := sourceFile.FileName()
		entry := entryMap[fileName]
		if entry == nil {
			continue // diagnostic in a lib file, skip
		}

		// Convert byte position to 0-based line and byte offset.
		lineStarts := sourceFile.ECMALineMap()
		tsLine0, tsByteOff := core.PositionToLineAndByteOffset(d.Loc().Pos(), lineStarts)
		tsLine := tsLine0 + 1  // 1-based
		tsCol := tsByteOff + 1 // 1-based

		// Map to template source location.
		templateFile, templateName, origLine, origCol := mapToTemplate(
			entry, tsLine, tsCol, blockOffsets[fileName],
		)

		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			TemplateFile: templateFile,
			TemplateName: templateName,
			Line:         origLine,
			Col:          origCol,
			Length:       d.Loc().Len(),
			TSLine:       tsLine,
			TSCol:        tsCol,
			Code:         int(d.Code()),
			Category:     categoryString(d.Category()),
			Message:      d.String(),
		})
	}

	return result, nil
}

// blockOffset records where a ScriptBlock's TypeScript content begins
// in the generated .ts file.
type blockOffset struct {
	startLine int // 1-based line in the .ts file where this block starts
	block     *tsextract.ScriptBlock
}

// computeBlockOffsets finds the 1-based line number in the generated .ts
// content where each ScriptBlock's TypeScript begins.
func computeBlockOffsets(content string, result *tsextract.ExtractResult) []blockOffset {
	if result == nil || len(result.ScriptBlocks) == 0 {
		return nil
	}

	var offsets []blockOffset
	for i := range result.ScriptBlocks {
		block := &result.ScriptBlocks[i]
		// Find the first line of this block's TypeScript in the content.
		// The block's TypeScript was TrimSpaced, so find its first line.
		firstLine := block.TypeScript
		if nl := strings.Index(firstLine, "\n"); nl >= 0 {
			firstLine = firstLine[:nl]
		}
		idx := strings.Index(content, firstLine)
		if idx < 0 {
			continue
		}
		// Count newlines before this position to get the 1-based line number.
		line := 1 + strings.Count(content[:idx], "\n")
		offsets = append(offsets, blockOffset{startLine: line, block: block})
	}
	return offsets
}

// mapToTemplate translates a TS file line/col to the original template location.
func mapToTemplate(entry *FileEntry, tsLine, tsCol int, offsets []blockOffset) (templateFile, templateName string, line, col int) {
	for i := len(offsets) - 1; i >= 0; i-- {
		o := offsets[i]
		blockLineCount := strings.Count(o.block.TypeScript, "\n") + 1
		if tsLine >= o.startLine && tsLine < o.startLine+blockLineCount {
			localLine := tsLine - o.startLine + 1 // 1-based within block
			loc := o.block.MapLocation(localLine, tsCol)
			return o.block.TemplateFile, o.block.TemplateName, loc.Line, loc.Col
		}
	}
	// Fallback: diagnostic in header/branded type area.
	if len(offsets) > 0 {
		b := offsets[0].block
		return b.TemplateFile, b.TemplateName, b.StartLine, 1
	}
	return "", "", tsLine, tsCol
}

func categoryString(c diagnostics.Category) string {
	switch c {
	case diagnostics.CategoryError:
		return "error"
	case diagnostics.CategoryWarning:
		return "warning"
	case diagnostics.CategorySuggestion:
		return "suggestion"
	case diagnostics.CategoryMessage:
		return "message"
	default:
		return "error"
	}
}
