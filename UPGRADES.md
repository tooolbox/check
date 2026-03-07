# Planned Upgrades

This document describes six features to be added to `check` / `check-templates`, each on its own feature branch.

---

## 1. Warn on non-static `{{template}}` names inside templates

**Branch:** `feature/template-node-static-name`

**Problem:** Inside a template, `{{template .Name .}}` uses a dynamic name for the sub-template. The tool currently tries to look up the name and fails with "template not found", but doesn't distinguish between a missing template and a dynamic name that can't be resolved statically.

**Current behavior:** `checkTemplateNode` in `check.go` calls `s.global.trees.FindTree(n.Name)` — `n.Name` is always a static string in the parsed tree (Go's `text/template/parse` requires template names to be string literals in `{{template "name"}}`). So this is actually **already enforced by the parser itself**. The real concern is the Go-side `ExecuteTemplate` call, which we already handle with the `-w` flag.

**Action needed:** Verify that Go's template parser rejects `{{template .Name .}}` at parse time. If so, this is a no-op — document it and close. If there are edge cases (e.g., constructed template names), add warnings similar to the `-w` flag.

**Files affected:** `check.go` (checkTemplateNode), possibly CLI warning output.

---

## 2. Unused template detection

**Branch:** `feature/unused-templates`

**Problem:** Templates loaded via `ParseFS` or `Parse` may never be referenced by any `ExecuteTemplate` call or `{{template "name"}}` action. These are dead templates that add maintenance burden.

**Design:**
- After `resolveTemplates` and `checkCalls` complete in `package.go`, collect the set of all template names that were actually referenced:
  - Template names from `ExecuteTemplate` calls (the `pendingCall.templateName` values)
  - Template names from `{{template "name"}}` nodes encountered during type-checking (via `InspectTemplateNode` or by tracking in `checkTemplateNode`)
- Compare against the full set of templates available in each `resolvedTemplate.templates`
- Report unreferenced templates as warnings via `WarningFunc`

**Files affected:**
- `check.go` — track referenced template names during `checkTemplateNode`
- `package.go` — after `checkCalls`, compute unreferenced set and emit warnings
- CLI test: new txtar test with an unused template

**Flag:** Gated behind `-w` (or a new `-w-unused` flag if we want granularity).

---

## 3. Nil dereference warnings

**Branch:** `feature/nil-deref-warnings`

**Problem:** When dot is a pointer type (e.g., `*Page`), accessing `.Title` will panic at runtime if dot is nil. Templates should guard pointer access with `{{with .}}` or `{{if .}}`.

**Design:**
- In `checkIdentifiers` (`check.go`), when we call `dereference(x)` and `x` was a pointer, check whether we're inside a `{{with}}` or `{{if}}` block that tested the same value.
- This requires tracking "guarded" variables in the scope — if `{{with .Foo}}` is active, then `.Foo` and `.` inside that block are known non-nil.
- Emit warnings (not errors) since nil pointer dereference is a runtime concern, not a type error.

**Complexity:** Medium-high. Tracking which values have been nil-checked requires scope-level metadata. Start with the simple case: warn when dot itself is a pointer type and is accessed without any guard.

**Files affected:**
- `check.go` — `scope` struct gets a `guarded` set, `checkWithNode`/`checkIfNode` populate it, `dereference` checks it
- `package.go` — wire warnings through
- CLI test: txtar tests for guarded vs unguarded pointer access

**Flag:** Gated behind `-w` or a dedicated flag.

---

## 4. Interface satisfaction warnings

**Branch:** `feature/interface-warnings`

**Problem:** When dot is `interface{}` / `any`, field access like `.Foo` cannot be verified at compile time. The tool should warn that this access is unchecked.

**Design:**
- In `checkIdentifiers` (`check.go`), after `dereference(x)`, check if the result is an interface type (via `types.IsInterface`).
- If so, emit a warning: "field access .Foo on interface type cannot be statically verified".
- Skip `types.LookupFieldOrMethod` since it will always fail for empty interfaces.

**Edge cases:**
- Non-empty interfaces (e.g., `fmt.Stringer`) — field access still can't be verified, but method calls could be checked against the interface's method set. Consider only warning for field access, not method calls.
- `any` used as a template data type is common in generic/dynamic templates — this may be noisy. Consider making it opt-in.

**Files affected:**
- `check.go` — `checkIdentifiers`, add interface check before `LookupFieldOrMethod`
- `package.go` — wire warnings
- CLI test: txtar test with `interface{}` dot type

**Flag:** Gated behind `-w` or a dedicated flag.

---

## 5. `ParseFiles` / `ParseGlob` support

**Branch:** `feature/parse-files`

**Problem:** The CLI only supports templates loaded via `embed.FS` + `ParseFS`. Projects that use `template.ParseFiles("path/to/file.html")` or `template.ParseGlob("templates/*.html")` are not analyzed at all.

**Design:**
- In `EvaluateTemplateSelector` (`internal/asteval/template.go`), add cases for `ParseFiles` and `ParseGlob` method calls on both package-level (`template.ParseFiles(...)`) and variable receivers (`ts.ParseFiles(...)`).
- `ParseFiles`: extract string literal arguments, resolve them relative to the package directory (from `packageDirectory` in `package.go`), read and parse each file.
- `ParseGlob`: extract the single string literal glob pattern, resolve relative to package directory, use `filepath.Glob` to expand, then read and parse matched files.
- Both are analogous to the existing `ParseFS` handling but without the `embed.FS` indirection.

**Implementation steps:**
1. Add `"ParseFiles"` case in `EvaluateTemplateSelector` for both `*ast.Ident` (package-level) and `*ast.CallExpr` (chained) branches.
2. Add `"ParseGlob"` case similarly.
3. For `ParseFiles`: use `StringLiteralExpressionList` to get file paths, resolve relative to working directory, call existing `parseFiles` helper.
4. For `ParseGlob`: use `StringLiteralExpression` to get the pattern, `filepath.Glob` to expand, call `parseFiles`.
5. Also handle `template.Must(template.ParseFiles(...))` and `template.Must(template.ParseGlob(...))` — the `Must` unwrapping already exists.

**Also needed in `package.go`:**
- `findExecuteCalls` already finds `ExecuteTemplate` calls regardless of how the template was initialized.
- `resolveTemplates` calls `EvaluateTemplateSelector` which is where the new cases go.
- No changes needed in `package.go` beyond what `EvaluateTemplateSelector` provides.

**Files affected:**
- `internal/asteval/template.go` — add `ParseFiles`/`ParseGlob` cases
- CLI tests: txtar tests for `ParseFiles` and `ParseGlob` (both pass and error cases)
- `README.md` — update "How the CLI discovers templates" to include `ParseFiles`/`ParseGlob`

---

## 6. Robust FuncMap type-checking

**Branch:** `feature/funcmap-typeof`

**Problem:** The current `evaluateFuncMap` in `internal/asteval/template.go` resolves function signatures by formatting the AST node back to source text and calling `types.Eval`. This fails for:
- Method values: `"format": myStruct.Format`
- Closures defined elsewhere and passed as variables
- Package-level function variables: `"escape": pkg.EscapeFunc` where `EscapeFunc` is a `var`

**Design:**
- Replace the `format.Node` → `types.Eval` round-trip with `typesInfo.TypeOf(el.Value)` to get the type directly from the type checker.
- The type checker has already resolved all of these cases during compilation, so `TypeOf` returns the correct `*types.Signature` (or a type that contains one).
- Fall back to the existing `types.Eval` approach if `TypeOf` returns nil (shouldn't happen for valid code, but defensive).

**Implementation:**
```go
// Current (fragile):
buf.Reset()
format.Node(&buf, fileSet, el.Value)
tv, err := types.Eval(fileSet, pkg, lit.Pos(), buf.String())
funcTypesMap[funcName] = tv.Type.(*types.Signature)

// New (robust):
if typesInfo != nil {
    tp := typesInfo.TypeOf(el.Value)
    if sig, ok := tp.(*types.Signature); ok {
        funcTypesMap[funcName] = sig
        continue
    }
}
// fall back to types.Eval for edge cases
```

**Files affected:**
- `internal/asteval/template.go` — `evaluateFuncMap` function
- CLI tests: txtar test with method value and package variable in FuncMap

---

## Implementation order

1. **(5)** `ParseFiles`/`ParseGlob` support — unblocks projects not using `embed.FS`
2. **(6)** Robust FuncMap type-checking — fixes existing fragile code
3. **(1)** Non-static `{{template}}` names — verify if this is already handled by parser
4. **(2)** Unused template detection — new warning feature
5. **(4)** Interface satisfaction warnings — new warning feature
6. **(3)** Nil dereference warnings — most complex, depends on scope tracking
