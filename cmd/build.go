package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/rBurgett/techgo/internal/config"
	"github.com/rBurgett/techgo/internal/images"
	"github.com/rBurgett/techgo/internal/media"
	"github.com/rBurgett/techgo/internal/render"
	"github.com/spf13/cobra"
)

// outputMarker is written at the root of the build output directory after every
// build. `techgo build` refuses to os.RemoveAll a directory that doesn't carry
// this marker, so pointing --output at a non-techgo directory can't wipe it.
const outputMarker = ".techgo-output"

var buildNoClean bool

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Render the site into the output directory",
	Long: `build renders the full static site into the output directory (default
./public): copies static/ assets, transcodes each episode's source media
idempotently (like "techgo media"), generates the square podcast cover art from
site.coverArt if it isn't already present, then writes index.html, an
episodes/<slug>.html page per episode, about.html, 404.html, the podcast feed
(feed.rss), the sitemap (sitemap.xml), and a robots.txt regenerated from
site.baseURL.

The output directory is cleaned first unless --no-clean is given; to avoid
accidents it is only removed when it carries the marker file this command writes,
and never when it is the project directory, the home directory, or a path near
the filesystem root. Requires ffmpeg and ffprobe on PATH.`,
	Args: cobra.NoArgs,
	RunE: runBuild,
}

func init() {
	buildCmd.Flags().BoolVar(&buildNoClean, "no-clean", false,
		"don't remove the output directory before building")
	rootCmd.AddCommand(buildCmd)
}

func runBuild(cmd *cobra.Command, _ []string) error {
	projectDir, err := ProjectDir()
	if err != nil {
		return err
	}
	outputDir, err := OutputDir()
	if err != nil {
		return err
	}
	if err := validateOutputDir(outputDir, projectDir); err != nil {
		return err
	}

	// Everything that can fail without touching the output directory goes first,
	// so a bad config or a broken template never leaves a half-wiped output dir.
	site, err := config.LoadSite(projectDir)
	if err != nil {
		return err
	}
	episodes, err := config.LoadEpisodes(projectDir)
	if err != nil {
		return err
	}
	renderer, err := render.New(projectDir, site)
	if err != nil {
		return err
	}
	if err := media.RequireTools(); err != nil {
		return err
	}

	w := cmd.OutOrStdout()

	if err := prepareOutputDir(outputDir, buildNoClean); err != nil {
		return err
	}

	// static/ assets, then a robots.txt regenerated from baseURL so its Sitemap:
	// line can never drift from the configured base URL.
	if err := copyStatic(projectDir, outputDir); err != nil {
		return err
	}
	if err := writeRobots(outputDir, site); err != nil {
		return fmt.Errorf("writing robots.txt: %w", err)
	}

	// Media: transcode (idempotently) and copy into the build; fill in each
	// episode's MP3/MP4 byte size and duration for the feed and pages.
	if err := buildMedia(w, projectDir, outputDir, episodes); err != nil {
		return err
	}

	// Cover art: generate <output>/cover.png from site.coverArt if not present.
	if err := buildCover(w, projectDir, outputDir, site); err != nil {
		return err
	}

	// Pages.
	if err := writeRendered(outputDir, "index.html", func(out io.Writer) error {
		return renderer.RenderIndex(out, episodes)
	}); err != nil {
		return err
	}
	for i := range episodes {
		ep := &episodes[i]
		var prev, next *config.Episode
		if i+1 < len(episodes) { // episodes are newest-first: the next index is the older episode
			prev = &episodes[i+1]
		}
		if i > 0 {
			next = &episodes[i-1]
		}
		if err := writeRendered(outputDir, ep.PagePath(), func(out io.Writer) error {
			return renderer.RenderEpisode(out, ep, prev, next)
		}); err != nil {
			return err
		}
	}
	if err := writeRendered(outputDir, "about.html", renderer.RenderAbout); err != nil {
		return err
	}
	if err := writeRendered(outputDir, "404.html", renderer.Render404); err != nil {
		return err
	}

	// Feed + sitemap (built with encoding/xml, not templates).
	if err := writeRendered(outputDir, "feed.rss", func(out io.Writer) error {
		return render.WriteFeed(out, site, episodes)
	}); err != nil {
		return err
	}
	if err := writeRendered(outputDir, "sitemap.xml", func(out io.Writer) error {
		return render.WriteSitemap(out, site, episodes)
	}); err != nil {
		return err
	}

	files, total, err := dirStats(outputDir)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "\nbuilt %d episode(s) -> %s (%d file(s), %s)\n",
		len(episodes), outputDir, files, humanBytes(total))
	return nil
}

