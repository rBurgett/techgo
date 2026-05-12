package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/config"
	"github.com/spf13/cobra"
)

var newCmd = &cobra.Command{
	Use:   `new ["Episode Title"]`,
	Short: "Scaffold the next episode YAML file in data/",
	Long: `new creates data/NNNN.yml for the next episode — numbered one past the
highest existing episode (or 0001 if there are none) — pre-filled with a YAML
scaffold (number, title, slug, pubDate, guid, source media paths, and a show-notes
block) for you to edit. It refuses to overwrite an existing file.

If a title argument is given it is used as the episode title; otherwise a
placeholder ("Episode N") is written.

This is the only command that writes into the project directory.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runNew,
}

func init() {
	rootCmd.AddCommand(newCmd)
}

func runNew(cmd *cobra.Command, args []string) error {
	projectDir, err := ProjectDir()
	if err != nil {
		return err
	}

	episodes, err := config.LoadEpisodes(projectDir)
	if err != nil {
		return err
	}
	next := 1
	for _, ep := range episodes {
		if ep.Number+1 > next {
			next = ep.Number + 1
		}
	}
	pad := fmt.Sprintf("%04d", next)

	title := fmt.Sprintf("Episode %d", next)
	if len(args) == 1 {
		if t := strings.TrimSpace(args[0]); t != "" {
			title = t
		}
	}

	dataDir := filepath.Join(projectDir, config.EpisodesDir)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dataDir, err)
	}
	target := filepath.Join(dataDir, pad+".yml")

	// O_EXCL: never clobber an existing episode file.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists — refusing to overwrite it", target)
		}
		return fmt.Errorf("creating %s: %w", target, err)
	}
	if _, werr := f.WriteString(scaffoldYAML(next, pad, title)); werr != nil {
		f.Close()
		os.Remove(target)
		return fmt.Errorf("writing %s: %w", target, werr)
	}
	if cerr := f.Close(); cerr != nil {
		os.Remove(target)
		return fmt.Errorf("writing %s: %w", target, cerr)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "created "+target)
	return nil
}

// scaffoldYAML returns the contents of a fresh data/NNNN.yml episode file.
func scaffoldYAML(number int, pad, title string) string {
	const tmpl = `# Episode {{NUM}}. Edit the fields below, then run "techgo media" to transcode
# the source recordings and "techgo build" to render the site.

number: {{NUM}}
title: {{TITLE}}

# Page slug — lowercase letters, digits, hyphens; must be unique across episodes.
# Defaults to the zero-padded number (URL /episodes/{{PAD}}.html); change it to
# something like "hello-world" for a nicer URL. Media files stay number-keyed.
slug: "{{PAD}}"

# One-line teaser shown on the front page and used as the feed <description>.
shortDescription: ""

# RFC3339 timestamp with an explicit offset. Defaults to when this file was
# created; set it to the actual publish time before building.
pubDate: {{NOW}}

# Stable feed id — keep it fixed once the episode is published.
guid: "techgo-{{PAD}}"

# Optional — omit to inherit site.yml's explicit flag; set true/false to override.
# explicit: false

# Optional — this episode's social-share image. Site-relative path: the file
# lives at static/<image> and is served from /<image> (so "episodes/{{PAD}}.png"
# means static/episodes/{{PAD}}.png). If omitted, the site-wide og-image.png is used.
# image: "episodes/{{PAD}}.png"

# Source media, relative to the project directory. "techgo media" transcodes these
# into media/out/{{PAD}}.mp3 (128 kbps CBR) and media/out/{{PAD}}.mp4 (faststart).
sourceWav: "media/src/{{PAD}}.wav"
sourceMp4: "media/src/{{PAD}}.mp4"

# Show notes — author-written, rendered as TRUSTED HTML (and as <content:encoded>
# in the RSS feed). Keep it to safe markup.
description: |
  <p>TODO: write the show notes for this episode.</p>
`
	return strings.NewReplacer(
		"{{NUM}}", strconv.Itoa(number),
		"{{TITLE}}", strconv.Quote(title),
		"{{PAD}}", pad,
		"{{NOW}}", time.Now().UTC().Format(time.RFC3339),
	).Replace(tmpl)
}
