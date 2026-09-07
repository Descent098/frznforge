package theme

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Brand asset guards — the port of tests/unit/assets.test.ts.
//
// These exist because of a specific regression: the old logo.png was a 1254×1254, 1.2 MB
// illustration served as a favicon on every single page load. The mark is now rendered from the
// vector master, and these checks keep the illustration from coming back — the shipped icons
// must stay small and stay real image files of the right format.
//
// They live beside the contrast checks because both answer the same kind of question about
// files the build copies verbatim and never inspects.

func TestVectorMasterExists(t *testing.T) {
	// assets/, not src/assets/ — 0.4.0 deleted src/ with the TypeScript engine, and the vector
	// master is neither engine nor shipped output. It is the source the PNG and the ICO below are
	// rendered from, so it lives beside them at the root rather than inside public/, which is
	// copied into every build verbatim.
	svg := readAsset(t, "assets", "logo.svg")
	for _, want := range []string{"<svg", `viewBox="0 0 128 128"`} {
		if !bytes.Contains(svg, []byte(want)) {
			t.Errorf("logo.svg does not contain %q", want)
		}
	}
}

func TestLogoPNGIsSmallEnoughForAFavicon(t *testing.T) {
	png := readAsset(t, "public", "logo.png")
	magic := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}
	if len(png) < 8 || !bytes.Equal(png[:8], magic) {
		t.Fatal("public/logo.png is not a PNG")
	}
	// The 1.2 MB favicon must not return.
	if limit := 100 * 1024; len(png) >= limit {
		t.Errorf("logo.png is %d bytes, over the %d-byte favicon budget", len(png), limit)
	}
}

func TestFaviconHasTheThreeClassicSizes(t *testing.T) {
	ico := readAsset(t, "public", "favicon.ico")
	if len(ico) < 6+3*16 {
		t.Fatal("public/favicon.ico is too short to hold three directory entries")
	}
	u16 := func(off int) uint16 { return binary.LittleEndian.Uint16(ico[off:]) }
	if u16(0) != 0 {
		t.Errorf("ICO reserved field = %d, want 0", u16(0))
	}
	if u16(2) != 1 {
		t.Errorf("ICO type = %d, want 1 (icon)", u16(2))
	}
	if n := u16(4); n != 3 {
		t.Fatalf("ICO holds %d images, want 3 (16 + 32 + 48)", n)
	}
	// Each directory entry is 16 bytes and opens with its width.
	sizes := []int{int(ico[6]), int(ico[22]), int(ico[38])}
	sort.Ints(sizes)
	want := []int{16, 32, 48}
	for i := range want {
		if sizes[i] != want[i] {
			t.Fatalf("ICO sizes = %v, want %v", sizes, want)
		}
	}
	if limit := 50 * 1024; len(ico) >= limit {
		t.Errorf("favicon.ico is %d bytes, over the %d-byte budget", len(ico), limit)
	}
}

func readAsset(t *testing.T, parts ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(append([]string{moduleRoot(t)}, parts...)...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(parts...), err)
	}
	return raw
}
