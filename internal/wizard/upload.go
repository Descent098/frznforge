package wizard

// /api/upload — a picture for the owner, an organization or a contributor.
//
// The browser sends WHICH avatar this is and the bytes; it never sends a path. The filename is
// derived here from the target plus the type sniffed out of the magic bytes, and always lands
// under <config dir>/public/images/. The config field is NOT set here: the page follows up with
// the ordinary validated `set`/`setAt` operation, so there is exactly one code path that writes
// config, with its schema check and its rollback. A stray image nothing points at is harmless.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// maxImageBytes is the decoded ceiling. An avatar is a few tens of kilobytes; six megabytes is
// already absurd, and refusing above it keeps a runaway upload from filling public/.
const maxImageBytes = 6 << 20

// imageTypes are the image kinds the wizard will write, identified by their magic bytes rather
// than by anything the browser claims. The extension comes from THIS table, never from the
// upload's own name.
var imageTypes = []struct {
	ext string
	is  func([]byte) bool
}{
	{"png", func(b []byte) bool { return bytes.HasPrefix(b, []byte{137, 80, 78, 71, 13, 10, 26, 10}) }},
	{"jpg", func(b []byte) bool { return bytes.HasPrefix(b, []byte{0xff, 0xd8, 0xff}) }},
	{"webp", func(b []byte) bool {
		return len(b) > 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP"))
	}},
	{"gif", func(b []byte) bool {
		return bytes.HasPrefix(b, []byte("GIF87a")) || bytes.HasPrefix(b, []byte("GIF89a"))
	}},
	{"svg", func(b []byte) bool {
		// SVG is markup, not a binary format: sniffed only after the binary types have been
		// ruled out, and only when it really opens as XML or SVG.
		head := b
		if len(head) > 512 {
			head = head[:512]
		}
		return svgOpening.Match(head)
	}},
}

var svgOpening = regexp.MustCompile(`(?is)^\s*(<\?xml.{0,200}?)?<svg[\s>]`)

// safeSlug keeps a target from introducing a path segment of its own.
var safeSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// uploadPathFor is the public/-relative path for a target — the server's decision, not the
// page's.
func uploadPathFor(target map[string]any, ext string) (string, error) {
	switch target["kind"] {
	case "owner":
		return "images/owner." + ext, nil
	case "org":
		slug, _ := target["slug"].(string)
		if !safeSlug.MatchString(slug) {
			return "", badRequest("invalid target.slug")
		}
		return "images/orgs/" + slug + "." + ext, nil
	case "contributor":
		index, err := asIndex(target["index"], "target")
		if err != nil || index > 999 {
			return "", badRequest("invalid target.index")
		}
		return fmt.Sprintf("images/contributors/%d.%s", index, ext), nil
	default:
		return "", badRequest("unknown upload target")
	}
}

// decodeImageUpload decodes a base64 image and identifies it by its magic bytes.
//
// The extension it returns comes from the sniffed type, so an upload claiming .png while
// carrying a JPEG gets .jpg — and one carrying a shell script is refused.
func decodeImageUpload(value any) ([]byte, string, error) {
	data, ok := value.(string)
	if !ok || data == "" {
		return nil, "", badRequest("missing image data")
	}
	// A data: URL as well as bare base64 — the browser's FileReader produces the former.
	payload := data
	if strings.HasPrefix(data, "data:") {
		if comma := strings.IndexByte(data, ','); comma >= 0 {
			payload = data[comma+1:]
		}
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", badRequest("image data is not base64")
	}
	if len(raw) == 0 {
		return nil, "", badRequest("image data is empty")
	}
	if len(raw) > maxImageBytes {
		return nil, "", badRequest("image is larger than 6 MB")
	}
	for _, kind := range imageTypes {
		if kind.is(raw) {
			return raw, kind.ext, nil
		}
	}
	return nil, "", badRequest("that does not look like a PNG, JPEG, WebP, GIF or SVG")
}

func (s *Session) handleUpload(w http.ResponseWriter, body map[string]any) error {
	if s.ConfigPath == "" {
		return conflict("no config file was found, so there is nowhere to put an image")
	}
	target, err := asRecord(body["target"])
	if err != nil {
		return err
	}
	raw, ext, err := decodeImageUpload(body["data"])
	if err != nil {
		return err
	}
	relative, err := uploadPathFor(target, ext)
	if err != nil {
		return err
	}

	public := filepath.Join(filepath.Dir(s.ConfigPath), "public")
	file := filepath.Join(public, filepath.FromSlash(relative))
	// Belt and braces: uploadPathFor builds the path out of validated pieces, but assert the
	// result really is inside public/ before anything is written.
	if rel, err := filepath.Rel(public, file); err != nil || rel != filepath.FromSlash(path.Clean(relative)) {
		return badRequest("refusing to write outside public/")
	}

	s.mu.Lock()
	err = s.storeImage(file, raw)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	s.sendJSON(w, http.StatusOK, map[string]any{
		"path":  relative,
		"bytes": len(raw),
		"file":  file,
	}, false)
	return nil
}

// storeImage writes the image, backing up whatever it replaces. The caller holds s.mu.
func (s *Session) storeImage(file string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return conflict(fmt.Sprintf("could not create %s: %s", filepath.Dir(file), err))
	}
	if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
		// A byte-preserving copy: an image must never be round-tripped through text.
		s.backupBinaryOnce(file)
	} else {
		s.markCreated(file)
	}
	if err := os.WriteFile(file, raw, 0o644); err != nil {
		return conflict(fmt.Sprintf("could not write %s: %s", file, err))
	}
	s.writes++
	return nil
}
