package ingest

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"frznforge/internal/config"
	"frznforge/internal/ingest/testsupport"
	"frznforge/internal/model"
)

// Remote sources: mirror cache mechanics plus the metadata precedence chain.
//
// No test here touches the network. The "remote" is always a local fixture repo cloned by path
// (`git clone --mirror <dir>` works exactly like an https clone), and every provider call goes
// through a stub importer or the recorded HTTP fixtures.

/* ---- helpers -------------------------------------------------------------- */

// remoteTempDir is a temp directory removed with the read-only bits cleared: a bare git mirror
// contains read-only pack files, which t.TempDir's cleanup cannot delete on Windows and would
// turn into a spurious failure of an otherwise green test.
func remoteTempDir(t testing.TB, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "frznforge-"+prefix+"-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(dir, func(path string, _ os.DirEntry, err error) error {
			if err == nil {
				_ = os.Chmod(path, 0o700)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	})
	return dir
}

// giteaRemoteSource is a gitea source pointing at nothing reachable — every test injects the
// real (local) clone URL through the mirror seam.
func giteaRemoteSource() config.RepoSourceConfig {
	return config.RepoSourceConfig{
		Type:  "gitea",
		Host:  "https://gitea.example.com",
		Owner: "acme",
		Repo:  "widget",
	}
}

// remoteTestConfig is the smallest resolved config PrepareRemote reads.
func remoteTestConfig(root, cacheDir, fetchMode string) *config.Resolved {
	cfg := &config.Resolved{Root: root, OutDir: filepath.Join(root, "data"), CacheDir: cacheDir}
	cfg.Ingest.Fetch = fetchMode
	cfg.Ingest.OutDir = cfg.OutDir
	cfg.Ingest.CacheDir = cacheDir
	return cfg
}

// resolvedRemote pairs a source with the mirror path it resolves to, the way config.Resolve does.
func resolvedRemote(cacheDir string, source config.RepoSourceConfig) config.ResolvedSource {
	return config.ResolvedSource{
		RepoSourceConfig: source,
		AbsPath:          filepath.Join(cacheDir, "mirrors", config.MirrorDirName(source)),
	}
}

// localMirror is the production EnsureMirror with the clone URL pointed at a local fixture repo.
// Everything else — the cache layout, the auth env, the failure handling — is production code.
func localMirror(origin string) func(context.Context, config.RepoSourceConfig, string, EnsureMirrorOptions) EnsureMirrorResult {
	return func(ctx context.Context, source config.RepoSourceConfig, cachePath string, opts EnsureMirrorOptions) EnsureMirrorResult {
		// Forward slashes: git accepts them on Windows and they keep the arg quoting simple.
		opts.CloneURL = filepath.ToSlash(origin)
		return EnsureMirror(ctx, source, cachePath, opts)
	}
}

// stubImporter answers with canned metadata and releases, and records what was asked for.
type stubImporter struct {
	mu          sync.Mutex
	calls       []string
	meta        *ImportedRepoMeta
	metaErr     error
	releases    []model.Release
	releasesErr error
	truncated   bool
}

func (s *stubImporter) Provider() string { return "gitea" }

func (s *stubImporter) FetchMeta(context.Context) (ImportedRepoMeta, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "meta")
	s.mu.Unlock()
	if s.metaErr != nil {
		return ImportedRepoMeta{}, s.metaErr
	}
	if s.meta != nil {
		return *s.meta, nil
	}
	return stubProviderMeta(), nil
}

func (s *stubImporter) FetchReleases(context.Context) (ImportedReleases, error) {
	s.mu.Lock()
	s.calls = append(s.calls, "releases")
	s.mu.Unlock()
	if s.releasesErr != nil {
		return ImportedReleases{}, s.releasesErr
	}
	releases := s.releases
	if releases == nil {
		releases = []model.Release{}
	}
	return ImportedReleases{Releases: releases, Truncated: s.truncated}, nil
}

func (s *stubImporter) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

// alwaysStub returns an importer factory serving one stub for every source.
func alwaysStub(stub *stubImporter) func(config.RepoSourceConfig, ImporterContext) Importer {
	return func(config.RepoSourceConfig, ImporterContext) Importer { return stub }
}

// noImporter models a source the registry has nothing for.
func noImporter(config.RepoSourceConfig, ImporterContext) Importer { return nil }

func stubProviderMeta() ImportedRepoMeta {
	return ImportedRepoMeta{
		Name:          nil,
		Description:   strPtr("from the provider"),
		Homepage:      strPtr("https://provider.example/home"),
		Topics:        []string{"provider-topic"},
		License:       strPtr("Apache-2.0"),
		DefaultBranch: strPtr("main"),
		WebURL:        "https://gitea.example.com/acme/widget",
		CloneURL:      "https://gitea.example.com/acme/widget.git",
		IssuesURL:     strPtr("https://gitea.example.com/acme/widget/issues"),
		Template:      false,
		Archived:      false,
	}
}

func stubRelease(tag string) model.Release {
	return model.Release{
		Tag:         tag,
		Name:        "Release " + tag,
		Body:        "notes",
		URL:         strPtr("https://gitea.example.com/acme/widget/releases/tag/" + tag),
		Prerelease:  false,
		PublishedAt: testsupport.At(0),
		Author:      strPtr("acme"),
		Assets:      []model.ReleaseAsset{},
	}
}

func remoteScanOptions() ScanOptions {
	return ScanOptions{MaxBlobBytes: 512 * 1024, TagTrees: 5, Archives: false}
}