// validateOutputDir rejects an --output path that would be dangerous to
// os.RemoveAll: empty/relative, a filesystem/volume root, the user's home
// directory, the project directory itself or one of its ancestors, or anything
// fewer than two path components deep (so a top-level dir like "/var" can't be
// wiped).
func validateOutputDir(outputDir, projectDir string) error {
	clean := filepath.Clean(outputDir)
	if clean == "" || clean == "." {
		return fmt.Errorf("refusing to use %q as the build output directory", outputDir)
	}
	if !filepath.IsAbs(clean) {
		return fmt.Errorf("build output directory must be an absolute path, got %q", outputDir)
	}
	sep := string(filepath.Separator)
	// "/" on Unix, plus a Windows drive root ("C:\") or UNC share root
	// ("\\server\share[\]"); filepath.VolumeName is "" on Unix, so this is just
	// the "/" check there.
	if vol := filepath.VolumeName(clean); clean == sep || clean == vol || clean == vol+sep {
		return fmt.Errorf("refusing to use the filesystem root %q as the build output directory", clean)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" && clean == filepath.Clean(home) {
		return fmt.Errorf("refusing to use the home directory %q as the build output directory", clean)
	}
	if clean == projectDir {
		return fmt.Errorf("refusing to use the project directory %q as the build output directory — pass --output elsewhere", clean)
	}
	if dirContains(clean, projectDir) {
		return fmt.Errorf("refusing to use %q as the build output directory: it contains the project directory", clean)
	}
	if len(strings.Split(strings.Trim(clean, sep), sep)) < 2 {
		return fmt.Errorf("refusing to use %q as the build output directory: too close to the filesystem root", clean)
	}
	return nil
}

// dirContains reports whether dir is a strict ancestor of other (both cleaned,
// absolute paths).
func dirContains(dir, other string) bool {
	rel, err := filepath.Rel(dir, other)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// prepareOutputDir empties outputDir (unless noClean) and recreates it, then
// drops the recognition marker. It only removes a directory that already carries
// that marker — validateOutputDir handled the path-level guard.
func prepareOutputDir(outputDir string, noClean bool) error {
	if !noClean {
		info, err := os.Stat(outputDir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			// nothing to clean
		case err != nil:
			return fmt.Errorf("checking %s: %w", outputDir, err)
		case !info.IsDir():
			return fmt.Errorf("%s exists and is not a directory", outputDir)
		default:
			markerPath := filepath.Join(outputDir, outputMarker)
			if _, err := os.Stat(markerPath); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("refusing to clean %s: not a techgo output dir (no %s marker) — use --no-clean or point --output elsewhere", outputDir, outputMarker)
				}
				return fmt.Errorf("checking %s: %w", markerPath, err)
			}
			if err := os.RemoveAll(outputDir); err != nil {
				return fmt.Errorf("cleaning %s: %w", outputDir, err)
			}
		}
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outputDir, err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, outputMarker),
		[]byte("techgo build output — generated by `techgo build`, safe to delete\n"), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outputMarker, err)
	}
	return nil
}

// copyStatic copies the project's static/ tree into outputDir. A missing
// static/ directory is allowed (the site just has no extra assets).
func copyStatic(projectDir, outputDir string) error {
	staticDir := filepath.Join(projectDir, "static")
	info, err := os.Stat(staticDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s: %w", staticDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", staticDir)
	}
	return filepath.WalkDir(staticDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(staticDir, path)
		if err != nil {
			return err
		}
		target := filepath.Join(outputDir, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			return copyFile(path, target)
		default:
			return nil // skip symlinks, sockets, etc.
		}
	})
}

// copyFile copies the regular file src to dst, creating dst's parent directory.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copying %s -> %s: %w", src, dst, err)
	}
	return out.Close()
}

