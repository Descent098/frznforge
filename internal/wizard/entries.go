package wizard

// Repository entries: the shapes the picker writes into `repos: [ … ]`, and the splice that
// puts them there. Ported from the entry half of scripts/cli.ts.
//
// Everything here treats a provider listing as hostile input. A repo name arrives over the
// network and ends up inside a file the user reads for years, so it is validated at the one
// place entries are built rather than at each call site.

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// RepoEntry is one configured source, as the picker writes it.
type RepoEntry struct {
	Type string
	Host string
	// Owner and Repo name a GitHub/Gitea/Forgejo repository.
	Owner string
	Repo  string
	// Project is GitLab's full namespaced path.
	Project string
	Slug    string
	// Releases is "provider" or "tags".
	Releases string
}

// entryKeyOrder is the order fields are written in, so two runs produce the same bytes.
var entryKeyOrder = []struct {
	key string
	get func(RepoEntry) string
}{
	{"type", func(e RepoEntry) string { return e.Type }},
	{"host", func(e RepoEntry) string { return e.Host }},
	{"owner", func(e RepoEntry) string { return e.Owner }},
	{"repo", func(e RepoEntry) string { return e.Repo }},
	{"project", func(e RepoEntry) string { return e.Project }},
	{"slug", func(e RepoEntry) string { return e.Slug }},
	{"releases", func(e RepoEntry) string { return e.Releases }},
}

// safeField is the characters a name, owner, project or slug may contain before it is written
// into the config. A superset of what GitHub, GitLab, Gitea and Forgejo actually allow in a
// path, so a value that fails this came from a hostile or broken API response rather than from
// a real repository.
var safeField = regexp.MustCompile(`^[A-Za-z0-9._\-/~+]{1,200}$`)

func assertSafeField(value, key string) (string, error) {
	if !safeField.MatchString(value) {
		return "", fmt.Errorf("the provider returned a %s that cannot go in a config file: %q", key, value)
	}
	return value, nil
}

// unsafeHostRune reports the characters that have no business in a URL and every business in an
// injection attempt: control and format characters, every kind of whitespace (U+2028 and U+2029
// included), quotes, backslashes and angle brackets.
func unsafeHostRune(r rune) bool {
	switch r {
	case '\'', '"', '`', '<', '>', '\\':
		return true
	}
	return unicode.IsSpace(r) || unicode.Is(unicode.C, r)
}

// assertSafeHost validates a host as TEXT, not just as whatever url.Parse makes of it.
//
// A URL parser is permissive about things a config file is not: the text is what ends up inside
// a string literal, so a line terminator or a quote in it is refused here even when the parse
// would have succeeded.
func assertSafeHost(value string) (string, error) {
	if len(value) > 300 || strings.IndexFunc(value, unsafeHostRune) >= 0 {
		return "", fmt.Errorf("not a usable host: %q", value)
	}
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not a URL: %s", value)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("host must be http or https")
	}
	if u.User != nil {
		return "", fmt.Errorf("host must not carry credentials")
	}
	return value, nil
}

// slugify turns a repository name into a slug: lowercase, non-alphanumeric runs to "-".
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
			continue
		}
		if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return "repo"
	}
	return s
}

// renderEntry is one config array element on one line.
func renderEntry(entry RepoEntry) string {
	var fields object
	for _, f := range entryKeyOrder {
		if v := f.get(entry); v != "" {
			fields = append(fields, field{key: f.key, value: v})
		}
	}
	return renderValue(fields)
}

// renderSnippet is the `"repos": [ … ]` fragment the page shows and a user can paste by hand.
func renderSnippet(entries []RepoEntry) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = "    " + renderEntry(e) + ","
	}
	return "  \"repos\": [\n" + strings.Join(lines, "\n") + "\n  ],"
}

// entryKey is the identity of a source, ignoring cosmetic fields: two entries with the same key
// describe the same remote repository and must never be added twice.
func entryKey(entry RepoEntry) string {
	host := entry.Host
	if host == "" {
		host = providers[entry.Type].DefaultHost
	}
	host = strings.ToLower(strings.TrimRight(host, "/"))
	id := entry.Project
	if id == "" {
		id = entry.Owner + "/" + entry.Repo
	}
	return entry.Type + "|" + host + "|" + strings.ToLower(id)
}

// entryFor builds a config entry for one listed repository.
func entryFor(provider, host string, repo RemoteRepo, releases, slug string) (RepoEntry, error) {
	entry := RepoEntry{Type: provider, Releases: releases}
	normalised := strings.TrimRight(host, "/")
	if normalised != "" && normalised != providers[provider].DefaultHost {
		safe, err := assertSafeHost(normalised)
		if err != nil {
			return RepoEntry{}, err
		}
		entry.Host = safe
	}
	// A listing is remote input. Validating here — the one place listing-derived entries are
	// built — is what stops a hostile or broken API response putting a line break, a quote or a
	// stray key into a config file.
	var err error
	if provider == "gitlab" {
		project := repo.Project
		if project == "" {
			project = repo.FullName
		}
		if entry.Project, err = assertSafeField(project, "project path"); err != nil {
			return RepoEntry{}, err
		}
	} else {
		if entry.Owner, err = assertSafeField(repo.Owner, "owner"); err != nil {
			return RepoEntry{}, err
		}
		if entry.Repo, err = assertSafeField(repo.Name, "repository name"); err != nil {
			return RepoEntry{}, err
		}
	}
	if slug != "" {
		if entry.Slug, err = assertSafeField(slug, "slug"); err != nil {
			return RepoEntry{}, err
		}
	}
	return entry, nil
}

