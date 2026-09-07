package wizard

// The endpoints. One handler per route, in the order the page uses them:
//
//	GET  /                     the page itself (server.go)
//	GET  /api/context          providers, token status, the flags the terminal started with
//	GET  /api/config           the config as the loader sees it, plus the schema's defaults
//	GET  /api/profile          the profile file, split into frontmatter and body
//	POST /api/repos            list an account's repositories through the provider
//	POST /api/preview          the exact snippet a write would splice in
//	POST /api/write            splice picked repositories into `repos`
//	POST /api/config/write     apply settings operations to the config file
//	POST /api/profile/preview  render the profile body with the site's own renderer
//	POST /api/profile/write    save the profile body, frontmatter preserved
//	POST /api/upload           store an avatar (upload.go)
//	POST /api/done, /api/cancel  end the session (server.go)

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"frznforge/internal/config"
	"frznforge/internal/ingest"
	"frznforge/internal/markdown"
)

// maxEntries caps one picker request. An account with more repositories than this is not being
// picked from a table, it is being scripted.
const maxEntries = 500

// palettes are the theme palettes the page offers.
//
// config's own list is unexported, and config.Validate is the thing that actually enforces it: a
// palette added there and forgotten here is refused at save time with the parser's message
// rather than silently written, which is the failure mode worth having.
var palettes = []string{"hearth", "frost"}

// defaultConfig is the config the schema produces from nothing but the two required fields — the
// values the page shows for fields the file leaves unset, and what it falls back to entirely
// when the file does not load.
var defaultConfig = sync.OnceValue(func() *config.Config {
	parsed, err := config.ParseBytes([]byte(`{"owner":{"name":"Owner","handle":"owner"}}`))
	if err != nil {
		return nil
	}
	return parsed
})

// nullable renders an absent string as JSON null rather than "", because the page tests these
// with `if (!cfg.configPath)` and an empty string would be a different kind of nothing.
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func (s *Session) configName() any {
	if s.ConfigPath == "" {
		return nil
	}
	return filepath.Base(s.ConfigPath)
}

/* ------------------------------------------------------------------ /api/context */

// tokenReport names the environment variables a provider's token can come from, and whether one
// of them is set. The token itself is deliberately not here.
type tokenReport struct {
	Names []string `json:"names"`
	From  any      `json:"from"`
	Found bool     `json:"found"`
}

// providerReport is one provider's row: its own description plus that token report.
type providerReport struct {
	ProviderInfo
	Token tokenReport `json:"token"`
}

func (s *Session) handleContext(w http.ResponseWriter) error {
	reports := make(map[string]providerReport, len(providerNames))
	for _, name := range providerNames {
		status := statusFor(name, s.opts.Env)
		reports[name] = providerReport{
			ProviderInfo: providers[name],
			Token:        tokenReport{Names: status.names, From: nullable(status.from), Found: status.token != ""},
		}
	}
	provider := s.opts.Provider
	if provider == "" {
		provider = "github"
	}
	releases := s.opts.Releases
	if releases == "" {
		releases = "provider"
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"order":     providerNames,
		"providers": reports,
		"defaults": map[string]any{
			"provider": provider,
			"host":     nullable(s.opts.Host),
			"account":  nullable(s.opts.Account),
			"releases": releases,
		},
		"configPath": nullable(s.ConfigPath),
		"configName": s.configName(),
	}, false)
	return nil
}

/* ------------------------------------------------------------------ /api/config */

func (s *Session) handleConfig(w http.ResponseWriter) error {
	loaded := s.loadConfig()
	issues := loaded.issues
	if issues == nil {
		issues = []string{}
	}
	var current any
	if loaded.parsed != nil {
		current = loaded.parsed
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"configPath": nullable(s.ConfigPath),
		"configName": s.configName(),
		"readable":   loaded.parsed != nil,
		"issues":     issues,
		"current":    current,
		"sources":    rawSources(loaded.input),
		"defaults":   defaultConfig(),
		"palettes":   palettes,
		"writes":     s.Writes(),
	}, false)
	return nil
}

