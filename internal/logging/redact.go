package logging

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Redaction lives HERE, at the sink, and nowhere else.
//
// The alternative — every call site remembering to wrap its own strings the way ingest's
// RedactToken is wrapped around error text — is the design that eventually leaks, because it
// only takes one slog.Debug written in a hurry to put a live token into a file the user then
// pastes into a bug report. A scrubber that records cannot go around is the only version of
// this that stays true as the code grows, so the handler does it and the callers do not have
// to know.
//
// Three things are scrubbed, because a secret reaches a record by three different routes:
//
//  1. The attribute KEY looks like a secret ("token", "authorization", "apiKey"): the value is
//     replaced wholesale, whatever it is, since nothing under such a key is worth printing.
//  2. A string anywhere in the record — message, attribute value, error text — contains the
//     literal value of a secret this process knows about: every environment variable whose NAME
//     looks like a secret, plus anything a caller passed to Redact.
//  3. A string has the SHAPE of a credential — userinfo in a URL, a token query parameter, an
//     Authorization header — even when the value itself is unknown here, because a token can
//     arrive from a config file or a redirect rather than from this process's environment.
//
// False positives are the cheap direction. Losing a diagnostic value costs a debugging session;
// leaking a token costs the user their account.

// minSecretLen is the shortest environment value treated as a secret literal.
//
// Without a floor, a variable like GITHUB_TOKEN=1 (or an empty-but-set one) would turn every
// "1" in the log into "***" and make the file useless. No real credential is this short.
const minSecretLen = 8

// Redacted is what replaces a secret. It matches ingest.RedactToken so a line that passed
// through both does not read as two different kinds of redaction.
const Redacted = "***"

var (
	// secretsMu guards writes; live is the read path, swapped whole so Handle never locks.
	secretsMu sync.Mutex
	secretSet = map[string]bool{}
	live      atomic.Pointer[[]string]

	envOnce sync.Once
)

// Redact registers a literal that must never appear in a log record again.
//
// It exists for the secrets the environment scan cannot find: a source may name its own token
// variable (`"tokenEnv": "MY_PAT"`), and "MY_PAT" does not look like a secret from the outside.
// Whoever resolves such a token calls this once and every later record is covered, including
// records written before the call is made — nothing is buffered, so "later" is all that matters.
//
// Safe to call from any goroutine, and safe to call before any sink exists.
func Redact(secret string) {
	addSecrets(secret)
}

func addSecrets(values ...string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	changed := false
	for _, v := range values {
		v = strings.TrimSpace(v)
		if len(v) < minSecretLen || secretSet[v] {
			continue
		}
		secretSet[v] = true
		changed = true
	}
	if !changed {
		return
	}
	list := make([]string, 0, len(secretSet))
	for v := range secretSet {
		list = append(list, v)
	}
	// Longest first: one secret can be a prefix of another (a token and the same token inside a
	// URL-encoded copy of itself), and replacing the short one first would leave the tail of the
	// long one in the file. Ties break lexicographically so the order does not depend on the map.
	sort.Slice(list, func(i, j int) bool {
		if len(list[i]) != len(list[j]) {
			return len(list[i]) > len(list[j])
		}
		return list[i] < list[j]
	})
	live.Store(&list)
}

// loadEnvSecrets harvests the process environment once per process.
//
// It reads names, never values, to decide what is a secret — and the values it keeps never
// leave this package: they are only ever used as the left-hand side of a replacement. Nothing
// here writes an environment variable anywhere.
func loadEnvSecrets() {
	envOnce.Do(func() {
		var vals []string
		for _, kv := range os.Environ() {
			name, value, ok := strings.Cut(kv, "=")
			// harvestEnvName, not SecretKey: this decides what gets substituted out of every
			// record, and a false positive here corrupts text rather than merely hiding it.
			if ok && harvestEnvName(name) && harvestable(value) {
				vals = append(vals, value)
			}
		}
		addSecrets(vals...)
	})
}

// secretWords are the key words that mean "never print this value". Matched against the words
// of an attribute key, so "githubToken", "github_token" and "GITHUB_TOKEN" all hit while
// "keyword" does not.
var secretWords = map[string]bool{
	"token": true, "tokens": true,
	"auth": true, "authorization": true, "authorisation": true, "bearer": true,
	"password": true, "passwd": true, "pwd": true, "passphrase": true,
	"secret": true, "secrets": true,
	"credential": true, "credentials": true, "creds": true,
	"cookie": true, "cookies": true,
	"key": true, "keys": true,
	"pat": true, "session": true, "sessionid": true,
	"signature": true, "sig": true,
	// The spellings that survive the splitter as one word. There is no general "ends in key"
	// rule on purpose: it takes "monkey" and "keyword" with it, and a rule with obvious false
	// positives is a rule people work around.
	"apikey": true, "privatekey": true, "publickey": true, "secretkey": true,
	"accesskey": true, "deploykey": true, "hostkey": true, "sshkey": true, "signingkey": true,
}

// secretSubstrings are unambiguous enough to match anywhere in a key. "key" is deliberately not
// in this list — it would take "keyword" and "monkey" with it.
var secretSubstrings = []string{"token", "password", "passwd", "secret", "cookie", "credential", "authorization"}

