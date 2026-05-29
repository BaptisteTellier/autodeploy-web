package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
)

//go:embed views/*.html
var viewsFS embed.FS

//go:embed all:static
var staticFS embed.FS

// baseFuncMap holds the language-independent template helpers. Per-language
// translation funcs (t, tjs) are layered on top by funcMapForLang.
var baseFuncMap = template.FuncMap{
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
	// humanSize converts bytes to a human-readable string (KB/MB/GB).
	"humanSize": func(n int64) string {
		const unit = 1024
		if n < unit {
			return fmt.Sprintf("%d B", n)
		}
		div, exp := int64(unit), 0
		for v := n / unit; v >= unit; v /= unit {
			div *= unit
			exp++
		}
		return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
	},
	// jsonStr JSON-encodes a string for safe inline use in Alpine x-data attributes.
	"jsonStr": func(s string) template.JS {
		b, _ := json.Marshal(s)
		return template.JS(b) //nolint:gosec
	},
	// isTextFile returns true for extensions that can be displayed in the viewer popup.
	"isTextFile": func(name string) bool {
		ext := strings.ToLower(filepath.Ext(name))
		switch ext {
		case ".cfg", ".log", ".xml", ".sh", ".json", ".txt",
			".ks", ".yaml", ".yml", ".ps1", ".conf", ".md", ".ini", ".env":
			return true
		}
		return false
	},
}

// funcMapForLang returns baseFuncMap plus the translation helpers bound to a
// specific language:
//   - t   "key"  → translated plain text (HTML-escaped by the template).
//   - tjs "key"  → translated text JSON-encoded as a JS string literal, safe
//     inside Alpine x-data / @click attributes and <script> blocks.
func funcMapForLang(lang string) template.FuncMap {
	fm := make(template.FuncMap, len(baseFuncMap)+2)
	for k, v := range baseFuncMap {
		fm[k] = v
	}
	fm["t"] = func(key string) string { return translate(lang, key) }
	fm["tjs"] = func(key string) template.JS {
		b, _ := json.Marshal(translate(lang, key))
		return template.JS(b) //nolint:gosec
	}
	return fm
}

// parseTemplates returns lang → (page-name → parsed template). Each page is
// parsed together with layout.html in its own template set (which avoids
// collisions on the shared `{{define "content"}}` block) and once per
// supported language so the `t`/`tjs` helpers resolve at parse time.
func parseTemplates() map[string]map[string]*template.Template {
	layout, err := viewsFS.ReadFile("views/layout.html")
	if err != nil {
		panic(err)
	}
	matches, err := fs.Glob(viewsFS, "views/*.html")
	if err != nil {
		panic(err)
	}
	out := make(map[string]map[string]*template.Template, len(supportedLangs))
	for _, lang := range supportedLangs {
		fm := funcMapForLang(lang)
		set := make(map[string]*template.Template)
		for _, m := range matches {
			if m == "views/layout.html" {
				continue
			}
			body, err := viewsFS.ReadFile(m)
			if err != nil {
				panic(err)
			}
			t := template.New(m).Funcs(fm)
			t, err = t.Parse(string(layout))
			if err != nil {
				panic(err)
			}
			t, err = t.Parse(string(body))
			if err != nil {
				panic(err)
			}
			set[m] = t
		}
		out[lang] = set
	}
	return out
}
