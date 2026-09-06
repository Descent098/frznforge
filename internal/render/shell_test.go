package render

import (
	"strings"
	"testing"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/model"
	"frznforge/internal/routes"
)

// TestShellRenders is the smoke test for the template layer: the shell, the sidebar, the
// avatar partial and one page body, executed end to end.
func TestShellRenders(t *testing.T) {
	data := model.EmptyForgeData()
	cfg := &config.Resolved{}
	cfg.Site.Title = "frznforge"
	cfg.Owner.Name = "Kieran Wood"
	cfg.Owner.Handle = "kieran"
	cfg.Theme.Palette = "hearth"
	cfg.Theme.Heat = config.HeatConfig{Hot: 7, Warm: 30, Neutral: 180, Cool: 365}

	site := &Site{Cfg: cfg, Data: &data, Router: routes.Router{Base: ""}, Now: time.Unix(0, 0).UTC()}
	r, err := New(site)
	if err != nil {
		t.Fatalf("parse templates: %v", err)
	}
	var out strings.Builder
	if err := r.RenderPage(&out, "page-404", Page{Title: "Not found"}); err != nil {
		t.Fatalf("render: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"<!doctype html>", `data-palette="hearth"`, `data-base=""`,
		"<title>Not found · frznforge</title>",
		`<symbol id="i-repo"`, `class="hf-sidebar"`, `id="hf-theme-toggle"`,
		"<hf-command-palette></hf-command-palette>", `hf-404-code">404`,
		`src="/js/theme.js"`, `href="/css/global.css"`, ">KW<",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered shell is missing %q", want)
		}
	}
	if strings.Contains(got, "astro") {
		t.Error("output mentions astro — the Go renderer emits none of its scaffolding")
	}
}

// TestShellUnderSubPath — every URL the shell emits must carry the deploy base, or a
// sub-path deploy serves a page whose own stylesheet 404s.
func TestShellUnderSubPath(t *testing.T) {
	data := model.EmptyForgeData()
	cfg := &config.Resolved{}
	cfg.Site.Title = "frznforge"
	cfg.Owner.Name = "K"
	cfg.Owner.Handle = "k"
	cfg.Theme.Palette = "frost"
	site := &Site{Cfg: cfg, Data: &data, Router: routes.Router{Base: "/mysite"}, Now: time.Unix(0, 0).UTC()}
	r, _ := New(site)
	var out strings.Builder
	if err := r.RenderPage(&out, "page-404", Page{Title: "Not found"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{`data-base="/mysite"`, `href="/mysite/css/global.css"`, `src="/mysite/js/theme.js"`, `href="/mysite/repos/"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q under a sub-path deploy", want)
		}
	}
}
