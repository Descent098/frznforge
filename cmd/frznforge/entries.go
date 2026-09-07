package main

// The entries `init` writes into `repos: [ … ]`, and the splice that puts them there. Ported
// from the entry half of scripts/cli.ts, retargeted from TypeScript source to JSONC.
//
// KNOWN DUPLICATION, same as listing.go: internal/wizard/entries.go and edit.go do this for
// `init --web`, unexported. The walkers here are the subset one splice needs — the wizard's are
// a general field editor — but the dialect rules and the output bytes must match, because both
// front ends write into the same file and a user may run either.
//
// Two rules the whole file lives by:
//
//   - **The config is edited textually.** Re-serialising it would throw away the comments that
//     are most of the file and all of its documentation. Entries are spliced into the existing
//     array and every byte outside it comes through unchanged.
//   - **A listing is hostile input.** A repository name arrives over the network and ends up in
//     a file the user reads for years, so it is validated at the one place entries are built.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"frznforge/internal/config"
)

// repoEntry is one configured source, as the picker writes it.
type repoEntry struct {
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
	get func(repoEntry) string
}{
	{"type", func(e repoEntry) string { return e.Type }},
	{"host", func(e repoEntry) string { return e.Host }},
	{"owner", func(e repoEntry) string { return e.Owner }},
	{"repo", func(e repoEntry) string { return e.Repo }},
	{"project", func(e repoEntry) string { return e.Project }},
	{"slug", func(e repoEntry) string { return e.Slug }},
	{"releases", func(e repoEntry) string { return e.Releases }},
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
		return "", errors.New("host must be http or https")
	}
	if u.User != nil {
		return "", errors.New("host must not carry credentials")
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

// jsonQuote renders a Go string as a JSON string literal.
//
// encoding/json is not used because it escapes <, > and & — a config full of URLs would come
// out unreadable, and this file is meant to be edited by hand afterwards. Same implementation
// as internal/wizard/edit.go's, so an entry written by the terminal and one written by the
// wizard are the same bytes.
func jsonQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r == utf8.RuneError && size == 1:
			b.WriteString(`�`)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// renderEntry is one config array element, on one line.
func renderEntry(entry repoEntry) string {
	var parts []string
	for _, f := range entryKeyOrder {
		if v := f.get(entry); v != "" {
			parts = append(parts, jsonQuote(f.key)+": "+jsonQuote(v))
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// renderSnippet is the `"repos": [ … ]` fragment a user can paste by hand.
func renderSnippet(entries []repoEntry) string {
	lines := make([]string, len(entries))
	for i, e := range entries {
		lines[i] = "    " + renderEntry(e) + ","
	}
	return "  \"repos\": [\n" + strings.Join(lines, "\n") + "\n  ],"
}

// entryKey is the identity of a source, ignoring cosmetic fields: two entries with the same key
// describe the same remote repository and must never be added twice.
func entryKey(entry repoEntry) string {
	host := entry.Host
	if host == "" {
		host = providers[entry.Type].defaultHost
	}
	host = strings.ToLower(strings.TrimRight(host, "/"))
	id := entry.Project
	if id == "" {
		id = entry.Owner + "/" + entry.Repo
	}
	return entry.Type + "|" + host + "|" + strings.ToLower(id)
}

// entryFor builds a config entry for one listed repository.
func entryFor(provider, host string, repo remoteRepo, releases, slug string) (repoEntry, error) {
	entry := repoEntry{Type: provider, Releases: releases}
	normalised := strings.TrimRight(host, "/")
	if normalised != "" && normalised != providers[provider].defaultHost {
		safe, err := assertSafeHost(normalised)
		if err != nil {
			return repoEntry{}, err
		}
		entry.Host = safe
	}
	var err error
	if provider == "gitlab" {
		project := repo.Project
		if project == "" {
			project = repo.FullName
		}
		if entry.Project, err = assertSafeField(project, "project path"); err != nil {
			return repoEntry{}, err
		}
	} else {
		if entry.Owner, err = assertSafeField(repo.Owner, "owner"); err != nil {
			return repoEntry{}, err
		}
		if entry.Repo, err = assertSafeField(repo.Name, "repository name"); err != nil {
			return repoEntry{}, err
		}
	}
	if slug != "" {
		if entry.Slug, err = assertSafeField(slug, "slug"); err != nil {
			return repoEntry{}, err
		}
	}
	return entry, nil
}

// entriesFor builds the entries for a whole selection, adding an explicit slug only where two
// picked repositories would otherwise collide (the default slug is the repository name).
func entriesFor(provider, host string, repos []remoteRepo, releases string) ([]repoEntry, error) {
	counts := map[string]int{}
	for _, r := range repos {
		counts[slugify(r.Name)]++
	}
	out := make([]repoEntry, 0, len(repos))
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

/* ---- the JSONC dialect ---------------------------------------------------- */

// The walkers below are comment- and string-aware, never a regex over the whole file: a
// `"repos": [` inside a commented-out block must not be what an edit lands in, and configs ship
// with exactly that.

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f'
}

// skipString returns the index just past the string literal starting at src[i] == '"'. An
// unterminated literal runs to end, which keeps a malformed file from looping forever.
func skipString(src string, i, end int) int {
	for j := i + 1; j < end; j++ {
		switch src[j] {
		case '\\':
			j++
		case '"':
			return j + 1
		}
	}
	return end
}

// skipComment returns the index just past a comment starting at i, or i when none starts there.
func skipComment(src string, i, end int) int {
	if i+1 >= end || src[i] != '/' {
		return i
	}
	switch src[i+1] {
	case '/':
		if nl := strings.IndexByte(src[i:end], '\n'); nl >= 0 {
			return i + nl + 1
		}
		return end
	case '*':
		if c := strings.Index(src[i+2:end], "*/"); c >= 0 {
			return i + 2 + c + 2
		}
		return end
	}
	return i
}

// skipTrivia returns the index of the next byte that is neither whitespace nor part of a comment.
func skipTrivia(src string, i, end int) int {
	for i < end {
		if isSpaceByte(src[i]) {
			i++
			continue
		}
		if next := skipComment(src, i, end); next != i {
			i = next
			continue
		}
		return i
	}
	return end
}

// matchBracket walks from an opening bracket to its match, skipping strings and comments, and
// returns -1 when the file does not close it.
func matchBracket(src string, open int) int {
	var closer byte
	switch src[open] {
	case '[':
		closer = ']'
	case '{':
		closer = '}'
	default:
		return -1
	}
	depth := 0
	for i := open; i < len(src); {
		c := src[i]
		if c == '/' {
			if next := skipComment(src, i, len(src)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(src, i, len(src))
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				if c == closer {
					return i
				}
				return -1
			}
		}
		i++
	}
	return -1
}

// lastMeaningfulIndex is the index of the last byte of body that is neither whitespace nor part
// of a comment. It is what makes an append land after the last real value rather than after a
// trailing comment — which would put the new entry inside the comment.
func lastMeaningfulIndex(body string) int {
	last := -1
	for i := 0; i < len(body); {
		c := body[i]
		if c == '/' {
			if next := skipComment(body, i, len(body)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			next := skipString(body, i, len(body))
			last = next - 1
			i = next
			continue
		}
		if !isSpaceByte(c) {
			last = i
		}
		i++
	}
	return last
}

// topLevelItems splits an array body into its top-level elements, skipping strings and comments.
func topLevelItems(body string) []string {
	var items []string
	depth := 0
	start := 0
	push := func(end int) {
		if text := body[start:end]; strings.TrimSpace(text) != "" {
			items = append(items, text)
		}
	}
	for i := 0; i < len(body); {
		c := body[i]
		if c == '/' {
			if next := skipComment(body, i, len(body)); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			i = skipString(body, i, len(body))
			continue
		}
		switch {
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			push(i)
			start = i + 1
		}
		i++
	}
	push(len(body))
	return items
}

// stripComments removes comments from one element's text, leaving string literals intact.
//
// topLevelItems skips comments when it looks for separators, but the slice it hands back still
// contains them — and a commented-out entry sitting above a real one would otherwise be read as
// if it were the entry.
func stripComments(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		c := text[i]
		if c == '/' && i+1 < len(text) {
			switch text[i+1] {
			case '/':
				next := skipComment(text, i, len(text))
				// The line comment ate its own newline; put one back so the following line does
				// not fuse onto this one.
				b.WriteByte('\n')
				i = next
				continue
			case '*':
				next := skipComment(text, i, len(text))
				b.WriteByte(' ')
				i = next
				continue
			}
		}
		if c == '"' {
			next := skipString(text, i, len(text))
			b.WriteString(text[i:next])
			i = next
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// findRootObject returns the brace span of the file's single top-level object.
func findRootObject(source string) (open, close int, ok bool) {
	i := skipTrivia(source, 0, len(source))
	if i >= len(source) || source[i] != '{' {
		return 0, 0, false
	}
	close = matchBracket(source, i)
	if close == -1 {
		return 0, 0, false
	}
	return i, close, true
}

// findKey locates `"key":` at the TOP level of the object body source[open+1:close] and returns
// the index of the key's opening quote and of the value's first byte.
//
// Comment- and string-aware, so a key of the same name inside a nested object, inside a string
// or inside a commented-away block never matches.
func findKey(source string, open, close int, key string) (keyStart, valueStart int, ok bool) {
	depth := 0
	expectKey := true // at the body's start, and after every top-level comma
	for i := open + 1; i < close; {
		c := source[i]
		if c == '/' {
			if next := skipComment(source, i, close); next != i {
				i = next
				continue
			}
		}
		if c == '"' {
			if depth != 0 || !expectKey {
				i = skipString(source, i, close)
				continue
			}
			afterKey := skipString(source, i, close)
			var name string
			decoded := json.Unmarshal([]byte(source[i:afterKey]), &name) == nil
			expectKey = false
			colon := skipTrivia(source, afterKey, close)
			if !decoded || colon >= close || source[colon] != ':' {
				i = afterKey
				continue
			}
			if name != key {
				i = colon + 1
				continue
			}
			return i, skipTrivia(source, colon+1, close), true
		}
		switch {
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			expectKey = true
		case depth == 0 && !isSpaceByte(c):
			// Not a quoted key: some other token in a malformed file. Stop expecting a key
			// until the next comma rather than trying to interpret it.
			expectKey = false
		}
		i++
	}
	return 0, 0, false
}

// objectField reads a top-level string field from one array element's text.
func objectField(item, key string) (string, bool) {
	open, close, ok := findRootObject(item)
	if !ok {
		return "", false
	}
	_, valueStart, found := findKey(item, open, close, key)
	if !found || item[valueStart] != '"' {
		return "", false
	}
	var out string
	if json.Unmarshal([]byte(item[valueStart:skipString(item, valueStart, close)]), &out) != nil {
		return "", false
	}
	return out, true
}

/* ---- the repos splice ----------------------------------------------------- */

// existingKeys collects the identity keys of the sources already in an array body.
func existingKeys(body string) map[string]bool {
	keys := map[string]bool{}
	for _, raw := range topLevelItems(body) {
		item := stripComments(raw)
		typ, ok := objectField(item, "type")
		if !ok || !knownProvider(typ) {
			continue
		}
		entry := repoEntry{Type: typ}
		entry.Host, _ = objectField(item, "host")
		entry.Owner, _ = objectField(item, "owner")
		entry.Repo, _ = objectField(item, "repo")
		entry.Project, _ = objectField(item, "project")
		keys[entryKey(entry)] = true
	}
	return keys
}

// insertResult reports what an insertRepos call did, or would do.
type insertResult struct {
	text string
	// added are the entries that were written; skipped were already in the file.
	added   []repoEntry
	skipped []repoEntry
	changed bool
}

// insertRepos splices entries into the `"repos": [ … ]` array of a config file's source text.
//
// ok is false when there is no repos array to splice into, which is the caller's cue to fall
// back to "paste this in yourself" rather than to guess where it should go.
//
// Idempotent: an entry whose identity already appears in the array is reported as skipped, so
// running init twice adds nothing the second time.
func insertRepos(source string, entries []repoEntry) (insertResult, bool) {
	open, close, ok := findRootObject(source)
	if !ok {
		return insertResult{}, false
	}
	keyStart, arrOpen, found := findKey(source, open, close, "repos")
	if !found || source[arrOpen] != '[' {
		return insertResult{}, false
	}
	arrClose := matchBracket(source, arrOpen)
	if arrClose == -1 {
		return insertResult{}, false
	}

	// Indent the new entries to match the `"repos":` line itself, whatever the file's style is.
	lineStart := strings.LastIndexByte(source[:keyStart], '\n') + 1
	indent := source[lineStart:keyStart]
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
	// That distinction is the bug this shape exists to avoid: the scaffolded config ships five
	// commented-out example entries inside `"repos": [ … ]`, and treating a value-less array as
	// an empty one would replace them with the new entries — deleting the only documentation a
	// new site has for the block it is about to grow.
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

/* ---- the file --------------------------------------------------------------*/

// findConfigFile returns the nearest frznforge.config.jsonc at or above from, or "".
func findConfigFile(from string) string {
	dir, err := filepath.Abs(from)
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, config.Filename)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// readConfigSource reads a config file, splitting off a byte-order mark.
//
// A BOM is not part of the JSONC grammar the walkers read, and findRootObject would refuse a
// file that starts with one. It is returned separately so the writer can put it back byte for
// byte: the editor that wrote it will keep expecting it, and this command's whole promise is
// that everything it did not edit is unchanged.
func readConfigSource(file string) (bom, source string, err error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}
	if config.HasBOM(raw) {
		return string(raw[:3]), string(config.TrimBOM(raw)), nil
	}
	return "", string(raw), nil
}

// backupPathFor is `frznforge.config.jsonc` → `frznforge.config.jsonc.20260823T195100Z.bak`.
func backupPathFor(file string, now time.Time) string {
	return file + "." + now.UTC().Format("20060102T150405Z") + ".bak"
}

// writeBackup copies source to a backup beside file, never over an existing one.
//
// backupPathFor has one-second resolution, so two writes in the same second would otherwise
// name the same file and the second would overwrite the first — losing the only copy of the
// pre-write state. O_EXCL makes the collision visible and a counter steps around it.
func writeBackup(file, source string, now time.Time) (string, error) {
	base := backupPathFor(file, now)
	for n := 0; n <= 100; n++ {
		candidate := base
		if n > 0 {
			candidate = fmt.Sprintf("%s.%d", base, n)
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := f.WriteString(source)
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		return candidate, nil
	}
	return "", fmt.Errorf("could not write a backup beside %s: %s and 100 numbered variants all exist already", file, base)
}

// writeResult is an insertResult that reached the disk.
type writeResult struct {
	insertResult
	// backup is the .bak that was taken first, or "" when nothing was written.
	backup string
}

// updateConfigFile applies insertRepos to a file on disk, taking a timestamped .bak copy first.
// Writing is skipped (and no backup is made) when nothing would change.
//
// ok is false when the file has no repos array — the same signal insertRepos gives.
func updateConfigFile(file string, entries []repoEntry, now time.Time) (writeResult, bool, error) {
	bom, source, err := readConfigSource(file)
	if err != nil {
		return writeResult{}, false, err
	}
	result, ok := insertRepos(source, entries)
	if !ok {
		return writeResult{}, false, nil
	}
	if !result.changed {
		return writeResult{insertResult: result}, true, nil
	}
	backup, err := writeBackup(file, bom+source, now)
	if err != nil {
		return writeResult{}, true, err
	}
	if err := os.WriteFile(file, []byte(bom+result.text), 0o644); err != nil {
		return writeResult{}, true, err
	}
	return writeResult{insertResult: result, backup: backup}, true, nil
}
