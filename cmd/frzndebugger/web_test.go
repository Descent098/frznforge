package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"frznforge/internal/timings"
)

// viewer binds a server over a fixture directory and returns it with a request builder that
// already carries the session key and the Host the server insists on.
func viewer(t *testing.T) (*WebServer, func(path string) *http.Request) {
	t.Helper()
	srv, err := ListenWeb(WebOptions{Dir: fixtureDir(t), Port: 0})
	if err != nil {
		t.Fatalf("ListenWeb: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, func(path string) *http.Request {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		r := httptest.NewRequest(http.MethodGet, path+sep+"s="+srv.key, nil)
		r.Host = "127.0.0.1:" + strconv.Itoa(srv.port)
		return r
	}
}

func do(srv *WebServer, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, r)
	return w
}

func TestWebServesThePageWithACSP(t *testing.T) {
	srv, req := viewer(t)
	res := do(srv, req("/"))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if got := res.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("content-type = %q", got)
	}
	csp := res.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	body := res.Body.String()
	if !strings.Contains(body, "frzn") || !strings.Contains(body, "/api/data") {
		t.Error("the page does not look like the viewer")
	}
	// The rule the whole project follows: nothing is fetched from anyone else.
	for _, host := range []string{"https://", "http://", "//cdn", "fonts.googleapis"} {
		if strings.Contains(body, host) {
			t.Errorf("the page references an external host (%q); it must call nobody", host)
		}
	}
}

func TestWebRefusesWithoutTheSessionKey(t *testing.T) {
	srv, _ := viewer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/data", nil)
	r.Host = "127.0.0.1:" + strconv.Itoa(srv.port)
	if res := do(srv, r); res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 -- loopback is reachable from any page the user has open", res.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/api/data?s=wrong", nil)
	r.Host = "127.0.0.1:" + strconv.Itoa(srv.port)
	if res := do(srv, r); res.Code != http.StatusForbidden {
		t.Fatalf("a wrong key gave %d", res.Code)
	}
}

func TestWebRefusesAForeignHostHeader(t *testing.T) {
	srv, _ := viewer(t)
	r := httptest.NewRequest(http.MethodGet, "/?s="+srv.key, nil)
	// DNS rebinding: the name resolved to 127.0.0.1, but the header still says whose it is.
	r.Host = "evil.example:" + strconv.Itoa(srv.port)
	if res := do(srv, r); res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
}

func TestWebRefusesACrossOriginApiCall(t *testing.T) {
	srv, req := viewer(t)
	r := req("/api/data")
	r.Header.Set("Origin", "https://evil.example")
	if res := do(srv, r); res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", res.Code)
	}
	// A typed-in URL sends no Origin at all, and that has to keep working.
	if res := do(srv, req("/api/data")); res.Code != http.StatusOK {
		t.Fatalf("no-Origin request gave %d", res.Code)
	}
}

func TestWebAnswersGetAndHeadOnly(t *testing.T) {
	srv, _ := viewer(t)
	r := httptest.NewRequest(http.MethodPost, "/api/data?s="+srv.key, nil)
	r.Host = "127.0.0.1:" + strconv.Itoa(srv.port)
	res := do(srv, r)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST gave %d, want 405 -- nothing here writes anything", res.Code)
	}
	if got := res.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q", got)
	}
}

func TestWebHasNoPathParameterToTraverse(t *testing.T) {
	srv, req := viewer(t)
	plain := do(srv, req("/api/data")).Body.String()

	// There is no path input anywhere in this API, so a page cannot ask for a file: anything it
	// invents is ignored and the answer is byte for byte the same.
	for _, attempt := range []string{
		"/api/data?path=../../../../etc/passwd",
		"/api/data?dir=C:%5CWindows",
		"/api/data?file=frznforge.log",
		"/api/data?run=../../secrets",
	} {
		if got := do(srv, req(attempt)).Body.String(); got != plain {
			t.Errorf("%s changed the answer; the directory must be fixed at bind time", attempt)
		}
	}
	for _, missing := range []string{"/../frznforge.log", "/frznforge.log", "/data", "/api/other"} {
		if res := do(srv, req(missing)); res.Code != http.StatusNotFound {
			t.Errorf("%s gave %d, want 404 -- only / and /api/data exist", missing, res.Code)
		}
	}
}