// refuseGit is a runner that fails the test if git is invoked at all.
func refuseGit(t testing.TB) GitRunner {
	return func(_ context.Context, args []string, _ GitRunContext) (GitRunResult, error) {
		t.Helper()
		return GitRunResult{}, errors.New("git must not run offline: " + strings.Join(args, " "))
	}
}

func gitIn(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := GitOutput(context.Background(), dir, args...)
	if err != nil {
		t.Fatalf("git %s in %s: %v", strings.Join(args, " "), dir, err)
	}
	return strings.TrimSpace(out)
}

/* ---- EnsureMirror --------------------------------------------------------- */

func TestEnsureMirrorClonesThenFetches(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})
	first := origin.Head()

	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "gitea", "gitea.example.com", "acme", "widget.git")
	cloned := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	})
	if cloned.Error != nil || cloned.Action != MirrorCloned {
		t.Fatalf("clone: action=%s err=%v", cloned.Action, cloned.Error)
	}
	if got := gitIn(t, mirrorPath, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Errorf("mirror is not bare: %q", got)
	}
	if got := gitIn(t, mirrorPath, "rev-parse", "main"); got != first {
		t.Errorf("mirror head = %q, want %q", got, first)
	}

	origin.WriteAndCommit(map[string]string{"a.txt": "two\n"}, "second", testsupport.CommitOptions{Date: testsupport.At(60)})
	second := origin.Head()

	fetched := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	})
	if fetched.Error != nil || fetched.Action != MirrorFetched {
		t.Fatalf("fetch: action=%s err=%v", fetched.Action, fetched.Error)
	}
	if got := gitIn(t, mirrorPath, "rev-parse", "main"); got != second {
		t.Errorf("mirror head after fetch = %q, want %q", got, second)
	}
}

func TestEnsureMirrorFetchNeverServesTheStaleCache(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})
	first := origin.Head()

	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	})

	origin.WriteAndCommit(map[string]string{"a.txt": "two\n"}, "second", testsupport.CommitOptions{Date: testsupport.At(60)})
	if origin.Head() == first {
		t.Fatal("the fixture did not advance")
	}

	res := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "never", CloneURL: filepath.ToSlash(origin.Dir), Run: refuseGit(t),
	})
	if res.Action != MirrorCached || res.Error != nil {
		t.Fatalf("action=%s err=%v", res.Action, res.Error)
	}
	// The mirror is deliberately out of date: the new origin commit was never fetched.
	if got := gitIn(t, mirrorPath, "rev-parse", "main"); got != first {
		t.Errorf("mirror head = %q, want the stale %q", got, first)
	}
}

func TestEnsureMirrorFetchNeverWithNoCacheIsMissing(t *testing.T) {
	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "nothing.git")
	res := EnsureMirror(context.Background(), giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "never", CloneURL: "https://gitea.example.com/acme/widget.git",
	})
	if res.Action != MirrorMissing {
		t.Fatalf("action = %s", res.Action)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "ingest.fetch is 'never'") {
		t.Fatalf("error = %v", res.Error)
	}
	if _, err := os.Stat(mirrorPath); err == nil {
		t.Error("a 'never' run created the mirror directory")
	}
}

func TestEnsureMirrorFallsBackToTheCacheWhenTheOriginIsUnreachable(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})
	first := origin.Head()

	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	})

	gone := filepath.Join(filepath.Dir(origin.Dir), "moved-away")
	if err := os.Rename(origin.Dir, gone); err != nil {
		t.Fatalf("rename: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(gone, origin.Dir) })

	res := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir),
	})
	if res.Action != MirrorCached || res.Error == nil {
		t.Fatalf("action=%s err=%v", res.Action, res.Error)
	}
	if got := gitIn(t, mirrorPath, "rev-parse", "main"); got != first {
		t.Errorf("the cached mirror was damaged: head = %q", got)
	}
}

func TestEnsureMirrorLeavesNothingBehindWhenTheFirstCloneFails(t *testing.T) {
	cacheDir := remoteTempDir(t, "cache")
	mirrorPath := filepath.Join(cacheDir, "gone.git")
	res := EnsureMirror(context.Background(), giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(filepath.Join(cacheDir, "no-such-origin")),
	})
	if res.Action != MirrorMissing || res.Error == nil {
		t.Fatalf("action=%s err=%v", res.Action, res.Error)
	}
	// A half-written clone would make every later run fail the "exists but is not a repo" check.
	if _, err := os.Stat(mirrorPath); err == nil {
		t.Error("a failed clone left its destination behind")
	}
}

func TestEnsureMirrorSendsTheTokenOutOfBandAndNeverPersistsIt(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	const token = "super-secret-token-value"
	basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))

	// Record argv + env, then really run it so the assertions below read a real mirror config.
	type invocation struct {
		args []string
		env  map[string]string
	}
	var seen []invocation
	spy := func(ctx context.Context, args []string, opts GitRunContext) (GitRunResult, error) {
		seen = append(seen, invocation{args: append([]string(nil), args...), env: opts.Env})
		return defaultGitRunner(ctx, args, opts)
	}

	res := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: filepath.ToSlash(origin.Dir), Token: token, Run: spy,
	})
	if res.Action != MirrorCloned {
		t.Fatalf("action=%s err=%v", res.Action, res.Error)
	}
	if len(seen) == 0 {
		t.Fatal("the spy runner was never called")
	}

	// The credential travels in the child's environment. On argv it would be readable by any
	// other process on the machine (/proc/<pid>/cmdline, Win32_Process) for the whole clone.
	argv := strings.Join(seen[0].args, " ")
	for _, forbidden := range []string{token, basic, "extraheader", token + "@"} {
		if strings.Contains(argv, forbidden) {
			t.Errorf("argv carried %q: %s", forbidden, argv)
		}
	}
	env := seen[0].env
	want := map[string]string{
		"GIT_CONFIG_COUNT":   "2",
		"GIT_CONFIG_KEY_0":   "credential.helper",
		"GIT_CONFIG_VALUE_0": "",
		"GIT_CONFIG_KEY_1":   "http.extraheader",
		"GIT_CONFIG_VALUE_1": "Authorization: Basic " + basic,
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}

	// ...and nothing lands on disk either.
	persisted, err := os.ReadFile(filepath.Join(mirrorPath, "config"))
	if err != nil {
		t.Fatalf("read mirror config: %v", err)
	}
	for _, forbidden := range []string{token, basic, "extraheader"} {
		if strings.Contains(string(persisted), forbidden) {
			t.Errorf("the mirror config persisted %q", forbidden)
		}
	}
}

