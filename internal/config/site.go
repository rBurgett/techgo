package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// SiteFileName is the name of the site metadata file, expected at the project root.
const SiteFileName = "site.yml"

// Site holds podcast/site metadata loaded from site.yml.
type Site struct {
	Title         string            `yaml:"title"`         // e.g. "Tech.Go"
	Tagline       string            `yaml:"tagline"`       // short subtitle for the home page header
	Description   string            `yaml:"description"`   // longer blurb; channel <description> and meta description
	Author        string            `yaml:"author"`        // <itunes:author>, e.g. "Isaac and his dad"
	OwnerName     string            `yaml:"ownerName"`     // <itunes:owner><itunes:name>
	OwnerEmail    string            `yaml:"ownerEmail"`    // <itunes:owner><itunes:email> (Apple requires this)
	BaseURL       string            `yaml:"baseURL"`       // absolute site URL, no trailing slash (normalized on load)
	Language      string            `yaml:"language"`      // RFC 5646 / RSS language code, e.g. "en-us"
	Copyright     string            `yaml:"copyright"`     // <copyright> and footer text
	Explicit      bool              `yaml:"explicit"`      // default explicitness; episodes may override
	Category      string            `yaml:"category"`      // exact Apple <itunes:category text="...">, e.g. "Technology"
	Subcategory   string            `yaml:"subcategory"`   // optional nested Apple subcategory
	ItunesType    string            `yaml:"itunesType"`    // "episodic" (default) or "serial"
	CoverArt      string            `yaml:"coverArt"`      // path (relative to project dir) to the source PNG for `techgo images`
	CoverArtURL   string            `yaml:"coverArtURL"`   // optional absolute override for the cover-art URL; else BaseURL + "/cover.png"
	TwitterHandle string            `yaml:"twitterHandle"` // optional, e.g. "@techgo" -> twitter:site meta tag
	Social        map[string]string `yaml:"social"`        // optional named links, e.g. {github: "https://..."}
}

// LoadSite reads and validates <projectDir>/site.yml.
func LoadSite(projectDir string) (*Site, error) {
	path := filepath.Join(projectDir, SiteFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s at %s", SiteFileName, path)
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var s Site
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if err := s.normalizeAndValidate(); err != nil {
		return nil, fmt.Errorf("%s: %w", SiteFileName, err)
	}
	return &s, nil
}

func (s *Site) normalizeAndValidate() error {
	missing := func(name, v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", name)
		}
		return nil
	}
	for _, e := range []error{
		missing("title", s.Title),
		missing("author", s.Author),
		missing("ownerEmail", s.OwnerEmail),
		missing("category", s.Category),
	} {
		if e != nil {
			return e
		}
	}

	if s.Language == "" {
		s.Language = "en-us"
	}
	if s.ItunesType == "" {
		s.ItunesType = "episodic"
	}
	if s.ItunesType != "episodic" && s.ItunesType != "serial" {
		return fmt.Errorf("itunesType must be \"episodic\" or \"serial\", got %q", s.ItunesType)
	}

	base := strings.TrimSpace(s.BaseURL)
	if base == "" {
		return fmt.Errorf("baseURL is required")
	}
	u, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("baseURL is not a valid URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("baseURL must be an absolute http(s) URL, got %q", s.BaseURL)
	}
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("baseURL must not contain a query string, fragment, or userinfo, got %q", s.BaseURL)
	}
	// Normalize: keep scheme://host[/path] and trim a trailing slash from the path.
	s.BaseURL = u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")

	if s.CoverArtURL != "" {
		cv := strings.TrimSpace(s.CoverArtURL)
		cu, err := url.Parse(cv)
		if err != nil || (cu.Scheme != "http" && cu.Scheme != "https") || cu.Host == "" {
			return fmt.Errorf("coverArtURL must be an absolute http(s) URL, got %q", s.CoverArtURL)
		}
		s.CoverArtURL = cv
	}
	return nil
}

// AbsURL joins a site-relative path onto BaseURL, producing an absolute URL.
func (s *Site) AbsURL(p string) string {
	return strings.TrimRight(s.BaseURL, "/") + "/" + strings.TrimLeft(p, "/")
}

// CoverURL is the absolute URL of the square podcast cover art (used for the RSS
// <itunes:image> and JSON-LD image). It is CoverArtURL if set, else BaseURL+"/cover.png".
func (s *Site) CoverURL() string {
	if s.CoverArtURL != "" {
		return s.CoverArtURL
	}
	return s.AbsURL("cover.png")
}

// OGImageURL is the absolute URL of the 1200x630 social card image
// (used for og:image / twitter:image).
func (s *Site) OGImageURL() string {
	return s.AbsURL("og-image.png")
}