// rawSources is each `repos` entry's string fields, in array order — what the page shows in the
// sources list and what a positional remove sends back as its `expect`.
//
// Read from the file's own decoded input rather than from the parsed config: the parsed one has
// per-provider host defaults filled in, and an `expect` carrying a host the file never wrote
// would fail the very safety check it exists to pass.
func rawSources(input map[string]any) []map[string]string {
	sources := []map[string]string{}
	list, _ := input["repos"].([]any)
	for _, entry := range list {
		record, ok := entry.(map[string]any)
		if !ok {
			sources = append(sources, map[string]string{})
			continue
		}
		picked := map[string]string{}
		for _, key := range arrayRemoveKeys["repos"] {
			if text, ok := record[key].(string); ok {
				picked[key] = text
			}
		}
		sources = append(sources, picked)
	}
	return sources
}

/* ------------------------------------------------------------------ /api/config/write */

func (s *Session) handleConfigWrite(w http.ResponseWriter, body map[string]any) error {
	if s.ConfigPath == "" {
		return conflict("no " + config.Filename + " was found — nothing to edit")
	}
	// Validated before the lock, so a malformed request cannot hold up a real save.
	ops, err := asOperations(body["operations"])
	if err != nil {
		return err
	}

	s.mu.Lock()
	changed, backup, err := s.applyOps(ops)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	if changed {
		plural := "s"
		if len(ops) == 1 {
			plural = ""
		}
		s.log("Updated %s (%d change%s).", s.ConfigPath, len(ops), plural)
		if backup != "" {
			s.log("Backup: %s", backup)
		}
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"changed":    changed,
		"backup":     nullable(backup),
		"configPath": s.ConfigPath,
	}, false)
	return nil
}

// applyOps is one settings save: judge, splice, write, re-read, roll back if the file does not
// now say what the judge approved. The caller holds s.mu.
func (s *Session) applyOps(ops []settingsOp) (changed bool, backup string, err error) {
	loaded := s.loadConfig()
	if loaded.parsed == nil {
		return false, "", conflict(fmt.Sprintf("the current config does not load cleanly (%s) — fix it by hand first",
			strings.Join(loaded.issues, "; ")))
	}

	// Judge the change on a copy before touching a byte of the file. That copy is also the
	// yardstick the post-write check holds the re-read file against.
	var expected *config.Config
	if candidate := applyOpsToInput(loaded.input, ops); candidate != nil {
		encoded, err := json.Marshal(candidate)
		if err != nil {
			return false, "", badRequest("that change cannot be written as JSON: " + err.Error())
		}
		parsed, err := config.ParseBytes(encoded)
		if err != nil {
			return false, "", badRequest("that change is not a valid config: " + strings.Join(configIssues(err), "; "))
		}
		expected = parsed
	}

	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return false, "", conflict(fmt.Sprintf("could not read %s: %s", s.ConfigPath, err))
	}
	source := string(raw)
	edited, changed, err := applyOpsToSource(source, ops)
	if err != nil {
		return false, "", err
	}
	if !changed {
		return false, "", nil
	}

	backup, err = s.backupOnce(s.ConfigPath, source)
	if err != nil {
		return false, "", conflict(fmt.Sprintf("could not back up %s before editing it: %s", s.ConfigPath, err))
	}
	if err := os.WriteFile(s.ConfigPath, []byte(edited), 0o644); err != nil {
		return false, "", conflict(fmt.Sprintf("could not write %s: %s", s.ConfigPath, err))
	}

	verify := s.loadConfig()
	// 1. The written file must still load: never leave a config the build cannot read.
	if verify.parsed == nil {
		return false, "", s.restoreAndFail(source, fmt.Sprintf("the edit did not produce a loadable config (%s)",
			strings.Join(verify.issues, "; ")))
	}
	// 2. It must load to the config the pre-check approved. If the textual edit diverged from the
	//    intended change — an edit that landed inside a comment, a duplicate key appended because
	//    the original was written in a shape the walker cannot follow — the re-read config differs
	//    from `expected`, and the write is rolled back rather than silently applying the wrong
	//    thing.
	if expected != nil && !sameConfig(verify.parsed, expected) {
		return false, "", s.restoreAndFail(source,
			"the edit did not change the config the way it should have (the file may use a shape the editor cannot follow)")
	}
	s.writes++
	return true, backup, nil
}

