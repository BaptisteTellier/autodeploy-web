package server

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
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
	// chipClass maps a job/deploy/node state token to the status-chip CSS class
	// (see layout.html). The JS twin window.chipClass in app.js must stay in sync
	// so SSE live updates pick the same colour. State tokens are NOT translated.
	"chipClass": func(state any) string {
		switch fmt.Sprintf("%v", state) {
		case "done", "ready", "success":
			return "ad-chip-done"
		case "running", "installing", "uploading", "wiring", "booting", "creating":
			return "ad-chip-running"
		case "failed", "error":
			return "ad-chip-failed"
		default:
			return "ad-chip-pending"
		}
	},
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
	// fmtTime formats a time.Time as "2006-01-02 15:04:05"; returns "" for the zero value.
	"fmtTime": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format("2006-01-02 15:04:05")
	},
	// short8 truncates an id-like string to its first 8 characters (dashboard/table display).
	"short8": func(v any) string {
		s := fmt.Sprintf("%v", v)
		if len(s) > 8 {
			return s[:8]
		}
		return s
	},
	// shortTime formats a time.Time as "01-02 15:04"; returns "—" for the zero value.
	"shortTime": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.Format("01-02 15:04")
	},
	// fmtDur formats the elapsed time between two instants (e.g. "5m30s"); returns "" if either is zero.
	"fmtDur": func(from, to time.Time) string {
		if from.IsZero() || to.IsZero() {
			return ""
		}
		d := to.Sub(from).Round(time.Second)
		if d < 0 {
			d = 0
		}
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		s := int(d.Seconds()) % 60
		if h > 0 {
			return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
		}
		if m > 0 {
			return fmt.Sprintf("%dm%02ds", m, s)
		}
		return fmt.Sprintf("%ds", s)
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
//   - t   "key"     → translated plain text (HTML-escaped by the template).
//   - tjs "key"     → translated text JSON-encoded as a JS string literal, safe
//     inside Alpine x-data / @click attributes and <script> blocks.
//   - helpTip "key" → the standard "?" help-bubble block (Alpine toggle) with
//     the translated text. One definition for the ~40 tooltips in the forms.
func funcMapForLang(lang string) template.FuncMap {
	fm := make(template.FuncMap, len(baseFuncMap)+3)
	for k, v := range baseFuncMap {
		fm[k] = v
	}
	fm["t"] = func(key string) string { return translate(lang, key) }
	fm["tjs"] = func(key string) template.JS {
		b, _ := json.Marshal(translate(lang, key))
		return template.JS(b) //nolint:gosec
	}
	fm["helpTip"] = func(key string) template.HTML {
		txt := template.HTMLEscapeString(translate(lang, key))
		return template.HTML(`<div class="relative inline-block ml-1" x-data="{h:false}">` +
			`<button type="button" @click="h=!h" class="w-5 h-5 rounded-full bg-slate-200 text-slate-500 text-xs font-bold hover:bg-slate-300 leading-none">?</button>` +
			`<div x-show="h" x-cloak @click.outside="h=false" class="absolute z-10 left-0 mt-1 w-72 bg-slate-800 text-white text-xs rounded-lg p-3 shadow-xl">` +
			txt + `</div></div>`) //nolint:gosec — txt is HTML-escaped above; the rest is a fixed literal
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
