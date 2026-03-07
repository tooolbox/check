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
- `-C dir` &mdash; change working directory before loading packages
- `-o format` &mdash; output format: `tsv` (default) or `jsonl`

### How the CLI discovers templates

The CLI works by statically analyzing your Go source code. It traces each `ExecuteTemplate` call back to the variable that holds the `*template.Template`, then follows that variable's initialization chain to find the template files. This means:

1. **`ExecuteTemplate` must use a string literal** for the template name (second argument). Calls that pass a variable or expression are skipped.

2. **Template initialization must use static arguments.** File paths passed to `ParseFiles`, glob patterns passed to `ParseGlob`, and embed patterns passed to `ParseFS` must all be string literals.

3. **Supported initialization patterns:**
   - `template.Must(template.ParseFiles("a.html", "b.html"))`
   - `template.Must(template.ParseGlob("templates/*.html"))`
   - `template.Must(template.ParseFS(fs, "*.html"))`
   - `template.New("name").ParseFiles("a.html")`
   - Chained calls: `.Funcs(...)`, `.Option(...)`, `.Delims(...)`, `.Parse(...)`
   - Additional `.ParseFiles(...)`, `.ParseGlob(...)`, or `.ParseFS(...)` calls on an already-initialized template variable

## Library usage

Call `Execute` with a `types.Type` for the template's data (`.`) and the template's `parse.Tree`. See [example_test.go](./example_test.go) for a working example.

## Related projects

- [`muxt`](https://github.com/typelate/muxt) &mdash; builds on this library to type-check templates wired to HTTP handlers. If you only need command-line checks, `muxt check` works too.
- [jba/templatecheck](https://github.com/jba/templatecheck) &mdash; a more mature alternative for template type-checking.

## Limitations

1. You must provide a `types.Type` for the template's root context (`.`).
2. No support for third-party template packages (e.g. [safehtml](https://pkg.go.dev/github.com/google/safehtml)).
3. Cannot detect runtime conditions such as out-of-range indexes or errors from boxed types.
