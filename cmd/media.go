package cmd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/rBurgett/techgo/internal/config"
	"github.com/rBurgett/techgo/internal/media"
	"github.com/spf13/cobra"
)

var (
	mediaForce     bool
	mediaEpisodeNo int
)

var mediaCmd = &cobra.Command{
	Use:   "media",
	Short: "Transcode episode source media with ffmpeg (idempotent)",
	Long: `media transcodes each episode's source recordings — the sourceWav and
sourceMp4 paths declared in data/*.yml — into the web-ready files under media/out/:

  media/out/NNNN.mp3   128 kbps CBR MP3 (libmp3lame), 44.1 kHz stereo, tags stripped
  media/out/NNNN.mp4   "faststart" progressive MP4 — remuxed losslessly if the source is
                       already H.264/AAC, otherwise re-encoded (H.264 high / yuv420p / AAC)

An output is (re)built only when it is missing or older than its source (compared
by file mtime); pass --force to rebuild everything, or --episode N to limit the
run to one episode. Requires ffmpeg and ffprobe on PATH.`,
	Args: cobra.NoArgs,
	RunE: runMedia,
}

func init() {
	mediaCmd.Flags().BoolVar(&mediaForce, "force", false,
		"rebuild every output even if it is up to date")
	mediaCmd.Flags().IntVar(&mediaEpisodeNo, "episode", 0,
		"transcode only this episode number (default: all episodes)")
	rootCmd.AddCommand(mediaCmd)
}

func runMedia(cmd *cobra.Command, _ []string) error {
	if err := media.RequireTools(); err != nil {
		return err
	}

	projectDir, err := ProjectDir()
	if err != nil {
		return err
	}
	episodes, err := config.LoadEpisodes(projectDir)
	if err != nil {
		return err
	}
	if mediaEpisodeNo != 0 {
		var only []config.Episode
		for _, ep := range episodes {
			if ep.Number == mediaEpisodeNo {
				only = append(only, ep)
			}
		}
		if len(only) == 0 {
			return fmt.Errorf("no episode numbered %d in %s",
				mediaEpisodeNo, filepath.Join(projectDir, config.EpisodesDir))
		}
		episodes = only
	}
	if len(episodes) == 0 {
		return fmt.Errorf("no episodes found in %s — run \"techgo new\" first",
			filepath.Join(projectDir, config.EpisodesDir))
	}

	outDir := filepath.Join(projectDir, "media", "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	w := cmd.OutOrStdout()
	for i := range episodes {
		if err := transcodeEpisode(w, projectDir, outDir, episodes[i], mediaForce); err != nil {
			return err
		}
	}
	printMediaSummary(w, outDir, episodes)
	return nil
}

// transcodeEpisode (re)builds episode ep's MP3 and MP4 under outDir, skipping
// either output that is already up to date.
func transcodeEpisode(w io.Writer, projectDir, outDir string, ep config.Episode, force bool) error {
	fmt.Fprintf(w, "episode %d: %s\n", ep.Number, ep.Title)

	srcWav, err := resolveExistingSource(projectDir, ep.SourceWav, ep, "sourceWav")
	if err != nil {
		return err
	}
	srcMp4, err := resolveExistingSource(projectDir, ep.SourceMp4, ep, "sourceMp4")
	if err != nil {
		return err
	}
	dstMp3 := filepath.Join(outDir, ep.Pad()+".mp3")
	dstMp4 := filepath.Join(outDir, ep.Pad()+".mp4")

	// Audio: .wav -> 128 kbps CBR .mp3
	if rebuild, err := media.NeedsRebuild(srcWav, dstMp3, force); err != nil {
		return err
	} else if rebuild {
		fmt.Fprintf(w, "  audio  %s -> %s\n", displayPath(projectDir, srcWav), displayPath(projectDir, dstMp3))
		if err := media.TranscodeWAVtoMP3(srcWav, dstMp3); err != nil {
			return fmt.Errorf("episode %d: transcoding audio: %w", ep.Number, err)
		}
	} else {
		fmt.Fprintf(w, "  audio  %s  skip (up to date)\n", displayPath(projectDir, dstMp3))
	}

	// Video: source .mp4 -> faststart .mp4 (remux if H.264/AAC, else re-encode)
	if rebuild, err := media.NeedsRebuild(srcMp4, dstMp4, force); err != nil {
		return err
	} else if rebuild {
		fmt.Fprintf(w, "  video  %s -> %s\n", displayPath(projectDir, srcMp4), displayPath(projectDir, dstMp4))
		if err := media.TranscodeMP4Faststart(srcMp4, dstMp4); err != nil {
			return fmt.Errorf("episode %d: transcoding video: %w", ep.Number, err)
		}
	} else {
		fmt.Fprintf(w, "  video  %s  skip (up to date)\n", displayPath(projectDir, dstMp4))
	}
	return nil
}

// resolveExistingSource turns a sourceWav/sourceMp4 value (relative to the
// project dir unless absolute) into an absolute path, verifying the file exists
// and is a regular file. field is the YAML key name, used in error messages.
func resolveExistingSource(projectDir, rel string, ep config.Episode, field string) (string, error) {
	p := rel
	if filepath.IsAbs(p) {
		p = filepath.Clean(p)
	} else {
		p = filepath.Join(projectDir, filepath.FromSlash(rel))
	}
	fi, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s: %s %q: no such file (looked at %s)",
				filepath.Base(ep.SourceFile), field, rel, p)
		}
		return "", fmt.Errorf("%s: %s %q: %w", filepath.Base(ep.SourceFile), field, rel, err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s: %s %q is not a regular file (%s)",
			filepath.Base(ep.SourceFile), field, rel, p)
	}
	return p, nil
}

// printMediaSummary prints a table of each episode's transcoded MP3/MP4 size
// and duration. It probes every listed episode, not just the ones rebuilt this
// run.
func printMediaSummary(w io.Writer, outDir string, episodes []config.Episode) {
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "EPISODE\tMP3\tDURATION\tMP4\tDURATION")
	for _, ep := range episodes {
		mp3Size, mp3Dur := fileSizeAndDuration(filepath.Join(outDir, ep.Pad()+".mp3"))
		mp4Size, mp4Dur := fileSizeAndDuration(filepath.Join(outDir, ep.Pad()+".mp4"))
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", ep.Number, mp3Size, mp3Dur, mp4Size, mp4Dur)
	}
	_ = tw.Flush()
}

// fileSizeAndDuration returns a human-readable size and an H:MM:SS duration for
// a transcoded media file, using placeholders if it is missing or unprobable.
func fileSizeAndDuration(path string) (size, duration string) {
	fi, err := os.Stat(path)
	if err != nil {
		return "(missing)", "—"
	}
	size = humanBytes(fi.Size())
	secs, err := media.ProbeDurationSeconds(path)
	if err != nil {
		return size, "?"
	}
	return size, durationHMS(secs)
}

// displayPath renders p relative to base when p is inside base, otherwise
// returns p unchanged.
func displayPath(base, p string) string {
	if rel, err := filepath.Rel(base, p); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return p
}

// humanBytes formats a byte count with binary-magnitude units (1 KB = 1024 B).
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// durationHMS formats a whole-second duration as H:MM:SS.
func durationHMS(secs int) string {
	if secs < 0 {
		secs = 0
	}
	h := secs / 3600
	m := (secs / 60) % 60
	s := secs % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}
