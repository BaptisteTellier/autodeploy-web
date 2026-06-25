package server

import (
	"html/template"
	"io"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
)

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

// TestExecuteCraftAPI renders the Craft API page. html/template's contextual
// autoescaper runs at EXECUTE time, not parse time, so TestParseTemplates cannot
// catch escaping errors (e.g. a literal quote inside a quoted x-data attribute).
// Executing the template is the only way to surface them.
func TestExecuteCraftAPI(t *testing.T) {
	sets := parseTemplates()
	data := map[string]any{
		"Kinds":        catalogViews(),
		"KindsJSON":    template.JS("{}"), //nolint:gosec — test fixture
		"CraftPresets": []string{"sample"},
	}
	for _, lang := range supportedLangs {
		tmpl := sets[lang]["views/craft_api.html"]
		if tmpl == nil {
			t.Fatalf("lang %q: views/craft_api.html not parsed", lang)
		}
		if err := tmpl.ExecuteTemplate(io.Discard, "layout.html", data); err != nil {
			t.Errorf("lang %q: craft_api execute: %v", lang, err)
		}
	}
}

// TestExecuteDeployDetail executes the deploy_detail.html template to catch any
// contextual autoescaping errors introduced by the console Alpine component.
func TestExecuteDeployDetail(t *testing.T) {
	sets := parseTemplates()
	data := map[string]any{
		"Deployment": deploy.View{
			ID:        "test-deploy-id",
			Kind:      "vsa+proxy",
			State:     deploy.StateDone,
			CreatedAt: time.Now(),
			Nodes: []deploy.NodeStatus{
				{Hostname: "vsa01", Role: "VSA", Step: "ready"},
			},
			Form: deploy.FormSnapshot{
				NodeOutputs: []string{"job-001"},
			},
		},
		"Lines": []string{"15:04:05 deploy started"},
	}
	for _, lang := range supportedLangs {
		tmpl := sets[lang]["views/deploy_detail.html"]
		if tmpl == nil {
			t.Fatalf("lang %q: views/deploy_detail.html not parsed", lang)
		}
		if err := tmpl.ExecuteTemplate(io.Discard, "layout.html", data); err != nil {
			t.Errorf("lang %q: deploy_detail execute: %v", lang, err)
		}
	}
}
