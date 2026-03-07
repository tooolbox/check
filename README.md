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
- `-w` &mdash; warn about `ExecuteTemplate` calls with non-static template names
- `-C dir` &mdash; change working directory before loading packages
- `-o format` &mdash; output format: `tsv` (default) or `jsonl`

### How the CLI discovers templates

The CLI works by statically analyzing your Go source code. It traces each `ExecuteTemplate` call back to the variable that holds the `*template.Template`, then follows that variable's initialization chain to find the template files. This means:

1. **Templates must be loaded via `embed.FS`** &mdash; The CLI resolves template files by reading `//go:embed` directives on `embed.FS` variables and matching them against the patterns passed to `ParseFS`. It does **not** support `template.ParseFiles()`, `template.ParseGlob()`, or templates loaded from the filesystem at runtime.

2. **`ExecuteTemplate` must use a string literal** for the template name (second argument). Calls that pass a variable or expression will produce a warning and be skipped.

3. **The `embed.FS`, template initialization, and `ExecuteTemplate` calls must be in the same package** (or at least the embed and initialization must be; the CLI resolves per-package).

4. **Supported initialization patterns:**
   - `template.Must(template.ParseFS(fs, "*.html"))`
   - `template.New("name").ParseFS(fs, "*.html")`
   - Chained calls: `.Funcs(...)`, `.Option(...)`, `.Delims(...)`, `.Parse(...)`
   - Additional `.ParseFS(...)` or `.Parse(...)` calls on an already-initialized template variable

If your project loads templates from disk (e.g., `template.ParseFiles("web/templates/index.html")`), the CLI cannot analyze those calls. Consider using the library API directly, or switching to `embed.FS` for your templates.

## Library usage

Call `Execute` with a `types.Type` for the template's data (`.`) and the template's `parse.Tree`. See [example_test.go](./example_test.go) for a working example.

## Related projects

- [`muxt`](https://github.com/typelate/muxt) &mdash; builds on this library to type-check templates wired to HTTP handlers. If you only need command-line checks, `muxt check` works too.
- [jba/templatecheck](https://github.com/jba/templatecheck) &mdash; a more mature alternative for template type-checking.

## Limitations

1. You must provide a `types.Type` for the template's root context (`.`).
2. No support for third-party template packages (e.g. [safehtml](https://pkg.go.dev/github.com/google/safehtml)).
3. Cannot detect runtime conditions such as out-of-range indexes or errors from boxed types.
4. The CLI only supports templates loaded via `embed.FS` and `ParseFS`. Templates loaded at runtime with `ParseFiles` or `ParseGlob` are not supported (see [How the CLI discovers templates](#how-the-cli-discovers-templates)).
