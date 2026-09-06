package markdown

import (
	"regexp"
	"strings"
	"testing"
)

// TestUntrustedMarkdownIsInert is an independent spot-check of the sanitiser, written without
// reference to the port's own tests. Untrusted markdown is what an IMPORTED repository's README
// and a provider's release notes are: content this site publishes but did not write.
func TestUntrustedMarkdownIsInert(t *testing.T) {
	payloads := []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`<a href="javascript:alert(1)">x</a>`,
		`[click](javascript:alert(1))`,
		`[click](JaVaScRiPt&#58;alert(1))`,
		`[click](&#106;avascript:alert(1))`,
		`[click](java&#09;script:alert(1))`,
		`[click](  javascript:alert(1))`,
		`![img](javascript:alert(1))`,
		`![img](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)`,
		`<iframe src="https://evil.example"></iframe>`,
		`<svg onload=alert(1)></svg>`,
		`<object data="javascript:alert(1)"></object>`,
		`<math><mtext><script>alert(1)</script></mtext></math>`,
		`<base href="https://evil.example">`,
		`<meta http-equiv="refresh" content="0;url=javascript:alert(1)">`,
		`<javascript:alert(1)>`,
		`<a href="vbscript:msgbox(1)">x</a>`,
		"[ref][1]\n\n[1]: javascript:alert(1)",
		`<div onclick="alert(1)">x</div>`,
	}

	// Two separate questions, because they need different checks:
	//   - no dangerous TAG may appear at all;
	//   - no ATTRIBUTE VALUE may carry an executable scheme.
	//
	// Scanning the whole document for "javascript:" instead would fail on the autolink
	// `<javascript:alert(1)>`, which renders as `<a href="#">javascript:alert(1)</a>` — the
	// scheme is inert visible text there, and a sanitiser is not required to censor prose.
	bannedTags := []string{"<script", "<iframe", "<object", "<embed", "<base", "<meta"}
	attrValue := regexp.MustCompile(`(?i)\s(?:href|src|data|action|formaction)\s*=\s*"([^"]*)"`)
	onAttr := regexp.MustCompile(`(?i)\son[a-z]+\s*=`)

	for _, p := range payloads {
		out := Render(p, Options{}) // zero value = untrusted, the fail-closed default
		low := strings.ToLower(out)
		for _, b := range bannedTags {
			if strings.Contains(low, b) {
				t.Errorf("payload %q emitted %s:\n%s", p, b, out)
			}
		}
		if onAttr.MatchString(out) {
			t.Errorf("payload %q emitted an event-handler attribute:\n%s", p, out)
		}
		for _, m := range attrValue.FindAllStringSubmatch(out, -1) {
			v := strings.ToLower(strings.TrimSpace(m[1]))
			for _, scheme := range []string{"javascript:", "vbscript:", "data:text/html"} {
				if strings.Contains(v, scheme) {
					t.Errorf("payload %q left %q in a URL attribute:\n%s", p, scheme, out)
				}
			}
		}
	}
}

// TestTrustedStillRendersRealReadmes — over-sanitising is its own failure. A repo you own is
// allowed its badge table and its centred header.
func TestTrustedStillRendersRealReadmes(t *testing.T) {
	src := "<p align=\"center\">\n  <img src=\"/logo.png\" alt=\"logo\">\n</p>\n\n# Title\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	out := Render(src, Options{Trusted: true})
	for _, want := range []string{`align="center"`, "<img", "<table", "<td"} {
		if !strings.Contains(out, want) {
			t.Errorf("trusted render dropped %q:\n%s", want, out)
		}
	}
}
