package server

import (
	"embed"
	"html/template"
	"io/fs"
)

//go:embed views/*.html
var viewsFS embed.FS

//go:embed all:static
var staticFS embed.FS

// pageTemplate is the parsed pair (layout.html + one page).
type pageTemplate struct {
	root *template.Template
}

var funcMap = template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
	"deflt": func(def, v string) string {
		if v == "" {
			return def
		}
		return v
	},
	"join": func(sep string, items []string) string {
		out := ""
		for i, s := range items {
			if i > 0 {
				out += sep
			}
			out += s
		}
		return out
	},
}

// parseTemplates returns a map page-name → parsed template. Each page is
// parsed together with layout.html in its own template set, which avoids
// collisions on the shared `{{define "content"}}` block.
func parseTemplates() map[string]*template.Template {
	layout, err := viewsFS.ReadFile("views/layout.html")
	if err != nil {
		panic(err)
	}
	out := make(map[string]*template.Template)
	matches, err := fs.Glob(viewsFS, "views/*.html")
	if err != nil {
		panic(err)
	}
	for _, m := range matches {
		if m == "views/layout.html" {
			continue
		}
		body, err := viewsFS.ReadFile(m)
		if err != nil {
			panic(err)
		}
		t := template.New(m).Funcs(funcMap)
		t, err = t.Parse(string(layout))
		if err != nil {
			panic(err)
		}
		t, err = t.Parse(string(body))
		if err != nil {
			panic(err)
		}
		out[m] = t
	}
	return out
}
