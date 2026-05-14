package cmd

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHexColor(t *testing.T) {
	good := map[string]color.RGBA{
		"#000000":   {0, 0, 0, 255},
		"#1a1a1a":   {26, 26, 26, 255},
		"#FFFFFF":   {255, 255, 255, 255},
		"#a04b3a":   {0xa0, 0x4b, 0x3a, 255},
		"#11223380": {0x11, 0x22, 0x33, 0x80}, // explicit alpha
	}
	for in, want := range good {
		got, err := parseHexColor(in)
		if err != nil {
			t.Errorf("parseHexColor(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseHexColor(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "#", "#fff", "#12345", "#1234567", "1a1a1a", "#gghhii", "##ffffff"} {
		if _, err := parseHexColor(bad); err == nil {
			t.Errorf("parseHexColor(%q) = nil error, want a rejection", bad)
		}
	}
}

// TestImagesCommand exercises the full `techgo images` command end-to-end: a
// synthesized 1024×1024 source PNG (no ffmpeg needed) goes in, the expected
// seven files come out with the right dimensions, and re-running overwrites
// cleanly. The aspect-ratio warning fires for a non-square source.
func TestImagesCommand(t *testing.T) {
	proj := t.TempDir()

	// Synthesize a source PNG — a half-red, half-blue 1024×1024 square (square so
	// it doesn't trip the aspect-ratio warning here; a separate sub-case below
	// confirms the warning fires for non-square sources).
	src := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			if x < 512 {
				src.Set(x, y, color.RGBA{200, 30, 30, 255})
			} else {
				src.Set(x, y, color.RGBA{30, 30, 200, 255})
			}
		}
	}
	srcPath := filepath.Join(proj, "logo.png")
	if err := writePNGFile(srcPath, src); err != nil {
		t.Fatalf("writing source: %v", err)
	}

	out, err := runImagesCmd(t, proj, srcPath)
	if err != nil {
		t.Fatalf("techgo images: %v\noutput:\n%s", err, out)
	}

	want := []struct {
		name string
		w, h int
	}{
		{"favicon-16.png", 16, 16},
		{"favicon-32.png", 32, 32},
		{"apple-touch-icon.png", 180, 180},
		{"icon-512.png", 512, 512},
		{"og-image.png", 1200, 630},
		{"cover.png", 3000, 3000},
	}
	for _, w := range want {
		p := filepath.Join(proj, "static", w.name)
		cfg, err := decodePNGConfig(p)
		if err != nil {
			t.Errorf("%s: %v", w.name, err)
			continue
		}
		if cfg.Width != w.w || cfg.Height != w.h {
			t.Errorf("%s is %dx%d, want %dx%d", w.name, cfg.Width, cfg.Height, w.w, w.h)
		}
	}
	// favicon.ico exists and looks like an ICO file (magic 00 00 01 00).
	icoPath := filepath.Join(proj, "static", "favicon.ico")
	icoBytes, err := os.ReadFile(icoPath)
	if err != nil {
		t.Fatalf("favicon.ico: %v", err)
	}
	if len(icoBytes) < 4 || icoBytes[0] != 0 || icoBytes[1] != 0 || icoBytes[2] != 1 || icoBytes[3] != 0 {
		t.Errorf("favicon.ico does not begin with the ICO magic bytes 00 00 01 00")
	}

	// Re-run: should succeed, overwriting the previous outputs.
	if _, err := runImagesCmd(t, proj, srcPath); err != nil {
		t.Errorf("second techgo images run: %v", err)
	}
}

func TestImagesCommandWarnsOnNonSquareSource(t *testing.T) {
	proj := t.TempDir()
	src := image.NewRGBA(image.Rect(0, 0, 200, 100)) // 2:1
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i+3] = 255 // opaque black
	}
	srcPath := filepath.Join(proj, "wide.png")
	if err := writePNGFile(srcPath, src); err != nil {
		t.Fatalf("writing source: %v", err)
	}
	out, err := runImagesCmd(t, proj, srcPath)
	if err != nil {
		t.Fatalf("techgo images: %v", err)
	}
	if !strings.Contains(out, "warning:") || !strings.Contains(out, "center-cropped") {
		t.Errorf("expected an aspect-ratio warning in output, got:\n%s", out)
	}
}

func TestImagesCommandRequiresPNG(t *testing.T) {
	proj := t.TempDir()
	bogus := filepath.Join(proj, "not-a-png.png")
	if err := os.WriteFile(bogus, []byte("this is just text"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runImagesCmd(t, proj, bogus); err == nil {
		t.Error("techgo images on a non-PNG source: nil error, want a rejection")
	}
}

func TestImagesCommandUsesSiteCoverArtWhenNoArg(t *testing.T) {
	proj := t.TempDir()
	mustWriteFile(t, filepath.Join(proj, "site.yml"),
		"title: \"T\"\nauthor: \"a\"\nownerEmail: \"a@b\"\ncategory: \"Technology\"\n"+
			"baseURL: \"https://example.com\"\ncoverArt: \"logo.png\"\n")
	src := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for i := 0; i < len(src.Pix); i += 4 {
		src.Pix[i+0], src.Pix[i+1], src.Pix[i+2], src.Pix[i+3] = 0x1a, 0x1a, 0x1a, 0xff
	}
	if err := writePNGFile(filepath.Join(proj, "logo.png"), src); err != nil {
		t.Fatal(err)
	}
	out, err := runImagesCmd(t, proj /* no arg */)
	if err != nil {
		t.Fatalf("techgo images (no arg, site.coverArt set): %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(proj, "static", "favicon.ico")); err != nil {
		t.Errorf("favicon.ico missing after no-arg run: %v", err)
	}
}

// runImagesCmd invokes `techgo images` via the real cobra command, capturing
// combined stdout/stderr.
func runImagesCmd(t *testing.T, projectDir string, extra ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		projectFlag = "."
		outputFlag = "public"
		imagesOutFlag = ""
		imagesBgFlag = "#1a1a1a"
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"images", "--project", projectDir}, extra...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func writePNGFile(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func decodePNGConfig(path string) (image.Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return image.Config{}, err
	}
	defer f.Close()
	return png.DecodeConfig(f)
}
