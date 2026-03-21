package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
)

//go:embed *.gohtml
var source embed.FS

func toJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var templates = template.Must(template.New("").Funcs(template.FuncMap{"toJSON": toJSON}).ParseFS(source, "*.gohtml"))

type Item struct {
	Name  string
	Price float64
}

type PageData struct {
	Items []Item
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	_ = templates.ExecuteTemplate(w, "index.gohtml", PageData{
		Items: []Item{{Name: "Widget", Price: 9.99}},
	})
}

var _ = fmt.Sprint