// sameConfig compares two parsed configs by their serialised form — struct field order makes
// that stable, and it catches a divergence anywhere in the tree rather than in the fields
// somebody remembered to check.
func sameConfig(a, b *config.Config) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

// restoreAndFail puts source back after a write that must be undone, and reports why.
//
// If the restoring write itself fails (a lock, a full disk), say so plainly and name the .bak —
// the one thing guaranteed to still hold the pre-wizard bytes — rather than letting a raw
// filesystem error surface over a now-broken config.
func (s *Session) restoreAndFail(source, why string) error {
	if err := os.WriteFile(s.ConfigPath, []byte(source), 0o644); err != nil {
		message := fmt.Sprintf("%s, and restoring the previous bytes also failed (%s)", why, err)
		if backup := s.backups[s.ConfigPath]; backup != "" {
			message += " — the pre-wizard copy is at " + backup
		}
		return conflict(message + ". The config file may be broken; check it before building.")
	}
	return conflict(why + " — the file was restored unchanged")
}

/* ------------------------------------------------------------------ /api/profile */

func (s *Session) handleProfile(w http.ResponseWriter) error {
	loaded := s.loadConfig()
	file := s.profilePath(loaded.parsed)
	if file == "" {
		s.sendJSON(w, http.StatusOK, map[string]any{"available": false}, false)
		return nil
	}
	text, err := os.ReadFile(file)
	exists := err == nil
	frontmatter, body := "", ""
	if exists {
		cut := frontmatterEndOffset(string(text))
		frontmatter, body = string(text[:cut]), string(text[cut:])
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"available": true,
		"path":      file,
		"exists":    exists,
		// The frontmatter block rides along read-only; the write below re-attaches these exact
		// bytes.
		"frontmatter": frontmatter,
		"body":        body,
	}, false)
	return nil
}

func (s *Session) handleProfilePreview(w http.ResponseWriter, body map[string]any) error {
	text, ok := body["body"].(string)
	if !ok {
		return badRequest(`expected a "body" string`)
	}
	// Trusted, like the site gives the owner's own profile; no mermaid — the wizard page ships no
	// diagram bundle, so the fence stays an honest code block here.
	s.sendJSON(w, http.StatusOK, map[string]any{
		"html": markdown.Render(text, markdown.Options{Trusted: true, Mermaid: false}),
	}, false)
	return nil
}

func (s *Session) handleProfileWrite(w http.ResponseWriter, body map[string]any) error {
	next, ok := body["body"].(string)
	if !ok {
		return badRequest(`expected a "body" string`)
	}
	if strings.ContainsRune(next, 0) {
		return badRequest("the profile body cannot contain NUL bytes")
	}

	s.mu.Lock()
	file, changed, backup, err := s.writeProfile(next)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if changed {
		s.log("Updated %s.", file)
		if backup != "" {
			s.log("Backup: %s", backup)
		}
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"changed": changed,
		"path":    file,
		"backup":  nullable(backup),
	}, false)
	return nil
}

// writeProfile replaces the body of the profile file, keeping its frontmatter block byte for
// byte. The caller holds s.mu.
func (s *Session) writeProfile(next string) (file string, changed bool, backup string, err error) {
	loaded := s.loadConfig()
	file = s.profilePath(loaded.parsed)
	if file == "" {
		if loaded.parsed == nil {
			return "", false, "", conflict(fmt.Sprintf(
				"the config does not load cleanly (%s), so the profile location is not known — fix the config first",
				strings.Join(loaded.issues, "; ")))
		}
		return "", false, "", conflict("the configured profile is not an editable markdown file inside the project")
	}

	raw, readErr := os.ReadFile(file)
	existing, exists := string(raw), readErr == nil
	head := ""
	if exists {
		head = existing[:frontmatterEndOffset(existing)]
	}
	joined := joinProfile(head, next)
	if exists && existing == joined {
		return file, false, "", nil
	}

	if exists {
		if backup, err = s.backupOnce(file, existing); err != nil {
			return "", false, "", conflict(fmt.Sprintf("could not back up %s before editing it: %s", file, err))
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return "", false, "", conflict(fmt.Sprintf("could not create %s: %s", filepath.Dir(file), err))
		}
		// A file the wizard creates has no pre-wizard state, so its session backup is "none" —
		// recorded so a second save does not snapshot this first write as if it were it.
		s.markCreated(file)
	}
	if err := os.WriteFile(file, []byte(joined), 0o644); err != nil {
		return "", false, "", conflict(fmt.Sprintf("could not write %s: %s", file, err))
	}
	s.writes++
	return file, true, backup, nil
}