func TestEnsureMirrorDisablesCredentialHelpersWithoutAToken(t *testing.T) {
	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	var env map[string]string
	spy := func(_ context.Context, _ []string, opts GitRunContext) (GitRunResult, error) {
		env = opts.Env
		zero := 0
		return GitRunResult{Code: &zero}, nil
	}
	EnsureMirror(context.Background(), giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: "https://gitea.example.com/acme/widget.git", Run: spy,
	})
	// A configured credential helper could pop a GUI dialog and stall the build forever.
	if env["GIT_CONFIG_COUNT"] != "1" || env["GIT_CONFIG_KEY_0"] != "credential.helper" || env["GIT_CONFIG_VALUE_0"] != "" {
		t.Fatalf("env = %v", env)
	}
	if _, set := env["GIT_CONFIG_KEY_1"]; set {
		t.Errorf("an anonymous clone set an auth header: %v", env)
	}
}

func TestEnsureMirrorTurnsAFailedSpawnIntoAWarning(t *testing.T) {
	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	exploding := func(context.Context, []string, GitRunContext) (GitRunResult, error) {
		return GitRunResult{}, errors.New("read ENOTCONN")
	}
	// A spawn that dies (an over-long destination path on Windows is the real-world case) must
	// settle as an error result — EnsureMirror never panics for an environment problem.
	res := EnsureMirror(context.Background(), giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: "https://gitea.example.com/acme/widget.git", Run: exploding,
	})
	if res.Action != MirrorMissing {
		t.Fatalf("action = %s", res.Action)
	}
	if res.Error == nil || !strings.Contains(res.Error.Error(), "ENOTCONN") {
		t.Fatalf("error = %v", res.Error)
	}
}

func TestEnsureMirrorSkipsAnUnchangedRemote(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	mirrorPath := filepath.Join(remoteTempDir(t, "cache"), "widget.git")
	cloneURL := filepath.ToSlash(origin.Dir)
	EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{Fetch: "auto", CloneURL: cloneURL})

	heads := ReadRefHeads(ctx, mirrorPath)
	if len(heads) == 0 {
		t.Fatal("no baseline heads to compare against")
	}

	// Every ref matches, so `git remote update` provably has nothing to do.
	current := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: cloneURL, SkipUnchanged: true, KnownHeads: heads,
	})
	if current.Action != MirrorCurrent || current.Error != nil {
		t.Fatalf("action=%s err=%v", current.Action, current.Error)
	}

	// One moved ref and the probe must fall through to a normal update.
	origin.WriteAndCommit(map[string]string{"a.txt": "two\n"}, "second", testsupport.CommitOptions{Date: testsupport.At(60)})
	moved := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: cloneURL, SkipUnchanged: true, KnownHeads: heads,
	})
	if moved.Action != MirrorFetched {
		t.Fatalf("action = %s, want a real fetch once a ref moved", moved.Action)
	}
	if got := gitIn(t, mirrorPath, "rev-parse", "main"); got != origin.Head() {
		t.Errorf("mirror head = %q, want %q", got, origin.Head())
	}

	// A new TAG counts too, not just a moved branch.
	fresh := ReadRefHeads(ctx, mirrorPath)
	origin.Tag("v1.0.0", testsupport.TagOptions{Date: testsupport.At(120)})
	tagged := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{
		Fetch: "auto", CloneURL: cloneURL, SkipUnchanged: true, KnownHeads: fresh,
	})
	if tagged.Action != MirrorFetched {
		t.Fatalf("action = %s, want a real fetch once a tag appeared", tagged.Action)
	}

	// Off by default: always a real update.
	def := EnsureMirror(ctx, giteaRemoteSource(), mirrorPath, EnsureMirrorOptions{Fetch: "auto", CloneURL: cloneURL})
	if def.Action != MirrorFetched {
		t.Fatalf("action = %s, want the ordinary update path", def.Action)
	}
}

/* ---- PrepareRemote -------------------------------------------------------- */