// entriesFor builds the entries for a whole selection, adding an explicit slug only where two
// picked repositories would otherwise collide (the default slug is the repository name).
func entriesFor(provider, host string, repos []RemoteRepo, releases string) ([]RepoEntry, error) {
	counts := map[string]int{}
	for _, r := range repos {
		counts[slugify(r.Name)]++
	}
	out := make([]RepoEntry, 0, len(repos))
	for _, r := range repos {
		slug := ""
		if counts[slugify(r.Name)] > 1 {
			slug = slugify(r.Owner + "-" + r.Name)
		}
		entry, err := entryFor(provider, host, r, releases, slug)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}

/* ------------------------------------------------------------------ the repos splice */

// insertResult reports what an insertRepos call did.
type insertResult struct {
	text string
	// added are the entries that were written; skipped were already in the file.
	added   []RepoEntry
	skipped []RepoEntry
	changed bool
}

// existingKeys collects the identity keys of the sources already in an array body.
func existingKeys(body string) map[string]bool {
	keys := map[string]bool{}
	for _, span := range topLevelItemSpans(body) {
		item := stripComments(span.text)
		typ, ok := readField(item, "type")
		if !ok || providers[typ].Label == "" {
			continue
		}
		entry := RepoEntry{Type: typ}
		entry.Host, _ = readField(item, "host")
		entry.Owner, _ = readField(item, "owner")
		entry.Repo, _ = readField(item, "repo")
		entry.Project, _ = readField(item, "project")
		keys[entryKey(entry)] = true
	}
	return keys
}

// insertRepos splices entries into the `repos: [ … ]` array of a config file's source text.
//
// Purely textual, like every editor in this package: nothing outside the array is touched.
// ok is false when there is no `repos` array to splice into, which is the caller's cue to fall
// back to "paste this in yourself".
//
// Idempotent: an entry whose identity already appears in the array is reported as skipped, so
// running the picker twice adds nothing the second time.
func insertRepos(source string, entries []RepoEntry) (insertResult, bool) {
	open, close, ok := findRootObject(source)
	if !ok {
		return insertResult{}, false
	}
	r, found := findKeyRange(source, open, close, "repos")
	if !found || source[r.valueStart] != '[' {
		return insertResult{}, false
	}
	arrOpen := r.valueStart
	arrClose := matchBracket(source, arrOpen)
	if arrClose == -1 {
		return insertResult{}, false
	}

	// Indent the new entries to match the `"repos":` line itself, whatever the file's style is.
	lineStart := strings.LastIndexByte(source[:r.keyStart], '\n') + 1
	indent := source[lineStart:r.keyStart]
	if strings.TrimLeft(indent, " \t") != "" {
		indent = "  "
	}
	itemIndent := indent + "  "

	body := source[arrOpen+1 : arrClose]
	present := existingKeys(body)

	result := insertResult{text: source}
	var lines []string
	for _, entry := range entries {
		key := entryKey(entry)
		if present[key] {
			result.skipped = append(result.skipped, entry)
			continue
		}
		present[key] = true
		result.added = append(result.added, entry)
		lines = append(lines, itemIndent+renderEntry(entry)+",")
	}
	if len(result.added) == 0 {
		return result, true
	}

	// head is everything up to and including the last real value, plus the comma that has to
	// separate it from what is being appended. An array with no value in it — a fresh config, or
	// one whose every entry is commented out — has nothing to separate from, and its whole body
	// is tail.
	//
	// That distinction is the bug this shape exists to avoid. The scaffolded config ships five
	// commented-out example entries inside `"repos": [ … ]` (internal/scaffold/files.go), so
	// `frznforge new` followed by `frznforge init --web` hits the value-less case on the very
	// first save. Treating a value-less array as an empty one replaced them with the new
	// entries, deleting the only documentation a new site has for the block it is about to grow.
	//
	// The identical shape lives in cmd/frznforge/entries.go, which had this fix while this copy
	// did not — the two splice engines are forked, and this is what forking them cost.
	head, tail := "", body
	if last := lastMeaningfulIndex(body); last != -1 {
		head = body[:last+1]
		if body[last] != ',' {
			head += ","
		}
		tail = body[last+1:]
	}
	// tail is whatever followed: a same-line comment, then the line break before the `]`. keep
	// holds the comment so it stays where its author put it; closeGap reuses the file's own line
	// break rather than inventing one, and only manufactures a break for a one-line array.
	trailing := tail[len(strings.TrimRight(tail, " \t\r\n")):]
	keep := tail[:len(tail)-len(trailing)]
	closeGap := trailing
	if !strings.Contains(trailing, "\n") {
		closeGap = "\n" + indent
	}
	newBody := head + keep + "\n" + strings.Join(lines, "\n") + closeGap
	result.text = source[:arrOpen+1] + newBody + source[arrClose:]
	result.changed = true
	return result, true
}
