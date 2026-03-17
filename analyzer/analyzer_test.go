package analyzer_test

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"

	"github.com/typelate/check/analyzer"
)

// TestAnalyzerPass checks that a valid template+Go package produces no diagnostics.
func TestAnalyzerPass(t *testing.T) {
	dir := writeTestPackage(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.25.0\n",
		"main.go": `package main

import (
	"embed"
	"html/template"
	"net/http"
)

var (
	//go:embed *.gohtml
	source embed.FS
	templates = template.Must(template.ParseFS(source, "*"))
)

type Page struct{ Title string }

func handle(w http.ResponseWriter, r *http.Request) {
	_ = templates.ExecuteTemplate(w, "index.gohtml", Page{Title: "hi"})
}
`,
		"index.gohtml": "<h1>{{.Title}}</h1>\n",
	})

	results := analysistest.Run(t, dir, analyzer.Analyzer)
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("unexpected error: %v", r.Err)
		}
	}
}

// TestAnalyzerFail checks that a missing field produces a diagnostic.
func TestAnalyzerFail(t *testing.T) {
	dir := writeTestPackage(t, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.25.0\n",
		"main.go": `package main

import (
	"embed"
	"html/template"
	"net/http"
)

var (
	//go:embed *.gohtml
	source embed.FS
	templates = template.Must(template.ParseFS(source, "*"))
)

type Page struct{ Title string }

func handle(w http.ResponseWriter, r *http.Request) {
	_ = templates.ExecuteTemplate(w, "index.gohtml", Page{Title: "hi"}) // want "Missing not found"
}
`,
		"index.gohtml": "<h1>{{.Missing}}</h1>\n",
	})

	results := analysistest.Run(t, dir, analyzer.Analyzer)
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("unexpected load error: %v", r.Err)
		}
	}
}

// writeTestPackage creates a temporary directory with the given files and
// returns its path.
func writeTestPackage(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