func TestPrepareRemoteLayersOverridesThenRepoFileThenProvider(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{
		"README.md":       "# widget\n",
		".frznforge.json": `{"description":"from the repo file","tags":["file-topic"]}`,
	}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	source := giteaRemoteSource()
	source.Overrides = &config.RepoMetaInput{Name: strPtr("Widget (config)")}
	cfg := remoteTestConfig(root, cacheDir, "auto")
	stub := &stubImporter{}

	prepared, err := PrepareRemote(ctx, resolvedRemote(cacheDir, source), cfg, PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(stub),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	if !prepared.Ready || prepared.Action != MirrorCloned {
		t.Fatalf("ready=%v action=%s", prepared.Ready, prepared.Action)
	}
	if len(prepared.Warnings) != 0 {
		t.Fatalf("warnings = %v", warningCodes(prepared.Warnings))
	}
	wantSequence(t, "importer calls", stub.recorded(), []string{"meta", "releases"})

	scanned, err := ScanRepo(ctx, prepared.ScanSource, remoteScanOptions())
	if err != nil {
		t.Fatalf("ScanRepo: %v", err)
	}
	if scanned.Skipped {
		t.Fatal("repo was skipped")
	}
	repo := scanned.Repo

	if repo.Name != "Widget (config)" { // config wins
		t.Errorf("name = %q", repo.Name)
	}
	wantStr(t, "description", repo.Description, "from the repo file") // .frznforge.json beats the provider
	wantSequence(t, "tags", repo.Tags, []string{"file-topic"})
	// The provider still fills the gaps the other two layers left.
	wantStr(t, "links.homepage", repo.Links.Homepage, "https://provider.example/home")
	wantStr(t, "links.upstream", repo.Links.Upstream, "https://gitea.example.com/acme/widget")
	if repo.License == nil || repo.License.Source != "config" ||
		repo.License.Spdx == nil || *repo.License.Spdx != "Apache-2.0" || repo.License.File != nil {
		t.Errorf("license = %+v", repo.License)
	}
	if repo.Slug != "widget" {
		t.Errorf("slug = %q", repo.Slug)
	}
	src := repo.Source
	if src.Type != "gitea" || src.Project != nil {
		t.Errorf("source = %+v", src)
	}
	wantStr(t, "source.host", src.Host, "https://gitea.example.com")
	wantStr(t, "source.owner", src.Owner, "acme")
	wantStr(t, "source.repo", src.Repo, "widget")
	wantStr(t, "source.webUrl", src.WebURL, "https://gitea.example.com/acme/widget")
	wantStr(t, "source.cloneUrl", src.CloneURL, "https://gitea.example.com/acme/widget.git")
}

func TestPrepareRemoteImportsReleasesAndHonoursTagMode(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	source := giteaRemoteSource()
	cacheDir := remoteTempDir(t, "cache")
	cfg := remoteTestConfig(root, cacheDir, "auto")

	prepared, err := PrepareRemote(ctx, resolvedRemote(cacheDir, source), cfg, PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	scanned, err := ScanRepo(ctx, prepared.ScanSource, remoteScanOptions())
	if err != nil || scanned.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, scanned.Skipped)
	}
	if scanned.Repo.ReleaseMode != "provider" {
		t.Errorf("releaseMode = %q", scanned.Repo.ReleaseMode)
	}
	wantSequence(t, "release tags", tags(scanned.Repo.Releases), []string{"v1.0.0"})

	// The repo's own file overrides the source default; imported releases are then dropped.
	tagMode := testsupport.Create(t, "widget", "main")
	tagMode.WriteAndCommit(map[string]string{".frznforge.json": `{"releaseMode":"tags"}`}, "first",
		testsupport.CommitOptions{Date: testsupport.At(0)})
	cacheDir2 := remoteTempDir(t, "cache")
	cfg2 := remoteTestConfig(root, cacheDir2, "auto")
	prepared2, err := PrepareRemote(ctx, resolvedRemote(cacheDir2, source), cfg2, PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
		EnsureMirror:   localMirror(tagMode.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	scanned2, err := ScanRepo(ctx, prepared2.ScanSource, remoteScanOptions())
	if err != nil || scanned2.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, scanned2.Skipped)
	}
	if scanned2.Repo.ReleaseMode != "tags" {
		t.Errorf("releaseMode = %q", scanned2.Repo.ReleaseMode)
	}
	if len(scanned2.Repo.Releases) != 0 {
		t.Errorf("tag mode kept imported releases: %v", tags(scanned2.Repo.Releases))
	}
}

func TestPrepareRemoteFetchNeverServesCachedProviderData(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	source := giteaRemoteSource()
	resolved := resolvedRemote(cacheDir, source)

	// Seed the cache with a normal run first.
	online, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("warm run: %v", err)
	}

	stub := &stubImporter{}
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "never"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(stub),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("offline run: %v", err)
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("an offline run called the API: %v", got)
	}
	if !prepared.Ready || prepared.Action != MirrorCached {
		t.Fatalf("ready=%v action=%s", prepared.Ready, prepared.Action)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings), []string{"remote-cache-stale"})
	if !strings.Contains(prepared.Warnings[0].Message, "came from the cache") {
		t.Errorf("message = %q", prepared.Warnings[0].Message)
	}
	// The offline build must publish exactly what the online one did: dropping the description,
	// topics, links and releases here silently rewrites the repo's pages.
	if !sameMetaLayer(online.ProviderMeta, prepared.ProviderMeta) {
		t.Errorf("provider layer drifted:\n online   = %+v\n offline  = %+v", online.ProviderMeta, prepared.ProviderMeta)
	}
	wantSequence(t, "releases", tags(prepared.Releases), []string{"v1.0.0"})
	wantStr(t, "source.webUrl", prepared.ScanSource.Source.WebURL, "https://gitea.example.com/acme/widget")
}