/* ------------------------------------------------------------------ /api/repos */

// hostLabel turns `https://api.github.com` into `api.github.com`, for something the page can show.
func hostLabel(host string) string {
	if u, err := url.Parse(host); err == nil && u.Host != "" {
		return u.Host
	}
	return host
}

func (s *Session) handleRepos(w http.ResponseWriter, body map[string]any) error {
	provider, err := asProvider(body["provider"])
	if err != nil {
		return err
	}
	info := providers[provider]
	host, err := asHost(body["host"], info.HostRequired)
	if err != nil {
		return err
	}
	if host == "" {
		host = info.DefaultHost
	}
	account, err := asAccount(body["account"])
	if err != nil {
		return err
	}

	status := statusFor(provider, s.opts.Env)
	// The one rule that keeps a token out of a stranger's logs: a host the terminal did not
	// authorise gets an anonymous request, never an Authorization header.
	trusted := hostTrustedForToken(provider, host, s.opts.Provider, s.opts.Host)
	token := ""
	if trusted {
		token = status.token
	}
	warnings := []string{}
	if !trusted && status.token != "" {
		warnings = append(warnings, fmt.Sprintf(
			"%s is not %s's own API host, so $%s was not sent to it — only public repositories are listed. "+
				"Restart with --host=%s if that host really is your instance.",
			hostLabel(host), info.Label, status.from, host))
	}

	repos, listWarnings, err := listRepos(listRequest{
		provider: provider, host: host, account: account, token: token, client: s.opts.Client,
	})
	if err != nil {
		return &httpError{status: http.StatusBadGateway, message: ingest.RedactToken(err.Error(), status.token)}
	}
	warnings = append(warnings, listWarnings...)

	// Which quick filters this listing can actually answer for. GitLab's project listings report
	// neither forks nor (for users) archived state, and a toggle that silently does nothing is
	// worse than one that is visibly unavailable.
	flags := map[string]bool{}
	for _, filter := range excludeFilters {
		known := true
		for _, repo := range repos {
			if !filter.known(repo) {
				known = false
				break
			}
		}
		flags[filter.one] = known
	}

	s.mu.Lock()
	s.listing = &listing{key: listingKey(provider, host, account), repos: repos}
	s.mu.Unlock()

	s.sendJSON(w, http.StatusOK, map[string]any{
		"provider":  provider,
		"host":      host,
		"hostLabel": hostLabel(host),
		"account":   account,
		"warnings":  warnings,
		"tokenSent": trusted && status.token != "",
		"flags":     flags,
		"repos":     repos,
	}, false)
	return nil
}

/* ------------------------------------------------------------------ /api/preview and /api/write */

// entriesFrom resolves a request into config entries: either pre-built (`entries`), or names
// picked out of the listing this session last loaded (`select`).
func (s *Session) entriesFrom(body map[string]any) ([]RepoEntry, error) {
	if raw, ok := body["entries"].([]any); ok {
		if len(raw) > maxEntries {
			return nil, badRequest("too many entries")
		}
		entries := make([]RepoEntry, 0, len(raw))
		for _, item := range raw {
			entry, err := asEntry(item)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
		}
		return entries, nil
	}

	provider, err := asProvider(body["provider"])
	if err != nil {
		return nil, err
	}
	info := providers[provider]
	host, err := asHost(body["host"], info.HostRequired)
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = info.DefaultHost
	}
	account, err := asAccount(body["account"])
	if err != nil {
		return nil, err
	}
	releases, err := asReleases(body["releases"])
	if err != nil {
		return nil, err
	}
	selected, ok := body["select"].([]any)
	if !ok {
		return nil, badRequest(`expected "entries" or "select"`)
	}
	if len(selected) > maxEntries {
		return nil, badRequest("too many entries")
	}

	s.mu.Lock()
	loaded := s.listing
	s.mu.Unlock()
	if loaded == nil || loaded.key != listingKey(provider, host, account) {
		return nil, badRequest("that repository list is no longer loaded — load it again")
	}

	wanted := map[string]bool{}
	for _, name := range selected {
		if text, ok := name.(string); ok {
			wanted[text] = true
		}
	}
	picked := make([]RemoteRepo, 0, len(wanted))
	for _, repo := range loaded.repos {
		if wanted[repo.FullName] {
			picked = append(picked, repo)
		}
	}
	if len(picked) == 0 {
		return nil, badRequest("nothing selected")
	}
	entries, err := entriesFor(provider, host, picked, releases)
	if err != nil {
		// 502, not the 500 an unwrapped error would become. Everything entriesFor rejects came
		// out of the provider's own listing — a repository name carrying a quote or a newline,
		// a host that is not a URL — so the wizard is reporting a bad answer from upstream, not
		// admitting it crashed. The distinction is the whole difference between "try again" and
		// "file a bug".
		var he *httpError
		if errors.As(err, &he) {
			return nil, err
		}
		return nil, &httpError{status: http.StatusBadGateway, message: err.Error()}
	}
	return entries, nil
}

