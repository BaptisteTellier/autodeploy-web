package server

import (
	"io"
	"testing"
	"time"

	"github.com/BaptisteTellier/autodeploy-web/internal/config"
	"github.com/BaptisteTellier/autodeploy-web/internal/deploy"
	"github.com/BaptisteTellier/autodeploy-web/internal/job"
)

// TestExecuteReskinnedPages executes every redesigned view that the other tests
// only PARSE. html/template's contextual autoescaper runs at execute time, so
// this is the only way to surface escaping errors (e.g. a stray quote inside an
// x-data/@click attribute) introduced during the re-skin. Each list carries one
// populated row so row-level markup is exercised too.
func TestExecuteReskinnedPages(t *testing.T) {
	sets := parseTemplates()
	now := time.Now()

	jobView := job.JobView{
		ID: "job00001abcdef", State: job.StateDone, Hostname: "VSADEMO",
		Appliance: "VSA", SourceISO: "src.iso", OutputISO: "out.iso",
		CreatedAt: now, StartedAt: now, FinishedAt: now,
	}
	depView := deploy.View{
		ID: "dep00001abcdef", Kind: "vsa+proxy+hr", State: deploy.StateRunning,
		Nodes: []deploy.NodeStatus{
			{Hostname: "vsa01", Role: "VSA", Step: "ready"},
			{Hostname: "px01", Role: "VIA-Proxy", Step: "uploading"},
		},
		CreatedAt: now, Form: deploy.FormSnapshot{Provider: "proxmox"},
	}
	cfg := config.Defaults()
	optionLists := map[string]any{
		"ApplianceTypes": config.ApplianceTypes, "KeyboardLayouts": config.KeyboardLayouts,
		"Timezones": config.Timezones, "SourceISOs": []string{"a.iso"}, "LicenseFiles": []string{"l.lic"},
	}

	pages := map[string]map[string]any{
		"views/jobs.html":   {"Jobs": []job.JobView{jobView}, "Deployments": []deploy.View{depView}},
		"views/job.html":    {"Job": jobView, "Lines": []string{"15:04:05 build started"}},
		"views/form.html":   {"Config": cfg, "Presets": []config.PresetInfo{{Name: "p1"}}, "Jobs": []job.JobView{jobView}},
		"views/wizard.html": {"Config": cfg},
		"views/admin.html": {
			"BakedPS1Version": "1.0.0", "OverridePS1Version": "1.0.1",
			"OverrideModTime": "2026-06-30 00:00:00", "OverrideActive": true, "MaxHistory": 50,
		},
		"views/media_workspace.html":  {"Files": []MediaFile{{Name: "a.iso", Size: 123, ModTime: now}}},
		"views/media_output.html":     {"Jobs": []OutputJobInfo{{JobID: "j1", FriendlyName: "VSADEMO", ModTime: now, FileCount: 2, TotalSize: 456}}},
		"views/media_output_job.html": {"JobID": "j1", "FriendlyName": "VSADEMO", "Files": []MediaFile{{Name: "a.cfg", Size: 12, ModTime: now}}},
		"views/media_license.html":    {"Files": []MediaFile{{Name: "v.lic", Size: 12, ModTime: now}}},
	}
	// Merge the shared option lists into the form + wizard data.
	for k, v := range optionLists {
		pages["views/form.html"][k] = v
		pages["views/wizard.html"][k] = v
	}

	for _, lang := range supportedLangs {
		for page, data := range pages {
			tmpl := sets[lang][page]
			if tmpl == nil {
				t.Fatalf("lang %q: %s not parsed", lang, page)
			}
			if err := tmpl.ExecuteTemplate(io.Discard, "layout.html", data); err != nil {
				t.Errorf("lang %q: %s execute: %v", lang, page, err)
			}
		}
	}
}
