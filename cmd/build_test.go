package cmd

import (
	"bytes"
	"encoding/xml"
	"image"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOutputDir(t *testing.T) {
	proj := t.TempDir()

	good := filepath.Join(proj, "public")
	if err := validateOutputDir(good, proj); err != nil {
		t.Errorf("validateOutputDir(%q, project) = %v, want nil", good, err)
	}

	bad := map[string]string{
		"empty string":        "",
		"relative path":       "public",
		"filesystem root":     string(filepath.Separator),
		"project dir itself":  proj,
		"ancestor of project": filepath.Dir(proj),
	}
	for name, out := range bad {
		if err := validateOutputDir(out, proj); err == nil {
			t.Errorf("%s: validateOutputDir(%q, project) = nil, want an error", name, out)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if err := validateOutputDir(home, proj); err == nil {
			t.Errorf("validateOutputDir(home %q, project) = nil, want an error", home)
		}
	}
}

// TestValidateOutputDirSymlink verifies the guards also reject paths whose
// symlink-resolved form is unsafe (project dir, home dir) — os.RemoveAll
// follows symlinks in path components, so a lexical-only check isn't enough.
func TestValidateOutputDirSymlink(t *testing.T) {
	proj := t.TempDir()
	linksDir := t.TempDir()

	toProject := filepath.Join(linksDir, "to-project")
	if err := os.Symlink(proj, toProject); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := validateOutputDir(toProject, proj); err == nil {
		t.Error("validateOutputDir(symlink-to-project, project) = nil, want an error")
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		toHome := filepath.Join(linksDir, "to-home")
		if err := os.Symlink(home, toHome); err == nil {
			if err := validateOutputDir(toHome, proj); err == nil {
				t.Error("validateOutputDir(symlink-to-home, project) = nil, want an error")
			}
		}
	}
}

// TestBuildCommand runs the whole `techgo build` against a throwaway project
// with tiny ffmpeg-generated source media, then checks the output tree, the
// regenerated robots.txt, an episode page, the feed (well-formed, with the real
// MP3 byte size), and the generated cover art — and that a second build skips
// up-to-date media.
func TestBuildCommand(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}

	proj := t.TempDir()
	mustCopyTree(t, filepath.Join("..", "templates"), filepath.Join(proj, "templates"))
	mustCopyTree(t, filepath.Join("..", "static"), filepath.Join(proj, "static"))
	mustCopyFile(t, filepath.Join("..", "site.yml"), filepath.Join(proj, "site.yml"))

	// A cover-art source so the build also exercises internal/images.
	runFFmpeg(t, "-f", "lavfi", "-i", "color=c=0x1a1a1a:s=128x128", "-frames:v", "1", "-update", "1",
		filepath.Join(proj, "static", "logo-source.png"))

	srcDir := filepath.Join(proj, "media", "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// ~2s of audio so the transcoded MP3 rounds to a non-zero <itunes:duration>.
	runFFmpeg(t, "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-ac", "2", "-ar", "44100",
		filepath.Join(srcDir, "0001.wav"))
	runFFmpeg(t, "-f", "lavfi", "-i", "testsrc=size=64x64:rate=10:duration=0.2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-profile:v", "baseline",
		"-c:a", "aac", "-ar", "44100", "-ac", "2", "-shortest",
		filepath.Join(srcDir, "0001.mp4"))

	dataDir := filepath.Join(proj, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(dataDir, "0001.yml"),
		"number: 1\n"+
			"title: \"Episode One & <Two>\"\n"+
			"slug: \"episode-one\"\n"+
			"shortDescription: \"A short & <punchy> teaser.\"\n"+
			"pubDate: 2026-05-11T12:00:00Z\n"+
			"sourceWav: \"media/src/0001.wav\"\n"+
			"sourceMp4: \"media/src/0001.mp4\"\n"+
			"description: |\n  <p>Show notes with <em>markup</em> &amp; an entity.</p>\n")

	out := filepath.Join(proj, "public")
	stdout, err := runBuildCmd(t, proj, out)
	if err != nil {
		t.Fatalf("techgo build: %v\noutput:\n%s", err, stdout)
	}

	for _, rel := range []string{
		".techgo-output",
		"index.html",
		"about.html",
		"404.html",
		"feed.rss",
		"sitemap.xml",
		"robots.txt",
		"cover.png",
		filepath.Join("css", "style.css"),
		"site.webmanifest",
		filepath.Join("episodes", "episode-one.html"),
		filepath.Join("media", "0001.mp3"),
		filepath.Join("media", "0001.mp4"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing build output %s: %v", rel, err)
		}
	}

	// robots.txt is regenerated from site.baseURL, not copied from static/.
	if robots := mustRead(t, filepath.Join(out, "robots.txt")); !strings.Contains(robots, "Sitemap: https://techgo.example.com/sitemap.xml") {
		t.Errorf("robots.txt missing the regenerated Sitemap line:\n%s", robots)
	}

	// The home page links the episode by its slug.
	if index := mustRead(t, filepath.Join(out, "index.html")); !strings.Contains(index, `href="/episodes/episode-one.html"`) {
		t.Errorf("index.html does not link the episode page")
	}

	// The episode page links its (number-keyed) media and renders trusted notes.
	page := mustRead(t, filepath.Join(out, "episodes", "episode-one.html"))
	for _, want := range []string{
		`src="/media/0001.mp3"`,
		`src="/media/0001.mp4"`,
		`<div class="show-notes"><p>Show notes with <em>markup</em> &amp; an entity.</p>`,
		`<link rel="canonical" href="https://techgo.example.com/episodes/episode-one.html">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("episode page missing %q", want)
		}
	}

	// The feed must be well-formed and carry the real MP3 byte size + integer duration.
	var feed struct {
		Channel struct {
			Items []struct {
				Enclosure struct {
					URL    string `xml:"url,attr"`
					Length int64  `xml:"length,attr"`
				} `xml:"enclosure"`
				Duration int `xml:"duration"` // itunes:duration
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(mustRead(t, filepath.Join(out, "feed.rss"))), &feed); err != nil {
		t.Fatalf("feed.rss is not well-formed XML: %v", err)
	}
	if len(feed.Channel.Items) != 1 {
		t.Fatalf("feed has %d items, want 1", len(feed.Channel.Items))
	}
	mp3Info, err := os.Stat(filepath.Join(out, "media", "0001.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if got := feed.Channel.Items[0].Enclosure.Length; got != mp3Info.Size() {
		t.Errorf("feed <enclosure length> = %d, want the actual MP3 byte size %d", got, mp3Info.Size())
	}
	if got := feed.Channel.Items[0].Enclosure.URL; got != "https://techgo.example.com/media/0001.mp3" {
		t.Errorf("feed <enclosure url> = %q", got)
	}
	if feed.Channel.Items[0].Duration <= 0 {
		t.Errorf("feed <itunes:duration> = %d, want a positive integer", feed.Channel.Items[0].Duration)
	}

	// cover.png is the square cover art at the iTunes size (upscaled from the tiny source).
	if cfg := mustImageConfig(t, filepath.Join(out, "cover.png")); cfg.Width != 3000 || cfg.Height != 3000 {
		t.Errorf("cover.png is %dx%d, want 3000x3000", cfg.Width, cfg.Height)
	}

	// A second build succeeds and skips up-to-date media.
	stdout2, err := runBuildCmd(t, proj, out)
	if err != nil {
		t.Fatalf("second techgo build: %v\n%s", err, stdout2)
	}
	if !strings.Contains(stdout2, "skip (up to date)") {
		t.Errorf("second build did not skip up-to-date media:\n%s", stdout2)
	}
}

// runBuildCmd invokes `techgo build` against projectDir via the real cobra
// command, returning its combined stdout/stderr and the command error.
func runBuildCmd(t *testing.T, projectDir, outputDir string, extraArgs ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		projectFlag = "."
		outputFlag = "public"
		buildNoClean = false
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"build", "--project", projectDir, "--output", outputDir}, extraArgs...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	full := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)
	if out, err := exec.Command("ffmpeg", full...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
	}
}

func mustCopyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatalf("copying %s -> %s: %v", src, dst, err)
	}
}

func mustCopyFile(t *testing.T, src, dst string) {
	t.Helper()
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copying %s -> %s: %v", src, dst, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func mustImageConfig(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	return cfg
}
