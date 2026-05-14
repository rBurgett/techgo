package images

import (
	"bytes"
	"encoding/binary"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeICOByteLayout(t *testing.T) {
	src := solidImage(200, 200, color.RGBA{0, 128, 255, 255})
	sizes := []int{16, 32, 48}

	var buf bytes.Buffer
	if err := EncodeICO(&buf, src, sizes...); err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}
	data := buf.Bytes()

	// ICONDIR
	if got := binary.LittleEndian.Uint16(data[0:2]); got != 0 {
		t.Errorf("reserved = %d, want 0", got)
	}
	if got := binary.LittleEndian.Uint16(data[2:4]); got != 1 {
		t.Errorf("type = %d, want 1 (ICO)", got)
	}
	if got := binary.LittleEndian.Uint16(data[4:6]); int(got) != len(sizes) {
		t.Errorf("count = %d, want %d", got, len(sizes))
	}

	// One ICONDIRENTRY per size, in order; each ImageOffset/BytesInRes points
	// at a well-formed PNG of the expected dimensions.
	const headerLen = 6
	const entryLen = 16
	for i, sz := range sizes {
		off := headerLen + entryLen*i
		w, h := data[off], data[off+1]
		planes := binary.LittleEndian.Uint16(data[off+4 : off+6])
		bpp := binary.LittleEndian.Uint16(data[off+6 : off+8])
		bir := binary.LittleEndian.Uint32(data[off+8 : off+12])
		ioff := binary.LittleEndian.Uint32(data[off+12 : off+16])

		if int(w) != sz || int(h) != sz {
			t.Errorf("entry %d w/h = %d/%d, want %d/%d", i, w, h, sz, sz)
		}
		if planes != 1 {
			t.Errorf("entry %d planes = %d, want 1", i, planes)
		}
		if bpp != 32 {
			t.Errorf("entry %d bpp = %d, want 32", i, bpp)
		}
		end := int(ioff) + int(bir)
		if end > len(data) {
			t.Fatalf("entry %d payload runs past EOF: offset=%d len=%d total=%d", i, ioff, bir, len(data))
		}
		// Each layer is a complete PNG that decodes to a sz×sz image.
		cfg, err := png.DecodeConfig(bytes.NewReader(data[ioff:end]))
		if err != nil {
			t.Fatalf("entry %d is not a valid PNG: %v", i, err)
		}
		if cfg.Width != sz || cfg.Height != sz {
			t.Errorf("entry %d PNG is %dx%d, want %dx%d", i, cfg.Width, cfg.Height, sz, sz)
		}
	}

	// `file(1)`'s heuristic for ICOs: magic bytes 00 00 01 00.
	if data[0] != 0 || data[1] != 0 || data[2] != 1 || data[3] != 0 {
		t.Errorf("ICO magic = % x, want 00 00 01 00", data[0:4])
	}
}

func TestEncodeICO256(t *testing.T) {
	// 256 must be encoded as the single byte 0 in the directory entry, per the
	// ICO format. The PNG payload is still 256×256.
	src := solidImage(64, 64, color.RGBA{255, 255, 255, 255})
	var buf bytes.Buffer
	if err := EncodeICO(&buf, src, 256); err != nil {
		t.Fatalf("EncodeICO: %v", err)
	}
	data := buf.Bytes()
	if data[6] != 0 || data[7] != 0 {
		t.Errorf("entry width/height bytes = %d/%d, want 0/0 for 256", data[6], data[7])
	}
	ioff := binary.LittleEndian.Uint32(data[18:22])
	bir := binary.LittleEndian.Uint32(data[14:18])
	cfg, err := png.DecodeConfig(bytes.NewReader(data[ioff : ioff+bir]))
	if err != nil {
		t.Fatalf("PNG payload: %v", err)
	}
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("PNG is %dx%d, want 256x256", cfg.Width, cfg.Height)
	}
}

func TestEncodeICORejectsInvalidSizes(t *testing.T) {
	src := solidImage(32, 32, color.RGBA{1, 2, 3, 255})
	var buf bytes.Buffer
	if err := EncodeICO(&buf, src); err == nil {
		t.Error("EncodeICO with no sizes: nil error, want a rejection")
	}
	for _, sz := range []int{0, -1, 257, 1024} {
		buf.Reset()
		if err := EncodeICO(&buf, src, sz); err == nil {
			t.Errorf("EncodeICO size=%d: nil error, want a rejection", sz)
		}
	}
}

func TestSaveICOWritesAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "favicon.ico")
	src := solidImage(64, 64, color.RGBA{0, 0, 0, 255})
	if err := SaveICO(src, path, 16, 32); err != nil {
		t.Fatalf("SaveICO: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("favicon.ico not at %s: %v", path, err)
	}
	// No leftover temp files in the destination directory.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
