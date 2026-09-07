package serve

import (
	"path/filepath"
	"strings"
)

// mimeByExt is the port of src/lib/mime.ts, extension (no dot, lowercase) → content type.
//
// It is a fixed table rather than mime.TypeByExtension on purpose. Go's stdlib lookup consults
// the machine — the Windows registry, /etc/mime.types — so the same file could be served as
// text/plain here and application/octet-stream on the next box, and the raw endpoints exist to
// hand back committed bytes with a type a browser will display rather than download.
//
// Small on purpose too: it covers what actually turns up in a source repository or a notes
// folder, and everything else is text.
var mimeByExt = map[string]string{
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

// MimeFor returns the Content-Type for a file path; only the final extension is inspected.
//
// Unknown falls back to UTF-8 plain text, which is the interesting half of the decision: an
// unrecognised extension in a source repository is far more likely to be text than a binary,
// and application/octet-stream would turn "view the raw file" into a download prompt.
func MimeFor(name string) string {
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
	if t, ok := mimeByExt[ext]; ok {
		return t
	}
	return "text/plain; charset=utf-8"
}