func TestPrepareRemoteFallsBackToCachedProviderDataWhenTheAPIFails(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	online, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("warm run: %v", err)
	}

	boom := &ImporterError{Kind: KindNetwork, Message: "GET https://gitea.example.com/api/v1/repos/acme/widget failed"}
	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{metaErr: boom, releasesErr: boom}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("degraded run: %v", err)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings),
		[]string{"remote-fetch-failed", "remote-fetch-failed", "remote-cache-stale"})
	if !strings.Contains(prepared.Warnings[2].Message, "cached provider metadata and releases were used") {
		t.Errorf("message = %q", prepared.Warnings[2].Message)
	}
	if !sameMetaLayer(online.ProviderMeta, prepared.ProviderMeta) {
		t.Errorf("provider layer drifted after a failed call")
	}
	wantSequence(t, "releases", tags(prepared.Releases), []string{"v1.0.0"})
	// An ATTEMPTED call that failed is a recorded false, never the nil that means "not attempted"
	// — the run log carries a nil forward, which would leave metaOk true from the run before.
	if prepared.FetchStatus == nil || prepared.FetchStatus.Meta == nil || *prepared.FetchStatus.Meta {
		t.Errorf("a failed metadata fetch must not report metaOk: %+v", prepared.FetchStatus)
	}
}

func TestPrepareRemoteSaysSoWhenThereIsNothingCached(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	// Warm only the mirror, never the provider cache.
	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: noImporter, EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "never"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(&stubImporter{}), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("offline run: %v", err)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings), []string{"remote-cache-stale"})
	if !strings.Contains(prepared.Warnings[0].Message, "nothing is cached") {
		t.Errorf("message = %q", prepared.Warnings[0].Message)
	}
	if prepared.ProviderMeta != nil {
		t.Errorf("providerMeta = %+v, want nil", prepared.ProviderMeta)
	}
}

func TestPrepareRemoteWarnsWhenTheReleaseWalkWasTruncated(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	cacheDir := remoteTempDir(t, "cache")
	cfg := remoteTestConfig(remoteTempDir(t, "root"), cacheDir, "auto")
	prepared, err := PrepareRemote(ctx, resolvedRemote(cacheDir, giteaRemoteSource()), cfg, PrepareRemoteDeps{
		Env: Env{},
		CreateImporter: alwaysStub(&stubImporter{
			releases: []model.Release{stubRelease("v1.0.0")}, truncated: true,
		}),
		EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings), []string{"remote-fetch-failed"})
	if !strings.Contains(prepared.Warnings[0].Message, "more releases than one build will page through") {
		t.Errorf("message = %q", prepared.Warnings[0].Message)
	}
}

func TestPrepareRemoteNamesTheRepoFromTheConfigNotTheCacheDirectory(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	source := giteaRemoteSource()
	source.Owner, source.Repo = "Acme", "MyProject"
	resolved := resolvedRemote(cacheDir, source)
	cfg := remoteTestConfig(root, cacheDir, "auto")

	// No provider metadata at all: the fallback must still be the configured name, not the
	// lower-cased, hash-suffixed mirror directory it happens to live in.
	prepared, err := PrepareRemote(ctx, resolved, cfg, PrepareRemoteDeps{
		Env: Env{}, CreateImporter: noImporter, EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	if prepared.ScanSource.Slug != "myproject" || prepared.ScanSource.DefaultName != "MyProject" {
		t.Fatalf("slug=%q defaultName=%q", prepared.ScanSource.Slug, prepared.ScanSource.DefaultName)
	}
	scanned, err := ScanRepo(ctx, prepared.ScanSource, remoteScanOptions())
	if err != nil || scanned.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, scanned.Skipped)
	}
	if scanned.Repo.Name != "MyProject" || scanned.Repo.Slug != "myproject" {
		t.Errorf("name=%q slug=%q", scanned.Repo.Name, scanned.Repo.Slug)
	}

	// ...and the provider's own spelling wins over the config when the API answers.
	meta := stubProviderMeta()
	meta.Name = strPtr("MyProject!")
	withAPI, err := PrepareRemote(ctx, resolved, cfg, PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{meta: &meta}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	rescanned, err := ScanRepo(ctx, withAPI.ScanSource, remoteScanOptions())
	if err != nil || rescanned.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, rescanned.Skipped)
	}
	if rescanned.Repo.Name != "MyProject!" {
		t.Errorf("name = %q", rescanned.Repo.Name)
	}
}

func TestPrepareRemoteTruncatesAnOverLongProviderDescription(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	cacheDir := remoteTempDir(t, "cache")
	cfg := remoteTestConfig(remoteTempDir(t, "root"), cacheDir, "auto")
	// 350 astral characters: 350 code points but 700 UTF-16 units, which is what the artifact's
	// 300-character limit counts. Truncating by code points left 595 units and blew up artifact
	// validation, killing the whole build over one remote description.
	meta := stubProviderMeta()
	meta.Description = strPtr(strings.Repeat("\U0001F600", 350))

	prepared, err := PrepareRemote(ctx, resolvedRemote(cacheDir, giteaRemoteSource()), cfg, PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{meta: &meta}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings), []string{"description-truncated"})
	if prepared.ProviderMeta == nil || prepared.ProviderMeta.Description == nil {
		t.Fatal("the description was dropped rather than truncated")
	}
	if utf16Len(*prepared.ProviderMeta.Description) > MaxDescription {
		t.Errorf("description is still %d UTF-16 units", utf16Len(*prepared.ProviderMeta.Description))
	}
	if bad := metaLayerBadFields(prepared.ProviderMeta); len(bad) != 0 {
		t.Errorf("layer still fails validation: %v", bad)
	}

	// The end-to-end guarantee: this repo can be validated for writing.
	scanned, err := ScanRepo(ctx, prepared.ScanSource, remoteScanOptions())
	if err != nil || scanned.Skipped {
		t.Fatalf("ScanRepo: %v skipped=%v", err, scanned.Skipped)
	}
	data := model.ForgeData{
		SchemaVersion: model.SchemaVersion,
		Repos:         []model.Repo{*scanned.Repo},
		Notes:         []model.Note{}, Organizations: []model.Organization{},
		Hosting: []model.HostedSite{}, Warnings: []model.Warning{},
	}
	if err := model.Validate(&data); err != nil {
		t.Fatalf("artifact validation: %v", err)
	}
}

