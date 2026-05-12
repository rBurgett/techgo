package config

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// EpisodesDir is the project subdirectory holding per-episode YAML files.
const EpisodesDir = "data"

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Episode holds the data for one podcast episode, loaded from data/<name>.yml.
// The file name is not significant; everything comes from the YAML.
type Episode struct {
	Number           int       `yaml:"number"`           // episode number; orders feed/index, keys media filenames
	Title            string    `yaml:"title"`            //
	Slug             string    `yaml:"slug"`             // optional; default = zero-padded Number; drives the page URL
	ShortDescription string    `yaml:"shortDescription"` // plain-text teaser (index list, feed <description>)
	Description      string    `yaml:"description"`      // show notes; rendered as TRUSTED HTML / feed <content:encoded>
	PubDate          time.Time `yaml:"pubDate"`          // RFC3339 with offset; yaml.v3 parses to time.Time
	GUID             string    `yaml:"guid"`             // optional stable id; default "techgo-<pad>"
	Explicit         *bool     `yaml:"explicit"`         // optional; nil = inherit site default
	Image            string    `yaml:"image"`            // optional; site-relative path (file lives at static/<image>), used as the social-share image
	SourceWav        string    `yaml:"sourceWav"`        // path (relative to project dir) to the source .wav
	SourceMp4        string    `yaml:"sourceMp4"`        // path (relative to project dir) to the source .mp4

	// Populated at media/build time, not from YAML.
	MP3Size  int64 `yaml:"-"`
	MP4Size  int64 `yaml:"-"`
	AudioDur int   `yaml:"-"` // seconds
	VideoDur int   `yaml:"-"` // seconds

	// SourceFile is the path of the YAML file this episode was loaded from (for error messages).
	SourceFile string `yaml:"-"`
}

// Pad returns the episode number zero-padded to four digits, e.g. "0007".
func (e Episode) Pad() string { return fmt.Sprintf("%04d", e.Number) }

// EffectiveSlug is the explicit slug if set, otherwise the zero-padded number.
func (e Episode) EffectiveSlug() string {
	if e.Slug != "" {
		return e.Slug
	}
	return e.Pad()
}

// PagePath is the site-relative path of the episode's HTML page.
func (e Episode) PagePath() string { return "episodes/" + e.EffectiveSlug() + ".html" }

// MP3Path is the site-relative path of the episode's audio file (number-keyed).
func (e Episode) MP3Path() string { return "media/" + e.Pad() + ".mp3" }

// MP4Path is the site-relative path of the episode's video file (number-keyed).
func (e Episode) MP4Path() string { return "media/" + e.Pad() + ".mp4" }

// EffectiveGUID is the explicit GUID if set, otherwise "techgo-<pad>".
func (e Episode) EffectiveGUID() string {
	if e.GUID != "" {
		return e.GUID
	}
	return "techgo-" + e.Pad()
}

// EffectiveExplicit returns the episode's explicitness, falling back to the site default.
func (e Episode) EffectiveExplicit(s *Site) bool {
	if e.Explicit != nil {
		return *e.Explicit
	}
	return s.Explicit
}

// SocialImageURL is the absolute URL of this episode's social-share image:
// the per-episode Image (a site-relative path; the file lives under static/)
// if set, otherwise the site-wide og-image.png.
func (e Episode) SocialImageURL(s *Site) string {
	if e.Image != "" {
		return s.AbsURL(e.Image)
	}
	return s.OGImageURL()
}

// LoadEpisodes reads, validates, and sorts every data/*.yml file under projectDir.
// Episodes are returned newest-first (descending by Number).
func LoadEpisodes(projectDir string) ([]Episode, error) {
	dir := filepath.Join(projectDir, EpisodesDir)
	matches, err := filepath.Glob(filepath.Join(dir, "*.yml"))
	if err != nil {
		return nil, fmt.Errorf("scanning %s: %w", dir, err)
	}
	sort.Strings(matches) // deterministic processing order for error reporting

	episodes := make([]Episode, 0, len(matches))
	for _, path := range matches {
		ep, err := loadEpisodeFile(path)
		if err != nil {
			return nil, err
		}
		episodes = append(episodes, ep)
	}

	if err := normalizeAndValidateEpisodes(projectDir, episodes); err != nil {
		return nil, err
	}

	sort.SliceStable(episodes, func(i, j int) bool { return episodes[i].Number > episodes[j].Number })
	return episodes, nil
}

func loadEpisodeFile(path string) (Episode, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Episode{}, fmt.Errorf("reading %s: %w", path, err)
	}
	var ep Episode
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&ep); err != nil {
		return Episode{}, fmt.Errorf("parsing %s: %w", path, err)
	}
	ep.SourceFile = path
	return ep, nil
}

// normalizeAndValidateEpisodes validates every episode and normalizes the Image
// field in place (clean, forward-slash, used directly as a site-relative URL path).
func normalizeAndValidateEpisodes(projectDir string, episodes []Episode) error {
	byNumber := make(map[int]string)
	bySlug := make(map[string]string)
	for i := range episodes {
		ep := &episodes[i]
		name := filepath.Base(ep.SourceFile)

		if ep.Number <= 0 {
			return fmt.Errorf("%s: number must be a positive integer, got %d", name, ep.Number)
		}
		if strings.TrimSpace(ep.Title) == "" {
			return fmt.Errorf("%s: title is required", name)
		}
		if ep.PubDate.IsZero() {
			return fmt.Errorf("%s: pubDate is required (RFC3339, e.g. 2026-05-11T12:00:00Z)", name)
		}
		if strings.TrimSpace(ep.SourceWav) == "" {
			return fmt.Errorf("%s: sourceWav is required", name)
		}
		if strings.TrimSpace(ep.SourceMp4) == "" {
			return fmt.Errorf("%s: sourceMp4 is required", name)
		}
		if ep.Slug != "" && !slugRe.MatchString(ep.Slug) {
			return fmt.Errorf("%s: slug %q must match %s (lowercase letters, digits, hyphens)", name, ep.Slug, slugRe)
		}
		if ep.Image != "" {
			img := path.Clean(filepath.ToSlash(ep.Image))
			if path.IsAbs(img) || img == "." || img == ".." || strings.HasPrefix(img, "../") {
				return fmt.Errorf("%s: image %q must be a relative path inside the project's static/ dir, e.g. \"episodes/0001.png\"", name, ep.Image)
			}
			imgPath := filepath.Join(projectDir, "static", filepath.FromSlash(img))
			if _, err := os.Stat(imgPath); err != nil {
				return fmt.Errorf("%s: image %q not found at %s", name, ep.Image, imgPath)
			}
			ep.Image = img // normalized; AbsURL(ep.Image) is the social-share URL, and the file is at static/<img>
		}

		if prev, ok := byNumber[ep.Number]; ok {
			return fmt.Errorf("%s and %s both declare number %d", filepath.Base(prev), name, ep.Number)
		}
		byNumber[ep.Number] = ep.SourceFile

		slug := ep.EffectiveSlug()
		if prev, ok := bySlug[slug]; ok {
			return fmt.Errorf("%s and %s resolve to the same page slug %q", filepath.Base(prev), name, slug)
		}
		bySlug[slug] = ep.SourceFile
	}
	return nil
}
