# Check [![Go Reference](https://pkg.go.dev/badge/github.com/typelate/check.svg)](https://pkg.go.dev/github.com/typelate/check)

**Check** is a Go library for statically type-checking `text/template` and `html/template`. It catches template/type mismatches early, making refactoring safer when changing types or templates.

## `check-templates` CLI

If all your `ExecuteTemplate` calls use a string literal for the template name and a static type for the data argument, you can use the CLI directly:

```sh
go get -tool github.com/typelate/check/cmd/check-templates
go tool check-templates ./...
```

Flags:
- `-v` &mdash; list each call with position, template name, and data type
- `-w` &mdash; enable warnings for potential issues (see [Warnings](#warnings) below)
- `-C dir` &mdash; change working directory before loading packages
- `-o format` &mdash; output format: `tsv` (default) or `jsonl`

### How the CLI discovers templates

The CLI works by statically analyzing your Go source code. It traces each `ExecuteTemplate` call back to the variable that holds the `*template.Template`, then follows that variable's initialization chain to find the template files. This means:

1. **`ExecuteTemplate` must use a string literal** for the template name (second argument). Calls that pass a variable or expression will produce a warning (with `-w`) and be skipped.

2. **Template initialization must use static arguments.** File paths passed to `ParseFiles`, glob patterns passed to `ParseGlob`, and embed patterns passed to `ParseFS` must all be string literals.

3. **Supported initialization patterns:**
   - `template.Must(template.ParseFiles("a.html", "b.html"))`
   - `template.Must(template.ParseGlob("templates/*.html"))`
   - `template.Must(template.ParseFS(fs, "*.html"))`
   - `template.New("name").ParseFiles("a.html")`
   - Chained calls: `.Funcs(...)`, `.Option(...)`, `.Delims(...)`, `.Parse(...)`
   - Additional `.ParseFiles(...)`, `.ParseGlob(...)`, or `.ParseFS(...)` calls on an already-initialized template variable

## Warnings

The `-w` flag enables warnings for issues that are not type errors but may indicate bugs. All warnings are printed to stderr.

### Unguarded pointer dereference

When dot is a pointer type (e.g. `*Page`), accessing a field like `.Title` will panic at runtime if dot is nil. The tool warns unless the access is guarded by `{{with}}` or `{{if}}`.

```go
type Page struct { Title string }

func render(p *Page) {
    _ = templates.ExecuteTemplate(w, "index.gohtml", p)
}
```

**Warns** &mdash; accessing `.Title` on a pointer without a nil guard:
```
{{.Title}}
```

**OK** &mdash; guarded with `{{with}}`:
```
{{with .}}
  {{.Title}}
{{end}}
```

**OK** &mdash; guarded with `{{if}}`:
```
{{if .}}
  {{.Title}}
{{end}}
```

### Interface field access

When dot is `interface{}` or `any`, field access cannot be verified at compile time.

```go
func render(data any) {
    _ = templates.ExecuteTemplate(w, "page.gohtml", data)
}
```

**Warns** &mdash; field access on an interface type:
```
{{.Title}}
```

### Unused templates

Templates loaded via `ParseFS`, `ParseFiles`, or `ParseGlob` that are never referenced by any `ExecuteTemplate` call or `{{template}}` action.

```go
//go:embed *.gohtml
var source embed.FS

var templates = template.Must(template.New("app").ParseFS(source, "*"))

func render() {
    _ = templates.ExecuteTemplate(w, "index.gohtml", data)
}
```

**Warns** if `unused.gohtml` exists in the embed but is never referenced:
```
main.go:5:5: warning - template "unused.gohtml" is defined but never referenced
```

### Non-static ExecuteTemplate name

`ExecuteTemplate` must be called with a string literal for the template name. Calls with a variable or expression cannot be checked statically.

```go
// Warns — template name is a variable, not a string literal:
name := getTemplateName()
_ = templates.ExecuteTemplate(w, name, data)

// OK — template name is a string literal:
_ = templates.ExecuteTemplate(w, "index.gohtml", data)
```

## Errors

These are type errors that `check-templates` reports regardless of the `-w` flag:

### Field not found

Accessing a field that does not exist on the data type.

```go
type Page struct { Title string }
```

**Error:**
```
{{.Titel}}  {{/* typo — "Titel" does not exist on Page */}}
```

### Type mismatch in template calls

When `{{template "name" .}}` passes a type that doesn't match what the sub-template expects.

## Library usage

Call `Execute` with a `types.Type` for the template's data (`.`) and the template's `parse.Tree`. See [example_test.go](./example_test.go) for a working example.

## Related projects

- [`muxt`](https://github.com/typelate/muxt) &mdash; builds on this library to type-check templates wired to HTTP handlers. If you only need command-line checks, `muxt check` works too.
- [jba/templatecheck](https://github.com/jba/templatecheck) &mdash; a more mature alternative for template type-checking.

## Limitations

1. You must provide a `types.Type` for the template's root context (`.`).
2. No support for third-party template packages (e.g. [safehtml](https://pkg.go.dev/github.com/google/safehtml)).
3. Cannot detect runtime conditions such as out-of-range indexes or errors from boxed types.
4. Template initialization must use static arguments — file paths, glob patterns, and embed patterns must be string literals (see [How the CLI discovers templates](#how-the-cli-discovers-templates)).
