package wizard

// Everything the wizard does to a path or a file that is not a splice: finding the config,
// loading it, the one-backup-per-file-per-session rule, and the profile file's frontmatter.
//
// The standing rule this file exists to enforce: the browser never names a file. Every path
// written is derived here from the config the terminal resolved.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frznforge/internal/config"
	"frznforge/internal/markdown"
)

// findConfigFile returns the nearest frznforge.config.jsonc at or above dir, or "".
func findConfigFile(from string) string {
	dir := from
	for {
		candidate := filepath.Join(dir, config.Filename)
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

/* ------------------------------------------------------------------ loading */

// loadedConfig is one read of the config file: the raw decoded input the operations are applied
// to, the validated config the page displays, and why it did not load when it did not.
type loadedConfig struct {
	// input is the file decoded as plain JSON, or nil when it did not decode. Operations are
	// applied to a copy of this to judge a change before the file is touched.
	input map[string]any
	// parsed is nil when the file is missing, malformed or fails validation.
	parsed *config.Config
	issues []string
}

// loadConfig reads and parses the config file. Every caller gets a fresh read.
//
// The TypeScript memoised this behind a generation counter because loading meant spawning tsx;
// re-reading a file costs nothing, and a memo is exactly how the wizard used to answer with the
// pre-edit config after a save.
func (s *Session) loadConfig() loadedConfig {
	if s.ConfigPath == "" {
		return loadedConfig{issues: []string{"no config file was found"}}
	}
	raw, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return loadedConfig{issues: []string{fmt.Sprintf("could not read %s: %s", s.ConfigPath, err)}}
	}
	loaded := loadedConfig{}
	if err := json.Unmarshal(config.StripJSONC(config.TrimBOM(raw)), &loaded.input); err != nil {
		loaded.input = nil
	}
	parsed, err := config.ParseBytes(raw)
	if err != nil {
		loaded.issues = configIssues(err)
		return loaded
	}
	loaded.parsed = parsed
	return loaded
}

// configIssues splits a config error into the list the page joins with "; ".
//
// config.Validate reports every problem at once as a bulleted block, and flattening that into
// one line would hand the user a wall of text with "  - " in the middle of sentences.
func configIssues(err error) []string {
	var issues []string
	for _, line := range strings.Split(err.Error(), "\n") {
		if problem, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok && problem != "" {
			issues = append(issues, problem)
		}
	}
	if len(issues) == 0 {
		// Not the bulleted shape — a decode failure, say. The message is the issue, and the
		// "config is not valid:" header is only dropped above because its bullets replace it.
		return []string{err.Error()}
	}
	return issues
}

/* ------------------------------------------------------------------ backups */

// backupPathFor names the backup for a file: `<file>.20240115T103000Z.bak`.
//
// Seconds resolution, UTC, no punctuation — the same name the TypeScript wizard wrote, so a
// project that has both versions' backups in it sorts them together.
func backupPathFor(file string, now time.Time) string {
	return file + "." + now.UTC().Format("20060102T150405Z") + ".bak"
}

// writeBackup copies source beside file, never over an existing backup.
//
// backupPathFor has one-second resolution, so two writes in the same second would otherwise name
// the same file and the second would overwrite the first — losing the only copy of the state
// before the wizard ran. O_EXCL makes the collision visible and a counter steps around it.
func writeBackup(file, source string, now time.Time) (string, error) {
	base := backupPathFor(file, now)
	for n := 0; n <= 100; n++ {
		candidate := base
		if n > 0 {
			candidate = base + "." + strconv.Itoa(n)
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
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
		return candidate, closeErr
	}
	return "", fmt.Errorf("could not find a free backup name beside %s — there are already a hundred of them", file)
}

// backupOnce takes this session's single backup of file, or returns the one already taken.
//
// One .bak per file per session: however many saves a session makes, "the state before the
// wizard touched anything" stays one file away instead of becoming a breadcrumb trail. Callers
// hold s.mu.
func (s *Session) backupOnce(file, source string) (string, error) {
	if existing, ok := s.backups[file]; ok {
		return existing, nil
	}
	backup, err := writeBackup(file, source, s.now())
	if err != nil {
		return "", err
	}
	s.backups[file] = backup
	return backup, nil
}

// backupBinaryOnce is backupOnce for a file that is not text.
//
// backupOnce writes the string it was handed — fine for config and markdown, silently corrupting
// for a PNG. Uploads copy the bytes instead, and share the same map so "one backup per file per
// session" still holds across both. A copy that fails records "no backup" rather than refusing
// the upload: the image on disk is replaceable, and the alternative is a wizard that cannot
// change an avatar because the folder is read-only.
func (s *Session) backupBinaryOnce(file string) string {
	if existing, ok := s.backups[file]; ok {
		return existing
	}
	backup := ""
	if bytes, err := os.ReadFile(file); err == nil {
		if written, err := writeBinaryBackup(file, bytes, s.now()); err == nil {
			backup = written
		}
	}
	s.backups[file] = backup
	return backup
}

// writeBinaryBackup is writeBackup for bytes.
func writeBinaryBackup(file string, source []byte, now time.Time) (string, error) {
	base := backupPathFor(file, now)
	for n := 0; n <= 100; n++ {
		candidate := base
		if n > 0 {
			candidate = base + "." + strconv.Itoa(n)
		}
		f, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, writeErr := f.Write(source)
		closeErr := f.Close()
		if writeErr != nil {
			return "", writeErr
		}
		return candidate, closeErr
	}
	return "", fmt.Errorf("could not find a free backup name beside %s", file)
}

// markCreated records that this session created a file, so a later save does not back up the
// wizard's own first output as if it were the pre-wizard state. A created file has none.
// Callers hold s.mu.
func (s *Session) markCreated(file string) {
	if _, ok := s.backups[file]; !ok {
		s.backups[file] = ""
	}
}

/* ------------------------------------------------------------------ the profile file */

// profilePath resolves the markdown file the profile editor writes — always here, never from
// anything the browser said.
//
// Contained, because owner.profile is a browser-settable config field. The resolved path must
// stay inside the project (no ".." escape, no absolute path elsewhere), must be a markdown file
// (the profile body is markdown; writing it to a .jsonc file is never legitimate and would
// otherwise let a crafted owner.profile overwrite the config the wizard then reads), and must
// not be the config file. Any of those fails and the profile endpoints decline rather than
// write. A config that does not parse yields "" too: without a validated config there is no
// trustworthy profile path.
func (s *Session) profilePath(parsed *config.Config) string {
	if s.ConfigPath == "" || parsed == nil {
		return ""
	}
	root := filepath.Dir(s.ConfigPath)
	file := filepath.FromSlash(parsed.Owner.Profile)
	if filepath.IsAbs(file) {
		file = filepath.Clean(file)
	} else {
		file = filepath.Join(root, file)
	}
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if file == s.ConfigPath {
		return ""
	}
	if !markdown.IsMarkdownPath(filepath.ToSlash(file)) {
		return ""
	}
	return file
}

// frontmatterOpen matches the `---` that opens a frontmatter block, BOM and all.
var frontmatterOpen = regexp.MustCompile("^\uFEFF?---[ \t]*\r?\n")

// frontmatterTerminator matches the line that closes one: YAML ends a document with `---` (a new
// one starts) or `...`.
var frontmatterTerminator = regexp.MustCompile(`^(---|\.\.\.)[ \t]*$`)

// frontmatterEndOffset is the byte offset where a markdown file's body starts: after the leading
// frontmatter block, or 0 when there is none — an unterminated block included, which is the
// stance internal/frontmatter takes too.
//
// Offsets rather than internal/frontmatter.SplitFile on purpose: that helper normalises line
// endings for parsing, while the profile editor must round-trip the frontmatter block byte for
// byte. The wizard edits the body and has no business reformatting metadata it does not read.
func frontmatterEndOffset(text string) int {
	open := frontmatterOpen.FindString(text)
	if open == "" {
		return 0
	}
	for i := len(open); ; {
		nl := strings.IndexByte(text[i:], '\n')
		line := text[i:]
		if nl >= 0 {
			line = text[i : i+nl]
		}
		if frontmatterTerminator.MatchString(strings.TrimSuffix(line, "\r")) {
			if nl == -1 {
				return len(text)
			}
			return i + nl + 1
		}
		if nl == -1 {
			return 0
		}
		i += nl + 1
	}
}

// joinProfile reattaches an edited body to a preserved frontmatter head.
//
// When the head ends at a bare terminator (`---` as the file's last line, no trailing newline),
// gluing the body straight on would fuse them into `---body`, and on the next read that line no
// longer closes the block — the whole frontmatter would be swallowed into the body. One
// separating newline keeps the terminator on its own line; the frontmatter bytes themselves are
// untouched.
func joinProfile(head, body string) string {
	if head == "" {
		return body
	}
	if strings.HasSuffix(head, "\n") {
		return head + body
	}
	return head + "\n" + body
}
