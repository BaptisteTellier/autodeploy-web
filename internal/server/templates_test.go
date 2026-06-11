package server

import "testing"

// TestParseTemplates ensures every embedded view parses for every supported
// language. parseTemplates panics on a malformed template, so a successful
// call is the assertion.
func TestParseTemplates(t *testing.T) {
	sets := parseTemplates()
	for _, lang := range supportedLangs {
		if sets[lang] == nil {
			t.Fatalf("no templates parsed for lang %q", lang)
		}
		for _, page := range []string{"views/deploy.html", "views/deploy_detail.html", "views/form.html", "views/wizard.html"} {
			if sets[lang][page] == nil {
				t.Errorf("lang %q: template %q not parsed", lang, page)
			}
		}
	}
}