func TestPrepareRemoteReportsMissingWithNoCacheAndNoNetwork(t *testing.T) {
	cacheDir := remoteTempDir(t, "cache")
	cfg := remoteTestConfig(remoteTempDir(t, "root"), cacheDir, "never")
	prepared, err := PrepareRemote(context.Background(), resolvedRemote(cacheDir, giteaRemoteSource()), cfg,
		PrepareRemoteDeps{Env: Env{}, CreateImporter: alwaysStub(&stubImporter{})}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	if prepared.Ready || prepared.Action != MirrorMissing {
		t.Fatalf("ready=%v action=%s", prepared.Ready, prepared.Action)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings),
		[]string{"remote-cache-stale", "remote-fetch-failed"})
}

func TestPrepareRemoteMapsImporterFailuresOntoWarningCodes(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})
	root := remoteTempDir(t, "root")

	run := func(kind ImporterErrorKind, env Env) PrepareRemoteResult {
		t.Helper()
		cacheDir := remoteTempDir(t, "cache")
		boom := &ImporterError{Kind: kind, Message: "GET https://gitea.example.com/api/v1/repos/acme/widget failed"}
		res, err := PrepareRemote(ctx, resolvedRemote(cacheDir, giteaRemoteSource()),
			remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
				Env:            env,
				CreateImporter: alwaysStub(&stubImporter{metaErr: boom, releasesErr: boom}),
				EnsureMirror:   localMirror(origin.Dir),
			}, PrepareRemoteOptions{})
		if err != nil {
			t.Fatalf("PrepareRemote: %v", err)
		}
		return res
	}

	anon := run(KindAuth, Env{})
	wantSequence(t, "anonymous auth failure", warningCodes(anon.Warnings),
		[]string{"remote-auth-missing", "remote-auth-missing"})
	if !strings.Contains(anon.Warnings[0].Message, "FRZNFORGE_GITEA_TOKEN or GITEA_TOKEN") {
		t.Errorf("message = %q", anon.Warnings[0].Message)
	}
	// A failed metadata call still leaves a scannable mirror.
	if !anon.Ready {
		t.Error("a metadata failure must not lose the mirror")
	}

	withToken := run(KindAuth, Env{"GITEA_TOKEN": "tok"})
	wantSequence(t, "authenticated auth failure", warningCodes(withToken.Warnings),
		[]string{"remote-fetch-failed", "remote-fetch-failed"})

	limited := run(KindRateLimit, Env{})
	wantSequence(t, "rate limit", warningCodes(limited.Warnings),
		[]string{"remote-rate-limited", "remote-rate-limited"})

	offline := run(KindNetwork, Env{"GITEA_TOKEN": "tok"})
	wantSequence(t, "network failure", warningCodes(offline.Warnings),
		[]string{"remote-fetch-failed", "remote-fetch-failed"})
}

func TestPrepareRemoteNeverLeaksTheTokenIntoAWarning(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	cacheDir := remoteTempDir(t, "cache")
	cfg := remoteTestConfig(remoteTempDir(t, "root"), cacheDir, "auto")
	const token = "tok-abcdef123456"
	boom := &ImporterError{
		Kind:    KindNetwork,
		Message: "https://oauth2:" + token + "@gitea.example.com refused the connection",
	}
	prepared, err := PrepareRemote(ctx, resolvedRemote(cacheDir, giteaRemoteSource()), cfg, PrepareRemoteDeps{
		Env:            Env{"GITEA_TOKEN": token},
		CreateImporter: alwaysStub(&stubImporter{metaErr: boom, releasesErr: boom}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("PrepareRemote: %v", err)
	}
	if len(prepared.Warnings) == 0 {
		t.Fatal("expected warnings")
	}
	for _, w := range prepared.Warnings {
		if strings.Contains(w.Message, token) {
			t.Errorf("warning leaked the token: %q", w.Message)
		}
		if !strings.Contains(w.Message, "***") {
			t.Errorf("warning was not redacted: %q", w.Message)
		}
	}
}

func TestPrepareRemoteWarnsAndKeepsTheCachedMirrorWhenTheRefreshFails(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(&stubImporter{}), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	gone := filepath.Join(filepath.Dir(origin.Dir), "moved-away")
	if err := os.Rename(origin.Dir, gone); err != nil {
		t.Fatalf("rename: %v", err)
	}
	t.Cleanup(func() { _ = os.Rename(gone, origin.Dir) })

	prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(&stubImporter{}), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("degraded run: %v", err)
	}
	if !prepared.Ready || prepared.Action != MirrorCached {
		t.Fatalf("ready=%v action=%s", prepared.Ready, prepared.Action)
	}
	wantSequence(t, "warnings", warningCodes(prepared.Warnings),
		[]string{"remote-fetch-failed", "remote-cache-stale"})
	if prepared.FetchStatus == nil || prepared.FetchStatus.Git == nil || *prepared.FetchStatus.Git {
		t.Errorf("a failed refresh must report gitOk=false: %+v", prepared.FetchStatus)
	}
}

func TestPrepareRemoteFreshnessWindowTouchesNothingAndWarnsAboutNothing(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	first, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
		EnsureMirror:   localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("warm run: %v", err)
	}

	stub := &stubImporter{}
	skipped, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env:            Env{},
		CreateImporter: alwaysStub(stub),
		EnsureMirror: func(context.Context, config.RepoSourceConfig, string, EnsureMirrorOptions) EnsureMirrorResult {
			t.Error("the freshness window must not touch git")
			return EnsureMirrorResult{}
		},
	}, PrepareRemoteOptions{SkipFetch: true})
	if err != nil {
		t.Fatalf("window run: %v", err)
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("the freshness window called the API: %v", got)
	}
	if skipped.Action != MirrorReused || !skipped.Ready {
		t.Fatalf("action=%s ready=%v", skipped.Action, skipped.Ready)
	}
	// No warning: the cache is exactly what a fetch would have returned, so the bytes cannot
	// differ — and a warning would make the same commits publish differently depending on timing.
	if len(skipped.Warnings) != 0 {
		t.Errorf("warnings = %v", warningCodes(skipped.Warnings))
	}
	// Nothing was attempted, so the caller keeps the previous run's stamp rather than inventing one.
	if skipped.FetchStatus != nil {
		t.Errorf("fetchStatus = %+v, want nil", skipped.FetchStatus)
	}
	if !sameMetaLayer(first.ProviderMeta, skipped.ProviderMeta) {
		t.Error("the replayed metadata layer differs from the one it replaced")
	}
	wantSequence(t, "releases", tags(skipped.Releases), []string{"v1.0.0"})
}