func (s *Session) handlePreview(w http.ResponseWriter, body map[string]any) error {
	entries, err := s.entriesFrom(body)
	if err != nil {
		return err
	}
	answer := map[string]any{
		"snippet":    renderSnippet(entries),
		"changed":    false,
		"added":      len(entries),
		"skipped":    0,
		"configPath": nullable(s.ConfigPath),
	}
	if s.ConfigPath != "" {
		if source, err := os.ReadFile(s.ConfigPath); err == nil {
			if spliced, ok := insertRepos(string(source), entries); ok {
				answer["changed"] = spliced.changed
				answer["added"] = len(spliced.added)
				answer["skipped"] = len(spliced.skipped)
			}
		}
	}
	s.sendJSON(w, http.StatusOK, answer, false)
	return nil
}

func (s *Session) handleWrite(w http.ResponseWriter, body map[string]any) error {
	if s.ConfigPath == "" {
		return conflict("no " + config.Filename + " was found — copy the snippet into your config by hand")
	}
	// Validated before the lock, so a malformed request cannot hold up a real save.
	entries, err := s.entriesFrom(body)
	if err != nil {
		return err
	}

	s.mu.Lock()
	added, skipped, backup, err := s.insertEntries(entries)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	if added > 0 {
		plural := "ies"
		if added == 1 {
			plural = "y"
		}
		s.log("Wrote %d entr%s to %s.", added, plural, s.ConfigPath)
		if backup != "" {
			s.log("Backup: %s", backup)
		}
	} else {
		s.log("Everything selected was already in %s — nothing written.", s.ConfigPath)
	}
	s.sendJSON(w, http.StatusOK, map[string]any{
		"configPath": s.ConfigPath,
		"backup":     nullable(backup),
		"added":      added,
		"skipped":    skipped,
		"changed":    added > 0,
	}, false)
	return nil
}

// insertEntries splices picked repositories into the config's repos array. The caller holds s.mu.
func (s *Session) insertEntries(entries []RepoEntry) (added, skipped int, backup string, err error) {
	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return 0, 0, "", conflict(fmt.Sprintf("could not read %s: %s", s.ConfigPath, err))
	}
	source := string(raw)
	spliced, ok := insertRepos(source, entries)
	if !ok {
		return 0, 0, "", conflict(fmt.Sprintf(
			`could not find a "repos": [ … ] array in %s — paste the snippet in by hand`, s.ConfigPath))
	}
	if !spliced.changed {
		return 0, len(spliced.skipped), "", nil
	}
	if backup, err = s.backupOnce(s.ConfigPath, source); err != nil {
		return 0, 0, "", conflict(fmt.Sprintf("could not back up %s before editing it: %s", s.ConfigPath, err))
	}
	if err := os.WriteFile(s.ConfigPath, []byte(spliced.text), 0o644); err != nil {
		return 0, 0, "", conflict(fmt.Sprintf("could not write %s: %s", s.ConfigPath, err))
	}
	s.writes++
	return len(spliced.added), len(spliced.skipped), backup, nil
}