// harvestWords is the NARROWER list: which environment variable NAMES make their VALUE a literal
// that is substituted out of every record, everywhere.
//
// It is deliberately not secretWords. The two answer different questions, and getting them
// confused is expensive in one direction only:
//
//   - Masking the value of an attribute named `session` is free. The attribute is one field, and
//     a `***` in it costs a reader nothing they needed.
//   - Substituting a value everywhere reaches strings that have nothing to do with credentials.
//     A session id is an ordinary identifier that turns up in paths, cache keys and temp
//     directories, and replacing it corrupts all of them. That happened: every path in the run
//     log pointed at a directory that did not exist, because one identifier had been declared a
//     secret and then removed from text that legitimately contained it.
//
// So this list holds only names that cannot plausibly mean anything but a credential. `session`,
// `sig`, and a bare `key` are absent on purpose — the compound spellings below cover the real
// credentials (`SSH_KEY`, `API_KEY`) without taking `KEYBOARD_LAYOUT` with them.
var harvestWords = map[string]bool{
	"token": true, "tokens": true,
	"password": true, "passwd": true, "passphrase": true,
	"secret": true, "secrets": true,
	"credential": true, "credentials": true, "creds": true,
	"authorization": true, "authorisation": true, "bearer": true,
	"pat": true, "cookie": true, "cookies": true,
	"apikey": true, "privatekey": true, "secretkey": true, "accesskey": true,
	"deploykey": true, "sshkey": true, "signingkey": true,
}

// harvestEnvName reports whether an environment variable's value should become a global literal.
func harvestEnvName(name string) bool {
	lower := strings.ToLower(name)
	for _, sub := range []string{"token", "password", "passwd", "secret", "credential", "authorization"} {
		if strings.Contains(lower, sub) {
			return true
		}
	}
	words := keyWords(name)
	for _, w := range words {
		if harvestWords[w] {
			return true
		}
	}
	// Adjacent pairs, so SSH_KEY and API_KEY reach the same answer as sshKey and apiKey. A bare
	// "key" is deliberately not enough on its own — that is what would take KEYBOARD_LAYOUT with
	// it, and a name that vague is exactly the kind whose value shows up in ordinary text.
	for i := 0; i+1 < len(words); i++ {
		if harvestWords[words[i]+words[i+1]] {
			return true
		}
	}
	return false
}

// harvestable rejects values that cannot be credentials but can easily be something a log needs
// to print correctly.
//
// A path, or anything with whitespace in it, is the shape that does the damage: substituting it
// rewrites unrelated lines. Real tokens are one opaque run of characters, so this costs nothing
// and removes a whole class of corruption.
func harvestable(v string) bool {
	if len(v) < minSecretLen {
		return false
	}
	if strings.ContainsAny(v, "/\\ \t\r\n") {
		return false
	}
	return true
}

// SecretKey reports whether an attribute key names something that must not be printed.
//
// Exported because internal/timings applies the same rule to the count keys it writes, and two
// implementations of "is this a secret" would eventually disagree.
func SecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range secretSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	for _, w := range keyWords(key) {
		if secretWords[w] {
			return true
		}
	}
	return false
}

// keyWords splits an attribute key into lowercase words on non-alphanumerics and on lower→upper
// transitions, so both snake_case and camelCase resolve to the same words.
func keyWords(key string) []string {
	var (
		words []string
		cur   strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	prevLower := false
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z':
			cur.WriteRune(r)
			prevLower = true
		case r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			if prevLower {
				flush()
			}
			cur.WriteRune(r)
			prevLower = false
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return words
}

// The shape-based patterns. Each one covers a way a credential reaches a string without this
// process ever having seen the credential itself.
var (
	// https://x-access-token:ghs_xxx@github.com/owner/repo.git — how every authenticated clone
	// URL and half the API URLs in ingest are built.
	urlUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)[^/@\s"'<>]+@`)
	// ?access_token=…, &private_token=…, &api_key=… — the other half.
	urlParamRe = regexp.MustCompile(`(?i)([?&](?:access_token|private_token|refresh_token|token|api_key|apikey|key|password|secret|auth|signature|sig)=)[^&\s"'<>]+`)
	// Authorization: Bearer …, and the "token …" spelling GitHub still accepts.
	authHeaderRe = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic|token)\s+)[^\s"'<>]+`)
)

// Scrub returns s with every known secret and every credential-shaped substring replaced.
//
// Exported because internal/timings writes its own file and must not be the hole in the wall.
// It is safe for concurrent use and allocates nothing when there is nothing to redact.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	if lits := live.Load(); lits != nil {
		for _, secret := range *lits {
			if strings.Contains(s, secret) {
				s = strings.ReplaceAll(s, secret, Redacted)
			}
		}
	}
	if strings.ContainsAny(s, ":=") {
		// The Contains guards keep the common record — a path, a slug, a count — off the regex
		// engine entirely. Every pattern below needs one of these characters to match.
		if strings.Contains(s, "://") {
			s = urlUserinfoRe.ReplaceAllString(s, "${1}"+Redacted+"@")
		}
		if strings.ContainsAny(s, "?&") {
			s = urlParamRe.ReplaceAllString(s, "${1}"+Redacted)
		}
		s = authHeaderRe.ReplaceAllString(s, "${1}"+Redacted)
	}
	return s
}
