package main

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
)

//go:embed *.gohtml
var source embed.FS

var templates = template.Must(template.ParseFS(source, "*.gohtml"))

type Page struct {
	Title    string
	Debug    bool
	LogLevel int
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	_ = templates.ExecuteTemplate(w, "page.gohtml", Page{Title: "Home"})
}

var _ = fmt.Sprint
