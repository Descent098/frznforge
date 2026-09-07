package serve

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// tsMimeTable is src/lib/mime.ts transcribed by hand, entry by entry.
//
// It is spelled out rather than derived from mimeByExt because a test that reads the table it
// is checking asserts nothing. The claim being made is that the Go table still says what the
// TypeScript one said, and the only other place these values get checked is a browser — which
// fails quietly. A missing charset on text/html renders the page in the browser's guess at an
// encoding; .mjs served as anything but JavaScript is a module that will not execute; .wasm
// without application/wasm loses streaming compilation. None of those show up in a build log.
var tsMimeTable = map[string]string{
	"png":   "image/png",
	"jpg":   "image/jpeg",
	"jpeg":  "image/jpeg",
	"gif":   "image/gif",
	"webp":  "image/webp",
	"svg":   "image/svg+xml",
	"ico":   "image/x-icon",
	"json":  "application/json",
	"pdf":   "application/pdf",
	"zip":   "application/zip",
	"wasm":  "application/wasm",
	"html":  "text/html; charset=utf-8",
	"htm":   "text/html; charset=utf-8",
	"css":   "text/css; charset=utf-8",
	"js":    "text/javascript; charset=utf-8",
	"mjs":   "text/javascript; charset=utf-8",
	"woff":  "font/woff",
	"woff2": "font/woff2",
	"ttf":   "font/ttf",
	"mp3":   "audio/mpeg",
	"mp4":   "video/mp4",
	"webm":  "video/webm",
}

func TestMimeTableMatchesTypeScript(t *testing.T) {
	// Sorted, not a bare map range: failure output has to read the same way twice, and the rule
	// against ranging an unsorted map is not suspended in tests.
	for _, ext := range slices.Sorted(maps.Keys(tsMimeTable)) {
		name := "file." + ext
		if got := MimeFor(name); got != tsMimeTable[ext] {
			t.Errorf("MimeFor(%q) = %q, want %q (src/lib/mime.ts)", name, got, tsMimeTable[ext])
		}
	}

	// The other direction. An extension only the Go table knows is an extension the built site
	// does not, so the same file would be typed one way by `frznforge dev` and another by
	// whatever host the site is deployed to — which is the exact class of drift this package
	// exists to close.
	for _, ext := range slices.Sorted(maps.Keys(mimeByExt)) {
		if _, ok := tsMimeTable[ext]; !ok {
			t.Errorf("mimeByExt has %q → %q, which src/lib/mime.ts does not; add it there too or drop it here", ext, mimeByExt[ext])
		}
	}
}

func TestMimeCharsets(t *testing.T) {
	// The half that only a browser would catch. Text served without a charset is decoded by
	// guesswork, and every non-ASCII glyph in a repo name or a commit message is the thing that
	// breaks.
	for _, ext := range []string{"html", "htm", "css", "js", "mjs"} {
		if got := MimeFor("f." + ext); !strings.HasSuffix(got, "; charset=utf-8") {
			t.Errorf("MimeFor(%q) = %q, want a UTF-8 charset — the site is UTF-8 throughout", "f."+ext, got)
		}
	}
	// And the inverse: a charset on a binary type is meaningless at best, and some proxies
	// treat it as a reason to transcode.
	for _, ext := range []string{"png", "jpg", "gif", "webp", "svg", "ico", "pdf", "zip", "wasm", "woff", "woff2", "ttf", "mp3", "mp4", "webm"} {
		if got := MimeFor("f." + ext); strings.Contains(got, "charset") {
			t.Errorf("MimeFor(%q) = %q, want no charset on a binary type", "f."+ext, got)
		}
	}
}

func TestMimeFallsBackToText(t *testing.T) {
	// The default is the interesting half of the table. `application/octet-stream` would turn
	// every "view the raw file" link on an unrecognised extension into a download prompt, and a
	// source repository is mostly unrecognised extensions.
	//
	// notes.ps1 is here by name: it is the file that caught the old split between
	// tests/e2e/serve.ts and the site's raw endpoint, one calling it octet-stream and the other
	// text/plain. One table is why that cannot recur.
	cases := []struct{ name, why string }{
		{"notes.ps1", "the .ps1 that caught the old two-table drift"},
		{"README", "no extension at all"},
		{"LICENSE", "the raw route serves this under its committed name"},
		{"main.rs", "an ordinary source file the table does not list"},
		{"archive.tar.gz", "only the final extension is inspected, and gz is not listed"},
		{".gitignore", "a leading dot is an extension to both implementations, and 'gitignore' is not a key"},
		{"", "the empty name"},
	}
	for _, c := range cases {
		if got := MimeFor(c.name); got != "text/plain; charset=utf-8" {
			t.Errorf("MimeFor(%q) = %q, want text/plain; charset=utf-8 (%s)", c.name, got, c.why)
		}
	}
}

func TestMimeIgnoresCase(t *testing.T) {
	// Windows checkouts hand back PHOTO.PNG, and a case-sensitive lookup would serve it as text.
	for _, c := range []struct{ name, want string }{
		{"PHOTO.PNG", "image/png"},
		{"Style.CSS", "text/css; charset=utf-8"},
		{"Module.MJS", "text/javascript; charset=utf-8"},
		{"Font.WOFF2", "font/woff2"},
	} {
		if got := MimeFor(c.name); got != c.want {
			t.Errorf("MimeFor(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMimeOnlyReadsTheFinalPathElement(t *testing.T) {
	// Go looks at the last element's extension; the TypeScript looks at the last dot in the
	// whole string. They cannot disagree — a candidate that spans a separator ("png/LICENSE")
	// is never a key — but the mechanisms differ, so the equivalence is worth pinning down
	// rather than assuming.
	for _, name := range []string{"repos/a/raw/main/dir.png/LICENSE", "a.png/b", "v1.2/notes"} {
		if got := MimeFor(name); got != "text/plain; charset=utf-8" {
			t.Errorf("MimeFor(%q) = %q, want text/plain; charset=utf-8 — a dot in a parent directory is not the file's extension", name, got)
		}
	}
	// The same path with a real extension still resolves, so the case above is not passing
	// merely because the fallback swallows everything.
	if got := MimeFor("repos/a/raw/main/dir.png/logo.png"); got != "image/png" {
		t.Errorf("MimeFor with a dotted parent directory = %q, want image/png", got)
	}
}
