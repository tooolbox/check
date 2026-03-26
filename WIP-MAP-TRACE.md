# WIP: Map-Based Template Tracing & Dynamic ParseFiles Resolution

**Branch:** `next-march-adv`
**Date:** 2026-03-25
**Status:** Functionally working against `beyond` project, needs test cleanup and full regression run.

## Problem Statement

The `beyond` project uses a common Go pattern where templates are loaded into a map by a helper function, then retrieved via map lookup in a closure:

```go
func New(db *sql.DB, templateRoot string) http.Handler {
    templates := loadTemplates(templateRoot)  // returns map[string]*template.Template
    render := func(w http.ResponseWriter, name string, data any) {
        t, ok := templates[name]
        _ = t.ExecuteTemplate(w, "base.html", data)
    }
    // ...routes call render(w, "dashboard/dashboard.html", Page{...})
}

func loadTemplates(templateRoot string) map[string]*template.Template {
    layoutFile := filepath.Join(templateRoot, "layouts", "base.html")
    partialFiles, _ := filepath.Glob(filepath.Join(templateRoot, "partials", "*.html"))
    shared := append([]string{layoutFile}, partialFiles...)
    pageFiles, _ := filepath.Glob(filepath.Join(templateRoot, "pages", "*", "*.html"))
    templates := make(map[string]*template.Template)
    for _, page := range pageFiles {
        files := make([]string, len(shared)+1)
        copy(files, shared)
        files[len(shared)] = page
        t, err := template.ParseFiles(files...)
        templates[name] = t
    }
    return templates
}
```

The tool previously crashed with `expected call expression` because it couldn't handle map index expressions as template receiver sources.

## What Was Built

### 1. Resilient resolveExpr (package.go)
- When a template receiver's defining expression is not a `*ast.CallExpr`, the tool now silently skips instead of erroring
- Map index expressions are detected and traced through

### 2. Map-Index Tracing (package.go: `traceMapIndex`)
- Detects `t, ok := templates[name]` patterns
- Finds direct map stores (`templates[key] = value`) in the same scope
- Follows into helper functions when the map comes from a function call (`loadTemplates(...)`)
- Returns both the traced expression and a `SliceEvalContext` with parameter bindings

### 3. Variable-to-Call Tracing (package.go: `findMapStoreInFunc`)
- When `templates[name] = t` is found, traces `t` back to its defining expression via `FindDefiningValueInBlock`
- Resolves `t` → `template.ParseFiles(files...)`

### 4. Closure Parameter Tracing (internal/asteval/resolve.go)
- `IsFuncParam` now supports `*ast.FuncLit` (closures), not just `*ast.FuncDecl`
- New helpers: `findEnclosingFuncLit`, `funcLitVarObj`
- Enables call-graph tracing through `render := func(w, name, data any) { ... }`

### 5. String-Slice Expression Evaluator (internal/asteval/sliceval.go)
New `ResolveStringSliceExpr` system that evaluates `[]string`-producing AST expressions:
- **`filepath.Join(args...)`** — resolves each arg, joins them
- **`filepath.Glob(pattern)`** — resolves pattern, runs against real filesystem
- **`filepath.Base(path)`**, **`filepath.Dir(path)`**
- **`append(slice, elems...)`** — handles spread and individual elements
- **`make([]string, n)`** — recognized as empty slice, triggers mutation scanning
- **`copy(dst, src)` + `dst[i] = val`** — `resolveMutatedSlice` assembles slice from copy+index mutations
- **Range variables** — `findRangeExpr` detects `for _, page := range pageFiles` and resolves the range expression
- **Composite literals** — `[]string{"a", "b"}`
- **String concatenation** — `"a" + "b"`
- **Variable tracing** — follows idents to defining expressions
- **Parameter bindings** — resolves function params from call site args

### 6. Cross-Package Parameter Resolution (package.go: `buildDeepParamBindings`)
- `PackageWithDeferred` now accepts `allPkgs []*packages.Package`
- When resolving function param values, searches call sites across ALL loaded packages
- Handles chains like: `main.go: app.New(db, "templates")` → `app.go: loadTemplates(templateRoot)`
- `resolveStringDeep` recursively chases params up the call graph with depth limit

### 7. Module Root for Relative Paths
- Template paths from `filepath.Join(templateRoot, ...)` are relative to the module root, not the package directory
- `SliceEvalContext.WorkingDirectory` uses `pkg.Module.Dir` when available
- `evaluateParseFilesArgs` spread path uses `sliceCtx.WorkingDirectory` for relative→absolute conversion

### 8. Enhanced evaluateParseFilesArgs (internal/asteval/template.go)
- Now accepts `*SliceEvalContext` parameter
- Fast path: string literals (unchanged behavior)
- Slow path: spread args (`ParseFiles(files...)`) resolved via `ResolveStringSliceExpr`
- Individual non-literal args resolved via `sliceCtx.resolveString`
- `EvaluateTemplateSelector` threads `sliceCtx` through all call paths

## Current State

### Working
- `beyond` project's templates are **discovered and parsed** (all 31 template files found via glob)
- Cross-package parameter resolution works (`"templates"` flows from `main.go` → `app.New` → `loadTemplates`)
- The tool reports real type-checking issues (e.g., `.Vehicles` field access on `any` type)
- No crash or false "expected call expression" errors
- Code compiles clean

### Needs Attention
1. **Full test suite not yet run** after removing debug output — need to verify no regressions
2. **Existing tests may need updates:**
   - `pass_map_lookup_parse_files.txt` — previously tested "silently skips"; may now need template files since the tracing is more capable
   - `err_map_lookup_missing_field.txt` — should still pass (same-scope map + closure pattern)
3. **New tests needed** for the beyond-style pattern (helper func + map + glob + closure + spread ParseFiles). These are scripttest txtar files and need real template files on disk (scripttest creates them).
4. **Data type resolution through closures** — the `render` closure receives `data any`, and while the closure param tracing infrastructure exists, the concrete types from call sites like `Page{...}` or `map[string]any{...}` need to flow through. This is partially working via `IsFuncParam` for closures + call graph resolution, but some call sites may pass `map[string]any` which is still opaque.

## Files Changed
- `package.go` — `resolveExpr`, `traceMapIndex`, `findMapStoreInFunc`, `buildDeepParamBindings`, `resolveStringDeep`, `resolveTemplates`, `PackageWithDeferred`, `Package`
- `internal/asteval/resolve.go` — `FindDefiningValue` (exported), `FindDefiningValueInBlock` (new), `IsFuncParam` (closure support), `findEnclosingFuncLit` (new), `funcLitVarObj` (new)
- `internal/asteval/sliceval.go` — **entirely new file** with `ResolveStringSliceExpr`, `SliceEvalContext`, `ParamBindings`, `BuildParamBindings`, and all supporting functions
- `internal/asteval/template.go` — `EvaluateTemplateSelector` (new `sliceCtx` param), `evaluateParseFilesArgs` (spread + non-literal support)
- `internal/asteval/string.go` — unchanged
- `cmd/check-templates/main.go` — passes `pkgs` to `PackageWithDeferred`
- `cmd/check-templates/testdata/pass_map_lookup_parse_files.txt` — new test
- `cmd/check-templates/testdata/err_map_lookup_missing_field.txt` — new test

## Next Steps (Tomorrow)
1. Run the full test suite and fix any regressions
2. Update the two existing map lookup tests if needed
3. Add a test case for the full beyond-style pattern (helper func + glob + spread)
4. Run against `beyond` project clean (no debug output) and verify the type-checking results make sense
5. Consider whether the `any`-typed data from closure params should emit a warning or be silently skipped