func TestPrepareRemoteBackfillSpendsNothingOnGit(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	if _, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(&stubImporter{}), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{}); err != nil {
		t.Fatalf("warm run: %v", err)
	}

	// This repo already has its metadata, so a backfill needs nothing from the network at all.
	stub := &stubImporter{}
	filled, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(stub), Git: refuseGit(t),
	}, PrepareRemoteOptions{BackfillMetadata: true})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("backfill spent quota on a repo that already had metadata: %v", got)
	}
	if filled.Action != MirrorReused || len(filled.Warnings) != 0 {
		t.Errorf("action=%s warnings=%v", filled.Action, warningCodes(filled.Warnings))
	}

	// A repo with NO cached metadata does call the API — and still never touches git.
	empty := remoteTempDir(t, "cache")
	emptyResolved := resolvedRemote(empty, giteaRemoteSource())
	gap := &stubImporter{}
	res, err := PrepareRemote(ctx, emptyResolved, remoteTestConfig(root, empty, "auto"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(gap),
	}, PrepareRemoteOptions{BackfillMetadata: true})
	if err != nil {
		t.Fatalf("backfill gap: %v", err)
	}
	wantSequence(t, "importer calls", gap.recorded(), []string{"meta", "releases"})
	// No mirror exists and git was forced to 'never', so the repo reports missing without a
	// misleading "the mirror could not be refreshed" warning.
	if res.Action != MirrorMissing {
		t.Errorf("action = %s", res.Action)
	}
	if containsString(warningCodes(res.Warnings), "remote-cache-stale") {
		t.Errorf("backfill flagged a mirror it deliberately did not update: %v", warningCodes(res.Warnings))
	}
}

func TestPrepareRemoteIsDeterministic(t *testing.T) {
	ctx := context.Background()
	origin := testsupport.Create(t, "widget", "main")
	origin.WriteAndCommit(map[string]string{"a.txt": "one\n"}, "first", testsupport.CommitOptions{Date: testsupport.At(0)})

	root := remoteTempDir(t, "root")
	cacheDir := remoteTempDir(t, "cache")
	resolved := resolvedRemote(cacheDir, giteaRemoteSource())

	scan := func() []byte {
		t.Helper()
		prepared, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "auto"), PrepareRemoteDeps{
			Env:            Env{},
			CreateImporter: alwaysStub(&stubImporter{releases: []model.Release{stubRelease("v1.0.0")}}),
			EnsureMirror:   localMirror(origin.Dir),
		}, PrepareRemoteOptions{})
		if err != nil {
			t.Fatalf("PrepareRemote: %v", err)
		}
		res, err := ScanRepo(ctx, prepared.ScanSource, remoteScanOptions())
		if err != nil || res.Skipped {
			t.Fatalf("ScanRepo: %v skipped=%v", err, res.Skipped)
		}
		data := model.ForgeData{
			SchemaVersion: model.SchemaVersion,
			Repos:         []model.Repo{*res.Repo},
			Notes:         []model.Note{}, Organizations: []model.Organization{},
			Hosting: []model.HostedSite{}, Warnings: []model.Warning{},
		}
		encoded, err := model.Serialize(data)
		if err != nil {
			t.Fatalf("Serialize: %v", err)
		}
		return encoded
	}

	first := scan()
	if second := scan(); string(second) != string(first) {
		t.Error("two runs of the same repo at the same commits produced different bytes")
	}
	// ...and again offline, off the provider cache the first two runs wrote.
	offline, err := PrepareRemote(ctx, resolved, remoteTestConfig(root, cacheDir, "never"), PrepareRemoteDeps{
		Env: Env{}, CreateImporter: alwaysStub(&stubImporter{}), EnsureMirror: localMirror(origin.Dir),
	}, PrepareRemoteOptions{})
	if err != nil {
		t.Fatalf("offline run: %v", err)
	}
	wantSequence(t, "offline releases", tags(offline.Releases), []string{"v1.0.0"})
	if offline.ProviderMeta == nil || offline.ProviderMeta.Description == nil ||
		*offline.ProviderMeta.Description != "from the provider" {
		t.Errorf("offline description = %+v", offline.ProviderMeta)
	}
}

