# Plan: Additional Static Analysis Checks & VSCode Integration

## Overview

This plan covers six features to make go-template-check catch more
template bugs and surface them in editors. Each feature is independent
and can be shipped as a separate PR.

---

## 1. Printf Format String Validation

**Problem**: `{{printf "%d" .Name}}` where `.Name` is a string panics at
runtime. The tool currently resolves `printf` to `fmt.Sprintf`'s
signature but only validates argument count, not format/type agreement.

**Approach**:
- In `func.go`, add a `checkPrintf` function called from `CheckCall`
  when the function name is `"printf"`.
- Parse the format string (first arg) to extract format verbs (%d, %s,
  %f, %v, etc.) using `go/analysis/passes/printf`'s verb table or a
  simple hand-rolled parser.
- Compare each verb's expected type against the actual argument type
  from the pipeline.
- Warn on: wrong number of args vs verbs, type mismatches (e.g. %d
  with string), unknown verbs.
- `%v` accepts any type (no check needed).

**Files to modify**:
- `func.go` — add `checkPrintf` logic
- `check.go` — wire printf checking into `checkIdentifierNode` or
  `checkCommandNode` where built-in functions are dispatched

**Tests**: stdlib_test.go already has 15+ printf test cases that
currently pass without validation. Add error cases for format
mismatches.

---

## 2. Support `template.Execute` (not just `ExecuteTemplate`)

**Problem**: `tpl.Execute(w, data)` calls are completely ignored.
Only `ExecuteTemplate` (3 args) is discovered.

**Approach**:
- In `package.go` `findExecuteCalls`, also match `sel.Sel.Name ==
  "Execute"` with `len(call.Args) == 2`.
- For Execute calls, the template name is the receiver's root template
  name. `resolveTemplates` already resolves the receiver to a
  `Template` interface — use its root tree name (the name passed to
  `template.New("name")` or the first filename from ParseFiles).
- Create `pendingCall` with `templateName` set to the root tree name
  and `dataType` from `call.Args[1]`.

**Files to modify**:
- `package.go` — extend `findExecuteCalls` to handle 2-arg Execute
  calls, add root-template-name lookup

**Tests**: New script test cases (pass_execute.txt, err_execute.txt).

---

## 3. Printf-Adjacent: Detect Unused Template Variables

**Problem**: `{{$x := .Foo}}` where `$x` is never used is dead code
that obscures intent.

**Approach**:
- Add a `used` set (`map[string]struct{}`) to the `scope` struct in
  `check.go`.
- In `checkVariableNode`, mark the variable name as used.
- After walking a scope's body (if/with/range), check for variables
  declared in that scope but never marked as used.
- Emit a new warning category `WarnUnusedVariable` (W005).
- Skip `$` (dot alias) and range index/value variables since those
  are often unused intentionally.

**Files to modify**:
- `check.go` — add `used` tracking to `scope`, emit warning
- `package.go` — add W005 constant

**Tests**: New script tests for unused and used variable cases.

---

## 4. Dead Conditional Branch Detection

**Problem**: `{{if true}}...{{else}}dead{{end}}` — the else branch is
unreachable. Similarly `{{if false}}dead{{end}}`.

**Approach**:
- In `checkIfNode` and `checkWithNode`, after evaluating the pipe,
  check if the pipe is a single `BoolNode` or `NilNode`.
- `BoolNode` with `True == true` → else branch is dead.
- `BoolNode` with `True == false` → if branch is dead.
- `NilNode` → condition always false, if branch is dead.
- Emit a new warning `WarnDeadBranch` (W006).
- Only flag literal constants, not expressions that happen to evaluate
  to a constant (that would require constant folding).

**Files to modify**:
- `check.go` — add constant-condition detection in `checkIfNode`
- `package.go` — add W006 constant

**Tests**: Script tests with literal true/false/nil conditions.

---

## 5. Template-to-Template Type Mismatch Warnings

**Problem**: `{{template "header" .Count}}` passes an int to a
sub-template that uses `{{.Title}}` — this is a runtime error. The
tool already checks the sub-template but only with the type from the
outer ExecuteTemplate call, not from `{{template}}` call sites with
different data.

**Approach**:
- In `checkTemplateNode`, the child template is already walked with
  the pipe's result type. If there's an error, it's already reported.
  This is already working.
- The gap is when the *same* sub-template is called from multiple
  `{{template}}` actions with different types. Currently, only the
  first caller's type is checked.
- Track all `(templateName, dataType)` pairs seen across the template
  set. After checking, report if a sub-template is called with
  incompatible types (e.g., both `Page` and `int`).
- Emit as `WarnInconsistentTemplateTypes` (W007).

**Files to modify**:
- `check.go` — track sub-template call types
- `package.go` — add W007 constant, aggregate after checking

**Tests**: Script tests with consistent and inconsistent template calls.

---

## 6. Gopls Analyzer / VSCode Integration

**Problem**: All checks currently require running the CLI. No
real-time editor feedback.

