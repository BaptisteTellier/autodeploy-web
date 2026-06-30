package server

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
)

type searchResult struct {
	Type     string `json:"type"`
	Icon     string `json:"icon"`
	Label    string `json:"label"`
	Sublabel string `json:"sublabel"`
	URL      string `json:"url"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	results := []searchResult{}
	if q == "" {
		writeJSON(w, results)
		return
	}

	lang := langFromRequest(r)
	contains := func(field string) bool {
		return strings.Contains(strings.ToLower(field), q)
	}

	// 1. ISO templates (config.Store)
	if isoPresets, err := s.deps.Store.List(); err == nil {
		for _, p := range isoPresets {
			if contains(p.Name) {
				results = append(results, searchResult{
					Type:     "iso_template",
					Icon:     "description",
					Label:    p.Name,
					Sublabel: translate(lang, "search.type.iso_template"),
					URL:      "/new?preset=" + url.QueryEscape(p.Name),
				})
			}
			if len(results) >= 25 {
				writeJSON(w, results)
				return
			}
		}
	}

	// 2. Deploy templates (deploy.PresetStore)
	if s.deps.DeployPresets != nil {
		if deployPresets, err := s.deps.DeployPresets.List(); err == nil {
			for _, p := range deployPresets {
				if contains(p.Name) {
					results = append(results, searchResult{
						Type:     "deploy_template",
						Icon:     "account_tree",
						Label:    p.Name,
						Sublabel: translate(lang, "search.type.deploy_template"),
						URL:      "/deploy?preset=" + url.QueryEscape(p.Name),
					})
				}
				if len(results) >= 25 {
					writeJSON(w, results)
					return
				}
			}
		}
	}

	// 3. Deployed VMs (deploy.Manager)
	if s.deps.DeployManager != nil {
		for _, d := range s.deps.DeployManager.List() {
			for _, n := range d.Nodes {
				if n.Hostname == "" {
					continue
				}
				if contains(n.Hostname) || contains(n.IP) {
					sublabel := n.IP
					if sublabel == "" {
						sublabel = translate(lang, "search.type.vm")
					}
					results = append(results, searchResult{
						Type:     "vm",
						Icon:     "dns",
						Label:    n.Hostname,
						Sublabel: sublabel,
						URL:      "/deploy/" + d.ID,
					})
				}
				if len(results) >= 25 {
					writeJSON(w, results)
					return
				}
			}
		}
	}

	// 4. Workspace ISOs
	for _, name := range listDir(filepath.Join(s.deps.DataDir, "iso"), []string{".iso"}) {
		if contains(name) {
			results = append(results, searchResult{
				Type:     "iso",
				Icon:     "album",
				Label:    name,
				Sublabel: translate(lang, "search.type.iso"),
				URL:      "/media/workspace",
			})
		}
		if len(results) >= 25 {
			writeJSON(w, results)
			return
		}
	}

	// 5. License files
	for _, name := range listDir(filepath.Join(s.deps.DataDir, "license"), []string{".lic"}) {
		if contains(name) {
			results = append(results, searchResult{
				Type:     "license",
				Icon:     "key",
				Label:    name,
				Sublabel: translate(lang, "search.type.license"),
				URL:      "/media/license",
			})
		}
		if len(results) >= 25 {
			writeJSON(w, results)
			return
		}
	}

	writeJSON(w, results)
}