func TestWebPayloadLeadsWithTheUnfinishedStep(t *testing.T) {
	srv, req := viewer(t)
	var p payload
	if err := json.Unmarshal(do(srv, req("/api/data")).Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Run != "20260906T224903Z-4812" {
		t.Errorf("run = %q, want the newest", p.Run)
	}
	if len(p.Unfinished) != 1 || p.Unfinished[0].ID != "3" {
		t.Fatalf("unfinished = %+v", p.Unfinished)
	}
	if len(p.Unfinished[0].Children) != 2 || p.Unfinished[0].AtLeastMS != 4700 {
		t.Errorf("unfinished detail = %+v", p.Unfinished[0])
	}
	if len(p.Runs) != 2 || len(p.Log) != 5 || len(p.Tree) == 0 {
		t.Errorf("payload is thin: %d runs, %d log records, %d tree roots", len(p.Runs), len(p.Log), len(p.Tree))
	}
	if p.LogPath == "" || p.TimingsPath == "" {
		t.Error("the payload must name the files it read")
	}
}

func TestWebAggregationIsTheOneInTheTimingsPackage(t *testing.T) {
	// The invariant internal/timings keeps Aggregate for: the page and the terminal must not be
	// able to disagree about what "worst" means, so the page is handed the package's own numbers.
	srv, req := viewer(t)
	var p payload
	if err := json.Unmarshal(do(srv, req("/api/data")).Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	scoped, _ := Scope(Load(srv.Dir).Records, "")
	want := timings.Aggregate(scoped)
	if len(p.Aggregate) != len(want) {
		t.Fatalf("got %d stats, want %d", len(p.Aggregate), len(want))
	}
	for i, s := range want {
		got := p.Aggregate[i]
		if got.Kind != s.Kind || got.Name != s.Name || got.Count != s.Count ||
			got.TotalMS != msOf(s.Total) || got.MeanMS != msOf(s.Mean) ||
			got.BestMS != msOf(s.Best) || got.WorstMS != msOf(s.Worst) {
			t.Errorf("stat %d = %+v, want %+v", i, got, s)
		}
	}
}

func TestWebScopesByRun(t *testing.T) {
	srv, req := viewer(t)
	var all payload
	if err := json.Unmarshal(do(srv, req("/api/data?run=all")).Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if all.Run != AllRuns {
		t.Fatalf("run = %q, want all", all.Run)
	}
	var one payload
	if err := json.Unmarshal(do(srv, req("/api/data?run=20260905T101010Z-100")).Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	if one.Run != "20260905T101010Z-100" || len(one.Unfinished) != 0 {
		t.Errorf("the older run is clean: %+v", one.Unfinished)
	}
}

func TestWebNeverShipsACredential(t *testing.T) {
	srv, req := viewer(t)
	body := do(srv, req("/api/data")).Body.String()
	if strings.Contains(body, "ghs_supersecretvalue") {
		t.Fatal("a credential in the log reached the browser")
	}
	if !strings.Contains(body, "***@example.com") {
		t.Error("the redacted form should still be there, so the reader knows a URL was involved")
	}
}

func TestWebSurvivesADirectoryWithNothingInIt(t *testing.T) {
	srv, err := ListenWeb(WebOptions{Dir: t.TempDir(), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	r := httptest.NewRequest(http.MethodGet, "/api/data?s="+srv.key, nil)
	r.Host = "127.0.0.1:" + strconv.Itoa(srv.port)
	res := do(srv, r)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	// Empty slices rather than nulls: the page iterates them without a guard on every one.
	body := res.Body.String()
	for _, want := range []string{`"runs":[]`, `"unfinished":[]`, `"log":[]`, `"tree":[]`} {
		if !strings.Contains(body, want) {
			t.Errorf("payload %s is missing %s", body, want)
		}
	}
}

func TestWebBindsLoopbackAndPrintsAUsableURL(t *testing.T) {
	srv, _ := viewer(t)
	if !strings.HasPrefix(srv.URL, "http://127.0.0.1:") {
		t.Errorf("URL = %q; a viewer of a developer's disk has no business on 0.0.0.0", srv.URL)
	}
	if !strings.Contains(srv.URL, "?s="+srv.key) {
		t.Error("the URL must carry the session key or nobody can open it")
	}
	if srv.port == 0 {
		t.Error("port 0 must resolve to the port actually bound, or --port=0 is unusable")
	}
}