**Approach**: Create a `golang.org/x/tools/go/analysis.Analyzer` that
wraps the existing `Package`/`PackageWithDeferred` logic. This gives
instant integration with gopls (and therefore VS Code, GoLand, Vim,
etc.) — errors and warnings appear as squiggles in real-time.

**Implementation**:
- New package `cmd/analyzer` (or `analyzer/`) containing:
  ```go
  var Analyzer = &analysis.Analyzer{
      Name: "templatecheck",
      Doc:  "checks html/template and text/template calls for type safety",
      Run:  run,
  }
  ```
- The `run` function converts `analysis.Pass` into a
  `*packages.Package` (or adapts the analysis to work directly with
  Pass.TypesInfo, Pass.Files, etc.) and calls the existing check
  logic.
- Errors become `analysis.Diagnostic` with position info.
- Warnings become diagnostics with appropriate severity.
- Publish as a standalone binary via `golang.org/x/tools/go/analysis/singlechecker`
  or `multichecker` so it can be used with `go vet -vettool=`.

**golangci-lint plugin** (optional, bonus):
- Register as a golangci-lint plugin for teams using that pipeline.

**Files to create**:
- `analyzer/analyzer.go` — the Analyzer definition
- `analyzer/analyzer_test.go` — analysistest-based tests
- `cmd/templatecheck-analyzer/main.go` — singlechecker binary

**Dependencies**: `golang.org/x/tools/go/analysis` (already available
via the existing `golang.org/x/tools` dependency).

---

## 7. VS Code Extension (Template-Side Editor Experience)

**Problem**: The gopls analyzer (#6) shows diagnostics on Go call
sites (`.go` files). But the actual template files (`.gohtml`,
`.tmpl`) get no editor support — no squiggles on `{{.Missing}}`, no
completion, no hover types.

**What it provides** (beyond gopls):
- Diagnostics **inside template files** — red squiggles on
  `{{.MissingField}}` in the `.gohtml` itself, not just the Go caller
- Hover-for-type on `{{.Field}}` showing the Go struct type
- Go-to-definition from `{{.Field}}` to the Go struct field
- Completion for `{{.` based on the data type passed from Go
- Template name completion for `{{template "..."}}` and
  `ExecuteTemplate(w, "..."`
- Syntax highlighting for Go template directives in HTML files

**Architecture**:
- TypeScript VS Code extension that spawns a Go language server
  (or shells out to a Go binary on save).
- The Go binary is a thin wrapper around the existing `check` package
  that outputs diagnostics in LSP-compatible format.
- Alternatively, implement a full LSP server in Go using
  `golang.org/x/tools/gopls/internal/protocol` or a lighter LSP
  library like `go-lsp`.

**Extension structure**:
```
vscode-go-template-check/
  package.json          — extension manifest, contributes, activationEvents
  src/extension.ts      — activate/deactivate, spawn language server
  syntaxes/             — TextMate grammar for Go template syntax
  server/               — Go LSP server binary (or bundled)
```

**Marketplace publishing**:
- `package.json` must include: `publisher`, `name`, `version`,
  `displayName`, `description`, `categories`, `engines.vscode`,
  `contributes.languages`, `contributes.grammars`.
- Create a publisher account at https://marketplace.visualstudio.com
- Build with `vsce package`, publish with `vsce publish`.
- Can also distribute via Open VSX for non-Microsoft editors.

**Non-overlapping scope design with #6 (gopls)**:
- Gopls analyzer → diagnostics in `.go` files only (at ExecuteTemplate
  call sites). Useful for CI via `go vet -vettool=`, and for non-VS
  Code editors (GoLand, Vim, Emacs).
- VS Code extension → diagnostics in `.gohtml`/`.tmpl` files only (at
  `{{.Missing}}` tokens). Plus hover, go-to-definition, completion.
- No duplication: each targets a different file type. If both are
  active, the developer sees the problem from both angles — the Go
  call site and the template source — which is complementary, not
  redundant.
- The extension can also add non-diagnostic features to `.go` files
  (e.g., ctrl+click on `"index.gohtml"` string to open the template).

**Files to create**:
- `vscode-go-template-check/` — new directory at repo root (or
  separate repo)
- `cmd/template-lsp/main.go` — Go LSP server binary

---

## Implementation Order

| Order | Feature | Effort | Impact |
|-------|---------|--------|--------|
| 1 | Printf format validation | Medium | High |
| 2 | template.Execute support | Low | High |
| 3 | Unused variable detection | Low | Medium |
| 4 | Dead conditional branches | Low | Medium |
| 5 | Template type mismatches | Medium | Medium |
| 6 | Gopls analyzer | Medium | Very High |
| 7 | VS Code extension | High | Very High |

Features 6 and 7 are listed last because they wrap everything else —
every check added before them automatically shows up in editors once
the analyzer/extension is wired up.

## Verification

Each feature should:
1. Pass `go test ./...`
2. Have script tests in `cmd/check-templates/testdata/` covering both
   positive (pass_*) and negative (err_*/warn_*) cases
3. Not break any existing tests
4. For the analyzer: pass `analysistest.Run` tests
5. For the VS Code extension: manual testing with template files,
   `vsce package` builds cleanly