// writeRobots writes outputDir/robots.txt with a Sitemap line derived from the
// site's base URL, overwriting any robots.txt carried in from static/.
func writeRobots(outputDir string, site *config.Site) error {
	body := "User-agent: *\nAllow: /\nSitemap: " + site.AbsURL("sitemap.xml") + "\n"
	return os.WriteFile(filepath.Join(outputDir, "robots.txt"), []byte(body), 0o644)
}

// buildMedia transcodes every episode's source media (rebuilding an output only
// when missing or older than its source), copies the results into
// outputDir/media/, and records each episode's MP3/MP4 byte size and duration on
// the in-memory slice for the feed and pages. The caller must have verified
// ffmpeg/ffprobe are available.
func buildMedia(w io.Writer, projectDir, outputDir string, episodes []config.Episode) error {
	transcodeDir := filepath.Join(projectDir, "media", "out")
	if err := os.MkdirAll(transcodeDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", transcodeDir, err)
	}
	mediaOut := filepath.Join(outputDir, "media")
	if err := os.MkdirAll(mediaOut, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", mediaOut, err)
	}
	for i := range episodes {
		ep := &episodes[i]
		if err := transcodeEpisode(w, projectDir, transcodeDir, *ep, false); err != nil {
			return err
		}
		for _, m := range []struct {
			name  string
			size  *int64
			dur   *int
			label string
		}{
			{ep.Pad() + ".mp3", &ep.MP3Size, &ep.AudioDur, "audio"},
			{ep.Pad() + ".mp4", &ep.MP4Size, &ep.VideoDur, "video"},
		} {
			src := filepath.Join(transcodeDir, m.name)
			dst := filepath.Join(mediaOut, m.name)
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("episode %d: copying %s into the build: %w", ep.Number, m.label, err)
			}
			fi, err := os.Stat(dst)
			if err != nil {
				return fmt.Errorf("episode %d: %w", ep.Number, err)
			}
			*m.size = fi.Size()
			secs, err := media.ProbeDurationSeconds(src)
			if err != nil {
				fmt.Fprintf(w, "  warning: episode %d: could not probe %s duration: %v\n", ep.Number, m.label, err)
				secs = 0
			}
			*m.dur = secs
		}
	}
	return nil
}

// buildCover generates outputDir/cover.png — the square podcast cover art — from
// site.coverArt, unless cover.png is already present (e.g. carried in from
// static/). A missing or unset coverArt source is a warning, not an error: the
// build still completes and the feed still points at /cover.png; the user is told
// to run `techgo images` or fix site.yml.
func buildCover(w io.Writer, projectDir, outputDir string, site *config.Site) error {
	dst := filepath.Join(outputDir, "cover.png")
	if _, err := os.Stat(dst); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking %s: %w", dst, err)
	}
	if strings.TrimSpace(site.CoverArt) == "" {
		fmt.Fprintln(w, "warning: site.coverArt is unset — not generating cover.png; run `techgo images` or set coverArt in site.yml")
		return nil
	}
	src := filepath.Join(projectDir, filepath.FromSlash(site.CoverArt))
	if _, err := os.Stat(src); errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(w, "warning: cover-art source %s not found — not generating cover.png; run `techgo images` or fix site.coverArt\n", src)
		return nil
	} else if err != nil {
		return fmt.Errorf("checking %s: %w", src, err)
	}
	img, err := images.LoadPNG(src)
	if err != nil {
		return fmt.Errorf("reading cover art %s: %w", src, err)
	}
	if err := images.SavePNG(images.ResizeSquare(img, 3000), dst); err != nil {
		return fmt.Errorf("writing %s: %w", dst, err)
	}
	fmt.Fprintf(w, "generated cover.png (3000x3000) from %s\n", site.CoverArt)
	return nil
}

// writeRendered renders into a buffer, then writes outputDir/<rel> (rel uses
// forward slashes), creating parent directories. Buffering means a render error
// never leaves a half-written page on disk.
func writeRendered(outputDir, rel string, fn func(io.Writer) error) error {
	var buf bytes.Buffer
	if err := fn(&buf); err != nil {
		return fmt.Errorf("rendering %s: %w", rel, err)
	}
	target := filepath.Join(outputDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(target, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", target, err)
	}
	return nil
}

// dirStats counts the regular files under root and sums their sizes.
func dirStats(root string) (files int, total int64, err error) {
	werr := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		total += info.Size()
		return nil
	})
	return files, total, werr
}