func TestProviderCachePathSitsBesideTheMirror(t *testing.T) {
	cases := map[string]string{
		filepath.Join("c", "gitea", "widget-abcd1234.git"): filepath.Join("c", "gitea", "widget-abcd1234.meta.json"),
		filepath.Join("c", "widget.GIT"):                   filepath.Join("c", "widget.meta.json"),
		filepath.Join("c", "widget"):                       filepath.Join("c", "widget.meta.json"),
	}
	for in, want := range cases {
		if got := ProviderCachePathFor(in); got != want {
			t.Errorf("ProviderCachePathFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDeriveWebURLEncodesEachSegment(t *testing.T) {
	cases := []struct {
		source config.RepoSourceConfig
		want   string
	}{
		{config.RepoSourceConfig{Type: "github", Host: "https://api.github.com", Owner: "a", Repo: "b"},
			"https://github.com/a/b"},
		{config.RepoSourceConfig{Type: "github", Host: "https://git.example.com/api/v3", Owner: "a", Repo: "b"},
			"https://git.example.com/a/b"},
		{config.RepoSourceConfig{Type: "gitlab", Host: "https://gitlab.com/", Project: "g/s/p"},
			"https://gitlab.com/g/s/p"},
		// Every segment is encoded the way encodeURIComponent encodes it: these are artifact bytes.
		{config.RepoSourceConfig{Type: "gitea", Host: "https://gitea.example.com", Owner: "acme", Repo: "my repo"},
			"https://gitea.example.com/acme/my%20repo"},
	}
	for _, tc := range cases {
		if got := deriveWebURL(tc.source); got != tc.want {
			t.Errorf("deriveWebURL(%s/%s%s) = %q, want %q", tc.source.Type, tc.source.Owner, tc.source.Project, got, tc.want)
		}
	}
}

// providerCacheGolden is the exact file writeProviderCache produces, and the exact file
// `JSON.stringify({version, meta, releases}, null, 2) + '\n'` produces on the TypeScript side.
//
// Key ORDER is the part worth pinning: the two implementations share this cache directory, and
// the TypeScript emits its object literal's insertion order while Go emits struct declaration
// order. They have to be the same order for a cache written by either to read back — and to diff
// — as the same file.
const providerCacheGolden = `{
  "version": 1,
  "meta": {
    "name": "widget",
    "description": "from the provider",
    "homepage": null,
    "topics": [
      "a"
    ],
    "license": null,
    "defaultBranch": null,
    "webUrl": "https://gitea.example.com/acme/widget",
    "cloneUrl": "https://gitea.example.com/acme/widget.git",
    "issuesUrl": null,
    "template": false,
    "archived": false
  },
  "releases": []
}
`

func TestProviderCacheFileShape(t *testing.T) {
	dir := remoteTempDir(t, "cache")
	file := filepath.Join(dir, "widget.meta.json")
	writeProviderCache(file, providerCache{
		Version: providerCacheVersion,
		Meta: &ImportedRepoMeta{
			Name:        strPtr("widget"),
			Description: strPtr("from the provider"),
			Topics:      []string{"a"},
			WebURL:      "https://gitea.example.com/acme/widget",
			CloneURL:    "https://gitea.example.com/acme/widget.git",
		},
		Releases: []model.Release{},
	})
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if string(raw) != providerCacheGolden {
		t.Errorf("cache file differs.\n got:\n%s\nwant:\n%s", raw, providerCacheGolden)
	}

	back := readProviderCache(file)
	if back == nil || back.Meta == nil {
		t.Fatal("the cache did not survive a round trip")
	}
	wantStr(t, "cached name", back.Meta.Name, "widget")
	wantSequence(t, "cached topics", back.Meta.Topics, []string{"a"})
	if back.Releases == nil {
		t.Error("releases came back nil, which would serialise as null")
	}
}

func TestReadProviderCacheDegradesRatherThanFails(t *testing.T) {
	dir := remoteTempDir(t, "cache")
	file := filepath.Join(dir, "x.meta.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Absent, unreadable, corrupt or written by another version all mean "nothing cached".
	if got := readProviderCache(filepath.Join(dir, "missing.meta.json")); got != nil {
		t.Errorf("a missing cache read as %+v", got)
	}
	for name, body := range map[string]string{
		"corrupt":         "{not json",
		"older version":   `{"version":0,"meta":null,"releases":[]}`,
		"not an object":   `[1,2,3]`,
		"null document":   `null`,
		"missing version": `{"meta":null,"releases":[]}`,
	} {
		write(body)
		if got := readProviderCache(file); got != nil {
			t.Errorf("%s read as %+v", name, got)
		}
	}

	// A release the artifact schema would reject is dropped rather than taking the cache with
	// it: the other releases beside it are still exactly what a fetch would have returned.
	write(`{"version":1,"meta":null,"releases":[
		{"tag":"good","name":"good","body":"","url":null,"prerelease":false,
		 "publishedAt":"2026-01-01T00:00:00Z","author":null,"assets":[]},
		{"tag":"bad","name":"bad","body":"","url":null,"prerelease":false,
		 "publishedAt":"whenever","author":null,"assets":[]}
	]}`)
	got := readProviderCache(file)
	if got == nil {
		t.Fatal("one bad release discarded the whole cache")
	}
	wantSequence(t, "kept releases", tags(got.Releases), []string{"good"})

	// A meta block of the wrong shape degrades to "no metadata", not to a discarded cache.
	write(`{"version":1,"meta":42,"releases":[]}`)
	got = readProviderCache(file)
	if got == nil || got.Meta != nil {
		t.Errorf("a malformed meta block gave %+v", got)
	}
}

// sameMetaLayer compares two provider metadata layers field by field.
func sameMetaLayer(a, b *config.RepoMetaInput) bool {
	if a == nil || b == nil {
		return a == b
	}
	encodedA, errA := StableStringify(a)
	encodedB, errB := StableStringify(b)
	return errA == nil && errB == nil && encodedA == encodedB
}
