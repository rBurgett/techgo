package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rBurgett/techgo/internal/config"
)

// runNewCmd invokes `techgo new` against projectDir via the real cobra command,
// returning whatever it wrote to stdout/stderr and the command error.
func runNewCmd(t *testing.T, projectDir string, extraArgs ...string) (string, error) {
	t.Helper()
	t.Cleanup(func() {
		projectFlag = "."
		outputFlag = "public"
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
	})
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"new", "--project", projectDir}, extraArgs...))
	err := rootCmd.Execute()
	return buf.String(), err
}

func writeEpisodeYAML(t *testing.T, dataDir, fileName string, number int) {
	t.Helper()
	pad := fmt.Sprintf("%04d", number)
	body := fmt.Sprintf("number: %d\ntitle: %q\npubDate: 2026-05-11T12:00:00Z\n"+
		"sourceWav: \"media/src/%[3]s.wav\"\nsourceMp4: \"media/src/%[3]s.mp4\"\n"+
		"description: |\n  <p>notes</p>\n", number, "Episode "+pad, pad)
	if err := os.WriteFile(filepath.Join(dataDir, fileName), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", fileName, err)
	}
}

func TestNewCommand(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, config.EpisodesDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEpisodeYAML(t, dataDir, "0001.yml", 1)

	// With a title arg → next file is 0002.yml, titled "Pilot", round-trips through LoadEpisodes.
	out, err := runNewCmd(t, dir, "Pilot")
	if err != nil {
		t.Fatalf("techgo new \"Pilot\": %v", err)
	}
	target := filepath.Join(dataDir, "0002.yml")
	if !strings.Contains(out, target) {
		t.Errorf("output %q does not mention created path %q", strings.TrimSpace(out), target)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected %s to exist: %v", target, err)
	}
	eps, err := config.LoadEpisodes(dir)
	if err != nil {
		t.Fatalf("LoadEpisodes after new: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d episodes, want 2", len(eps))
	}
	if eps[0].Number != 2 || eps[0].Title != "Pilot" {
		t.Errorf("new episode = #%d %q, want #2 %q", eps[0].Number, eps[0].Title, "Pilot")
	}
	if got := eps[0].EffectiveGUID(); got != "techgo-0002" {
		t.Errorf("new episode GUID = %q, want techgo-0002", got)
	}
	if got := eps[0].EffectiveSlug(); got != "0002" {
		t.Errorf("new episode slug = %q, want 0002", got)
	}
	if eps[0].PubDate.IsZero() {
		t.Error("new episode pubDate is zero")
	}

	// Without a title arg → next file is 0003.yml with a non-empty placeholder title.
	if _, err := runNewCmd(t, dir); err != nil {
		t.Fatalf("techgo new (no title): %v", err)
	}
	eps, err = config.LoadEpisodes(dir)
	if err != nil {
		t.Fatalf("LoadEpisodes after second new: %v", err)
	}
	if len(eps) != 3 || eps[0].Number != 3 {
		t.Fatalf("got %d episodes (newest #%d), want 3 (newest #3)", len(eps), eps[0].Number)
	}
	if strings.TrimSpace(eps[0].Title) == "" {
		t.Error("placeholder episode title is empty")
	}
}

// TestNewRefusesExistingTarget exercises the O_EXCL guard with a layout where the
// next episode file name already exists on disk.
func TestNewRefusesExistingTarget(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, config.EpisodesDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Episodes 1 and 2 live in files whose names are 0002.yml and 0003.yml, so the
	// computed "next" is 3 and the target file name (0003.yml) is already taken.
	writeEpisodeYAML(t, dataDir, "0002.yml", 1)
	writeEpisodeYAML(t, dataDir, "0003.yml", 2)
	if _, err := runNewCmd(t, dir, "Nope"); err == nil {
		t.Fatal("expected techgo new to refuse overwriting an existing data/0003.yml")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention the file already exists", err)
	}
}
