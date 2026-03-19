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

type Item struct {
	Name  string
	Price float64
}

type Page struct {
	Title string
	Items []Item
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	_ = templates.ExecuteTemplate(w, "index.gohtml", Page{Title: "Shop", Items: []Item{{Name: "Widget", Price: 9.99}}})
}

var _ = fmt.Sprint
