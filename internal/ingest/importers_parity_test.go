package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/model"
)

// The acceptance bar for the port is byte identity with the TypeScript ingest, and provider
// metadata and releases go straight into forge.json. So the test that matters most for the
// importers is not a Go assertion: it feeds the SAME recorded provider payloads to both
// implementations and compares what each one normalised them into.
//
// Neither side touches the network. The Go client serves the recorded files through its fixture
// Doer; the TypeScript side serves the same files through `fixtureFetch`, the harness its own
// unit tests use.

// tsImportRoute is one canned response in the request handed to ts-import.ts.
type tsImportRoute struct {
	Pattern string            `json:"pattern"`
	Fixture string            `json:"fixture,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// tsImportOutput is what ts-import.ts prints. Decoded with DisallowUnknownFields: a field the Go
// model does not know about means the two implementations have drifted, which is exactly what
// this test exists to catch.
type tsImportOutput struct {
	Provider  string           `json:"provider"`
	Meta      ImportedRepoMeta `json:"meta"`
	Releases  []model.Release  `json:"releases"`
	Truncated bool             `json:"truncated"`
}

func TestImportersMatchTypeScript(t *testing.T) {
	if testing.Short() {
		t.Skip("parity test shells out to tsx")
	}
	root := projectRoot(t)
	if _, err := os.Stat(tsxBinary(root)); err != nil {
		t.Skipf("tsx is not installed (%s); run npm install to enable the parity test", tsxBinary(root))
	}

	// A GitLab release payload built by hand rather than recorded, because the live one has no
	// host-relative asset link and that is the case most likely to diverge between the two URL
	// resolvers (Go's net/url versus the WHATWG parser).
	const relativeAssets = `[{"tag_name":"v1.0.0","name":"one","description":"notes",
		"released_at":"2026-01-01T10:30:00.500+02:00","created_at":"2025-12-31T00:00:00Z",
		"author":{"username":"someone","name":"Some One"},
		"_links":{"self":"/gitlab-org/gitlab-runner/-/releases/v1.0.0"},
		"assets":{"links":[{"name":"installer exe","direct_asset_url":"/g/p/-/releases/v1.0.0/downloads/installer.exe"},
		{"name":"absolute","url":"https://cdn.test/x.bin"}],
		"sources":[{"format":"zip","url":"/g/p/-/archive/v1.0.0/x.zip"},{"format":"tar.gz","url":"https://gitlab.com/a.tar.gz"}]}}]`

	cases := []struct {
		name         string
		source       config.RepoSourceConfig
		routes       []tsImportRoute
		wantReleases bool
	}{
		{
			name:   "github",
			source: githubTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Fixture: "github/releases.json"},
				{Pattern: "/repos/Descent098/ezcv", Fixture: "github/repo.json"},
			},
			wantReleases: true,
		},
		{
			name:   "github assets",
			source: githubTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Fixture: "github/releases-with-assets.json"},
				{Pattern: "/repos/Descent098/ezcv", Fixture: "github/repo.json"},
			},
			wantReleases: true,
		},
		{
			name:   "gitlab",
			source: gitlabTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Fixture: "gitlab/releases.json"},
				{Pattern: "/projects/", Fixture: "gitlab/project.json"},
			},
			wantReleases: true,
		},
		{
			name:   "gitlab host-relative assets and an offset timestamp",
			source: gitlabTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Body: json.RawMessage(relativeAssets)},
				{Pattern: "/projects/", Fixture: "gitlab/project.json"},
			},
			wantReleases: true,
		},
		{
			name:   "gitea",
			source: giteaTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Fixture: "gitea/releases.json"},
				{Pattern: "/repos/gitea/tea", Fixture: "gitea/repo.json"},
			},
			wantReleases: true,
		},
		{
			name:   "forgejo",
			source: forgejoTestSource,
			routes: []tsImportRoute{
				{Pattern: "/releases", Fixture: "forgejo/releases.json"},
				{Pattern: "/repos/forgejo/forgejo", Fixture: "forgejo/repo.json"},
			},
			wantReleases: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetSharedBackoff(t)
			ts := runTypeScriptImport(t, root, tc.source, tc.routes, tc.wantReleases)

			doer := newHTTPFixtureDoer(goFixtureRoutes(t, tc.routes)...)
			importer := CreateImporter(tc.source, ImporterContext{HTTP: doer})
			if importer == nil {
				t.Fatalf("no Go importer for %s", tc.source.Type)
			}
			if importer.Provider() != ts.Provider {
				t.Errorf("provider = %q, TypeScript says %q", importer.Provider(), ts.Provider)
			}

			meta, err := importer.FetchMeta(context.Background())
			if err != nil {
				t.Fatalf("FetchMeta: %v", err)
			}
			// Both sides go through the same encoder, so this is exact on values and on array
			// order — which is what this port owns.
			if got, want := mustJSON(t, meta), mustJSON(t, ts.Meta); got != want {
				t.Errorf("metadata differs.\n go: %s\n ts: %s", got, want)
			}

			if !tc.wantReleases {
				return
			}
			imported, err := importer.FetchReleases(context.Background())
			if err != nil {
				t.Fatalf("FetchReleases: %v", err)
			}
			if imported.Truncated != ts.Truncated {
				t.Errorf("truncated = %v, TypeScript says %v", imported.Truncated, ts.Truncated)
			}
			if got, want := mustJSON(t, imported.Releases), mustJSON(t, ts.Releases); got != want {
				t.Errorf("releases differ.\n go: %s\n ts: %s", got, want)
			}
		})
	}
}

// goFixtureRoutes turns the shared route list into the Go client's own fixture routes, reading
// the same files the TypeScript side reads.
func goFixtureRoutes(t *testing.T, routes []tsImportRoute) []httpFixtureRoute {
	t.Helper()
	out := make([]httpFixtureRoute, 0, len(routes))
	for _, r := range routes {
		route := httpFixtureRoute{Pattern: r.Pattern, Status: r.Status, Headers: r.Headers}
		switch {
		case r.Fixture != "":
			route.Body = loadHTTPFixture(t, r.Fixture)
		case len(r.Body) > 0:
			route.Body = string(r.Body)
		}
		out = append(out, route)
	}
	return out
}

func runTypeScriptImport(
	t *testing.T,
	root string,
	source config.RepoSourceConfig,
	routes []tsImportRoute,
	wantReleases bool,
) tsImportOutput {
	t.Helper()
	request := map[string]any{"source": source, "routes": routes, "wantReleases": wantReleases}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(requestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(tsxBinary(root), filepath.Join("internal", "ingest", "testdata", "ts-import.ts"), requestPath)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("tsx ts-import.ts: %v\n%s", err, stderr.String())
	}

	var out tsImportOutput
	dec := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode TypeScript import: %v\n%s", err, stdout.String())
	}
	return out
}

// mustJSON renders a value through the Go encoder. Both sides of a comparison go through it, so
// a diff is about VALUES and array order, never about how each language spells a struct.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
