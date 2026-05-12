// Package media wraps the external ffmpeg and ffprobe tools used to turn an
// episode's source recordings into the web-ready files served by the site:
//
//	source .wav  ->  media/out/NNNN.mp3   128 kbps CBR MP3 (libmp3lame), 44.1 kHz stereo
//	source .mp4  ->  media/out/NNNN.mp4   "faststart" progressive MP4 — remuxed losslessly
//	                                      if already H.264/AAC, otherwise re-encoded
//
// ffmpeg/ffprobe are invoked via os/exec; no media library is linked.
package media

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RequireTools verifies that both ffmpeg and ffprobe are on PATH. ffmpeg does
// the transcoding; ffprobe is needed for codec detection (remux vs. re-encode)
// and for duration probing. The two are normally installed together.
func RequireTools() error {
	if err := requireTool("ffmpeg"); err != nil {
		return err
	}
	return requireTool("ffprobe")
}

func requireTool(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("%s not found in PATH — install ffmpeg (ffprobe ships with it) and make sure it is on your PATH", name)
	}
	return nil
}

// ProbeDurationSeconds returns the media duration of path rounded to whole
// seconds, via `ffprobe ... -show_entries format=duration`.
func ProbeDurationSeconds(path string) (int, error) {
	out, err := runProbe("-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path)
	if err != nil {
		return 0, fmt.Errorf("probing duration of %s: %w", path, err)
	}
	out = strings.TrimSpace(out)
	if out == "" || out == "N/A" {
		return 0, fmt.Errorf("ffprobe could not determine the duration of %s", path)
	}
	secs, err := strconv.ParseFloat(out, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing duration %q reported by ffprobe for %s: %w", out, path, err)
	}
	if secs < 0 {
		secs = 0
	}
	return int(math.Round(secs)), nil
}

// ProbeIsH264AAC reports whether path's first video stream is H.264 and its
// first audio stream is AAC — i.e. it can be remuxed into a faststart MP4 with
// `-c copy` instead of being re-encoded.
func ProbeIsH264AAC(path string) (bool, error) {
	vcodec, err := runProbe("-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path)
	if err != nil {
		return false, fmt.Errorf("probing video codec of %s: %w", path, err)
	}
	acodec, err := runProbe("-v", "error", "-select_streams", "a:0",
		"-show_entries", "stream=codec_name", "-of", "csv=p=0", path)
	if err != nil {
		return false, fmt.Errorf("probing audio codec of %s: %w", path, err)
	}
	return strings.TrimSpace(vcodec) == "h264" && strings.TrimSpace(acodec) == "aac", nil
}

// TranscodeWAVtoMP3 encodes src to a 128 kbps CBR MP3 at dst (44.1 kHz stereo,
// all source metadata and any embedded cover-art stream stripped).
func TranscodeWAVtoMP3(src, dst string) error {
	return runFFmpegToFile(dst,
		"-y", "-i", src,
		"-vn",
		"-c:a", "libmp3lame", "-b:a", "128k", "-ar", "44100", "-ac", "2",
		"-map_metadata", "-1",
		"-id3v2_version", "3", "-write_id3v1", "1",
	)
}

// TranscodeMP4Faststart writes a web-optimized progressive ("faststart") MP4 at
// dst. If src is already H.264/AAC it is remuxed losslessly (`-c copy`),
// otherwise it is re-encoded to H.264 high / yuv420p / AAC for broad browser,
// Safari, and QuickTime compatibility. Either way `-movflags +faststart` moves
// the moov atom to the front of the file so playback and seeking can begin
// before the whole file has downloaded.
func TranscodeMP4Faststart(src, dst string) error {
	remuxable, err := ProbeIsH264AAC(src)
	if err != nil {
		return err
	}
	if remuxable {
		return runFFmpegToFile(dst,
			"-y", "-i", src,
			"-c", "copy",
			"-map_metadata", "-1",
			"-movflags", "+faststart",
		)
	}
	return runFFmpegToFile(dst,
		"-y", "-i", src,
		"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-preset", "slow", "-crf", "21",
		"-c:a", "aac", "-b:a", "160k", "-ar", "48000", "-ac", "2",
		"-map_metadata", "-1",
		"-movflags", "+faststart",
	)
}

// NeedsRebuild reports whether dst must be (re)generated from src: always true
// when force is set or dst does not exist, otherwise true only when src is
// newer than dst. Heads-up: a fresh `git clone` resets file mtimes to the
// checkout time, so after a clone this can report "up to date" for outputs
// whose sources actually changed — pass force in that case.
func NeedsRebuild(src, dst string, force bool) (bool, error) {
	if force {
		return true, nil
	}
	dstInfo, err := os.Stat(dst)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, fmt.Errorf("stat %s: %w", dst, err)
	}
	srcInfo, err := os.Stat(src)
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", src, err)
	}
	return srcInfo.ModTime().After(dstInfo.ModTime()), nil
}

// runFFmpegToFile runs ffmpeg to produce dst atomically: ffmpeg writes to a
// hidden temporary file in dst's directory and the result is renamed into place
// only after ffmpeg exits successfully. So an interrupted, failed, or
// disk-full transcode never leaves a partial output behind — which matters
// because NeedsRebuild only compares mtimes, and a fresh-but-corrupt file would
// otherwise be mistaken for up to date. argsBeforeDst is the ffmpeg argv up to
// (but not including) the output path; the temp path is appended as the output.
func runFFmpegToFile(dst string, argsBeforeDst ...string) error {
	base := filepath.Base(dst)
	ext := filepath.Ext(base)
	// Keep dst's extension on the temp name so ffmpeg selects the right muxer.
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+strings.TrimSuffix(base, ext)+".tmp-*"+ext)
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", dst, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed; removes a leftover/partial temp file otherwise
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("preparing temp file %s: %w", tmpPath, err)
	}

	if err := runFFmpeg(append(argsBeforeDst, tmpPath)...); err != nil {
		return err
	}
	// os.CreateTemp makes the file 0600; match a normal ffmpeg output (best effort).
	_ = os.Chmod(tmpPath, 0o644)
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("finalizing %s: %w", dst, err)
	}
	return nil
}

// runFFmpeg runs ffmpeg with args, streaming its progress and diagnostics
// straight to this process's stderr so a long re-encode shows live feedback;
// ffmpeg's own error output is therefore the actionable message on failure.
func runFFmpeg(args ...string) error {
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w (see ffmpeg output above)", err)
	}
	return nil
}

// runProbe runs ffprobe with args and returns its trimmed stdout. ffprobe is a
// quick metadata query, so its stderr is captured and surfaced in the error
// rather than printed.
func runProbe(args ...string) (string, error) {
	cmd := exec.Command("ffprobe", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("ffprobe: %w: %s", err, msg)
		}
		return "", fmt.Errorf("ffprobe: %w", err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
