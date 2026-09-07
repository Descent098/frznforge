package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// resetSinks puts the package back to "nothing configured" so one test cannot leak a handler
// into the next. The secret list is deliberately NOT reset: secrets are per-process by design,
// and every test below uses a value distinctive enough that sharing the list is harmless.
func resetSinks(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = Close()
		Setup("", nil)
	})
}

func TestSecretKey(t *testing.T) {
	secret := []string{
		"token", "Token", "TOKEN", "githubToken", "github_token", "GITHUB_TOKEN",
		"auth", "Authorization", "authorization", "bearer",
		"password", "passwd", "pwd", "passphrase",
		"secret", "client_secret", "cookie", "Set-Cookie",
		"key", "apiKey", "api_key", "x-api-key", "privateKey", "credentials",
	}
	for _, k := range secret {
		if !SecretKey(k) {
			t.Errorf("SecretKey(%q) = false, want true", k)
		}
	}
	// The false positives that would actually hurt: this is a git tool, and "author" and
	// "keyword" are ordinary words here.
	safe := []string{"url", "repo", "slug", "path", "ms", "author", "authored", "keyword", "monkey", "ref", "count", "bytes"}
	for _, k := range safe {
		if SecretKey(k) {
			t.Errorf("SecretKey(%q) = true, want false", k)
		}
	}
}

func TestScrubLiteralsAndShapes(t *testing.T) {
	Redact("ghp_scrubliteral1234567890")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"literal in a path", "fetched https://host/x/ghp_scrubliteral1234567890/y",
			"fetched https://host/x/***/y"},
		{"url userinfo", "clone https://x-access-token:ghs_unknown9999@github.com/o/r.git",
			"clone https://***@github.com/o/r.git"},
		{"query parameter", "GET https://gitlab.com/api/v4/projects?private_token=unknown-value&per_page=100",
			"GET https://gitlab.com/api/v4/projects?private_token=***&per_page=100"},
		{"authorization header", "sending Authorization: Bearer abcdef.ghijkl to the API",
			"sending Authorization: Bearer *** to the API"},
		{"nothing to do", "rendered 812 pages for kieran/frznforge", "rendered 812 pages for kieran/frznforge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Scrub(tc.in); got != tc.want {
				t.Errorf("Scrub(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// A secret shorter than minSecretLen must be ignored: registering "1" would turn every digit in
// the log into ***.
func TestScrubIgnoresShortSecrets(t *testing.T) {
	Redact("abc")
	if got := Scrub("abc def"); got != "abc def" {
		t.Fatalf("short secret was redacted: %q", got)
	}
}

// The three routes a token takes into a record: the message, an attribute value, and a URL.
func TestHandlerRedactsMessageAttrAndURL(t *testing.T) {
	resetSinks(t)
	const token = "glpat-recordredaction01"
	Redact(token)

	var buf bytes.Buffer
	Setup("debug", &buf)

	slog.Debug("cloning with "+token,
		"url", "https://gitlab.com/api/v4/projects/1?private_token="+token,
		"token", token,
		"remote", "https://oauth2:"+token+"@gitlab.com/o/r.git",
		"slug", "kieran/frznforge")

	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("token survived into the record:\n%s", out)
	}
	for _, want := range []string{"msg=", "***", "slug=kieran/frznforge"} {
		if !strings.Contains(out, want) {
			t.Errorf("record is missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "token=***") {
		t.Errorf("secret-looking key was not replaced:\n%s", out)
	}
}

// slog.With binds an attribute once; a handler that only scrubbed in Handle would never see it.
func TestHandlerRedactsBoundAttrs(t *testing.T) {
	resetSinks(t)
	const token = "ghs_boundattrredaction1"
	Redact(token)

	var buf bytes.Buffer
	Setup("debug", &buf)

	slog.Default().With("token", token, "url", "https://host/"+token).Info("bound")
	if out := buf.String(); strings.Contains(out, token) {
		t.Fatalf("token survived a With-bound attribute:\n%s", out)
	}
}

// A group named after a secret must take its children with it, and an error value has to be
// scrubbed even though it is not a string.
func TestHandlerRedactsGroupsAndErrors(t *testing.T) {
	resetSinks(t)
	const token = "ghp_grouperrorredaction"
	Redact(token)

	var buf bytes.Buffer
	Setup("debug", &buf)

	slog.Error("failed",
		"err", &stubErr{"GET https://host/" + token + " returned 401"},
		slog.Group("auth", "header", "Bearer "+token))

	if out := buf.String(); strings.Contains(out, token) {
		t.Fatalf("token survived a group or an error value:\n%s", out)
	}
}

type stubErr struct{ msg string }

func (e *stubErr) Error() string { return e.msg }

// WithGroup("auth").With("header", tok) hides a credential behind an innocent attribute key:
// only the enclosing group says what it is.
func TestHandlerRedactsInsideASecretGroup(t *testing.T) {
	resetSinks(t)
	const token = "ghp_withGroupRedaction01"
	Redact(token)

	var buf bytes.Buffer
	Setup("debug", &buf)

	slog.Default().WithGroup("auth").With("header", token).Info("request")
	out := buf.String()
	if strings.Contains(out, token) {
		t.Fatalf("token survived inside a group named like a secret:\n%s", out)
	}
	// An ordinary group must still print its values.
	buf.Reset()
	slog.Default().WithGroup("repo").With("slug", "kieran/frznforge").Info("request")
	if !strings.Contains(buf.String(), "kieran/frznforge") {
		t.Fatalf("an ordinary group was redacted:\n%s", buf.String())
	}
}

// A value of an unknown type — a slice of remote URLs, say — is still a place a token can hide.
func TestHandlerRedactsUnknownValueTypes(t *testing.T) {
	resetSinks(t)
	const token = "ghp_unknownValueType001"
	Redact(token)

	var buf bytes.Buffer
	Setup("debug", &buf)

	slog.Info("remotes", "remotes", []string{"https://host/a", "https://host/" + token})
	if out := buf.String(); strings.Contains(out, token) {
		t.Fatalf("token survived inside a slice value:\n%s", out)
	}
}

// Redact and Scrub are called from every goroutine in a parallel build.
func TestRedactIsConcurrencySafe(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			Redact("concurrent-secret-value-" + string(rune('a'+i)))
			for j := 0; j < 100; j++ {
				_ = Scrub("https://host/path?token=abc&x=1")
			}
		}(i)
	}
	wg.Wait()
}
