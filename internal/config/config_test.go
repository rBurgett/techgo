package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParseDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeFile(t, path, `# a comment
AWS_ACCESS_KEY_ID=AKIAEXAMPLE

  # indented comment
AWS_SECRET_ACCESS_KEY="se/cr+et value"
export AWS_REGION = us-east-2
S3_PREFIX=
EMPTYQUOTES=''
`)
	got, err := ParseDotEnv(path)
	if err != nil {
		t.Fatalf("ParseDotEnv: %v", err)
	}
	want := map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "se/cr+et value",
		"AWS_REGION":            "us-east-2",
		"S3_PREFIX":             "",
		"EMPTYQUOTES":           "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("key %q = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(got), len(want), got)
	}

	if _, err := ParseDotEnv(filepath.Join(dir, "nope.env")); err == nil {
		t.Error("expected error for missing .env file")
	}
	bad := filepath.Join(dir, "bad.env")
	writeFile(t, bad, "NOTKEYVALUE\n")
	if _, err := ParseDotEnv(bad); err == nil {
		t.Error("expected error for malformed line")
	}
}

const validSiteYML = `
title: "Tech.Go"
tagline: "A tech podcast"
description: "Desc"
author: "Isaac and his dad"
ownerName: "Isaac and his dad"
ownerEmail: "x@example.com"
baseURL: "https://techgo.example.com/"
category: "Technology"
`

func TestLoadSite(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, SiteFileName), validSiteYML)
	s, err := LoadSite(dir)
	if err != nil {
		t.Fatalf("LoadSite: %v", err)
	}
	if s.BaseURL != "https://techgo.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash trimmed", s.BaseURL)
	}
	if s.Language != "en-us" {
		t.Errorf("Language default = %q, want en-us", s.Language)
	}
	if s.ItunesType != "episodic" {
		t.Errorf("ItunesType default = %q, want episodic", s.ItunesType)
	}
	if got, want := s.AbsURL("feed.rss"), "https://techgo.example.com/feed.rss"; got != want {
		t.Errorf("AbsURL = %q, want %q", got, want)
	}
	if got, want := s.CoverURL(), "https://techgo.example.com/cover.png"; got != want {
		t.Errorf("CoverURL = %q, want %q", got, want)
	}
	if got, want := s.OGImageURL(), "https://techgo.example.com/og-image.png"; got != want {
		t.Errorf("OGImageURL = %q, want %q", got, want)
	}

	// Missing required field.
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, SiteFileName), `title: "X"`+"\n")
	if _, err := LoadSite(dir2); err == nil {
		t.Error("expected error for missing required fields")
	}
	// Empty site.yml: io.EOF is tolerated, then validation reports missing fields.
	dEmpty := t.TempDir()
	writeFile(t, filepath.Join(dEmpty, SiteFileName), "")
	if _, err := LoadSite(dEmpty); err == nil {
		t.Error("expected error for empty site.yml")
	}

	// Bad baseURL / coverArtURL variants: each must be rejected.
	for name, body := range map[string]string{
		"non-http baseURL":        "baseURL: \"ftp://nope\"\n",
		"baseURL with query":      "baseURL: \"https://x.example.com/?a=1\"\n",
		"baseURL with frag":       "baseURL: \"https://x.example.com/#top\"\n",
		"baseURL with bare hash":  "baseURL: \"https://x.example.com/#\"\n",
		"baseURL with userinfo":   "baseURL: \"https://user:pass@x.example.com\"\n",
		"non-http coverArtURL":    "baseURL: \"https://x.example.com\"\ncoverArtURL: \"file:///tmp/cover.png\"\n",
		"coverArtURL with query":  "baseURL: \"https://x.example.com\"\ncoverArtURL: \"https://cdn.example.com/c.png?v=1\"\n",
		"coverArtURL with frag":   "baseURL: \"https://x.example.com\"\ncoverArtURL: \"https://cdn.example.com/c.png#x\"\n",
		"coverArtURL w/ userinfo": "baseURL: \"https://x.example.com\"\ncoverArtURL: \"https://u:p@cdn.example.com/c.png\"\n",
	} {
		d := t.TempDir()
		writeFile(t, filepath.Join(d, SiteFileName),
			"title: T\nauthor: A\nownerEmail: e@x.com\ncategory: Technology\n"+body)
		if _, err := LoadSite(d); err == nil {
			t.Errorf("%s: expected LoadSite to fail", name)
		}
	}
	// Whitespace around baseURL / coverArtURL is trimmed, not rejected.
	d := t.TempDir()
	writeFile(t, filepath.Join(d, SiteFileName),
		"title: T\nauthor: A\nownerEmail: e@x.com\ncategory: Technology\n"+
			"baseURL: \"  https://x.example.com/  \"\ncoverArtURL: \"  https://cdn.example.com/c.png  \"\n")
	st, err := LoadSite(d)
	if err != nil {
		t.Fatalf("LoadSite with padded URLs: %v", err)
	}
	if st.BaseURL != "https://x.example.com" {
		t.Errorf("BaseURL = %q, want trimmed", st.BaseURL)
	}
	if st.CoverArtURL != "https://cdn.example.com/c.png" {
		t.Errorf("CoverArtURL = %q, want trimmed", st.CoverArtURL)
	}
}

func TestEpisodeImageValidation(t *testing.T) {
	mkEp := func(image string) string {
		return "number: 1\ntitle: T\npubDate: 2026-01-01T00:00:00Z\n" +
			"sourceWav: a.wav\nsourceMp4: a.mp4\nimage: \"" + image + "\"\n"
	}

	// Valid: file exists under static/.
	good := t.TempDir()
	writeFile(t, filepath.Join(good, "static", "episodes", "0001.png"), "fakepng")
	writeFile(t, filepath.Join(good, EpisodesDir, "0001.yml"), mkEp("episodes/0001.png"))
	eps, err := LoadEpisodes(good)
	if err != nil {
		t.Fatalf("LoadEpisodes (valid image): %v", err)
	}
	if got, want := eps[0].Image, "episodes/0001.png"; got != want {
		t.Errorf("normalized Image = %q, want %q", got, want)
	}
	if got, want := eps[0].SocialImageURL(&Site{BaseURL: "https://x.example.com"}), "https://x.example.com/episodes/0001.png"; got != want {
		t.Errorf("SocialImageURL = %q, want %q", got, want)
	}

	for name, image := range map[string]string{
		"missing file":   "episodes/missing.png",
		"escapes static": "../secret.png",
		"absolute path":  "/etc/passwd",
	} {
		d := t.TempDir()
		writeFile(t, filepath.Join(d, EpisodesDir, "0001.yml"), mkEp(image))
		if _, err := LoadEpisodes(d); err == nil {
			t.Errorf("%s: expected LoadEpisodes to fail for image %q", name, image)
		}
	}
}

func TestLoadEpisodes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, EpisodesDir, "0001.yml"), `
number: 1
title: "First"
shortDescription: "first ep"
pubDate: 2026-01-01T00:00:00Z
sourceWav: "media/src/0001.wav"
sourceMp4: "media/src/0001.mp4"
description: "<p>one</p>"
`)
	writeFile(t, filepath.Join(dir, EpisodesDir, "two.yml"), `
number: 2
title: "Second"
slug: "second-episode"
shortDescription: "second ep"
pubDate: 2026-02-01T00:00:00Z
explicit: true
sourceWav: "media/src/0002.wav"
sourceMp4: "media/src/0002.mp4"
description: "<p>two</p>"
`)
	eps, err := LoadEpisodes(dir)
	if err != nil {
		t.Fatalf("LoadEpisodes: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d episodes, want 2", len(eps))
	}
	if eps[0].Number != 2 || eps[1].Number != 1 {
		t.Errorf("episodes not sorted newest-first: %d, %d", eps[0].Number, eps[1].Number)
	}
	if got, want := eps[1].EffectiveSlug(), "0001"; got != want {
		t.Errorf("ep1 EffectiveSlug = %q, want %q", got, want)
	}
	if got, want := eps[0].EffectiveSlug(), "second-episode"; got != want {
		t.Errorf("ep2 EffectiveSlug = %q, want %q", got, want)
	}
	if got, want := eps[1].PagePath(), "episodes/0001.html"; got != want {
		t.Errorf("ep1 PagePath = %q, want %q", got, want)
	}
	if got, want := eps[0].PagePath(), "episodes/second-episode.html"; got != want {
		t.Errorf("ep2 PagePath = %q, want %q", got, want)
	}
	if got, want := eps[1].MP3Path(), "media/0001.mp3"; got != want {
		t.Errorf("ep1 MP3Path = %q, want %q", got, want)
	}
	if got, want := eps[1].EffectiveGUID(), "techgo-0001"; got != want {
		t.Errorf("ep1 EffectiveGUID = %q, want %q", got, want)
	}

	site := &Site{Explicit: false, BaseURL: "https://x.example.com"}
	if eps[1].EffectiveExplicit(site) != false {
		t.Error("ep1 should inherit site explicit=false")
	}
	if eps[0].EffectiveExplicit(site) != true {
		t.Error("ep2 should override to explicit=true")
	}
	site.Explicit = true
	if eps[1].EffectiveExplicit(site) != true {
		t.Error("ep1 should inherit site explicit=true when not overridden")
	}
}

func TestLoadEpisodesDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, EpisodesDir, "a.yml"), `
number: 1
title: "A"
pubDate: 2026-01-01T00:00:00Z
sourceWav: "a.wav"
sourceMp4: "a.mp4"
`)
	writeFile(t, filepath.Join(dir, EpisodesDir, "b.yml"), `
number: 1
title: "B"
pubDate: 2026-01-02T00:00:00Z
sourceWav: "b.wav"
sourceMp4: "b.mp4"
`)
	if _, err := LoadEpisodes(dir); err == nil {
		t.Error("expected error for duplicate episode number")
	}

	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, EpisodesDir, "a.yml"), `
number: 1
title: "A"
slug: "0002"
pubDate: 2026-01-01T00:00:00Z
sourceWav: "a.wav"
sourceMp4: "a.mp4"
`)
	writeFile(t, filepath.Join(dir2, EpisodesDir, "b.yml"), `
number: 2
title: "B"
pubDate: 2026-01-02T00:00:00Z
sourceWav: "b.wav"
sourceMp4: "b.mp4"
`)
	if _, err := LoadEpisodes(dir2); err == nil {
		t.Error("expected error for slug colliding with another episode's default slug")
	}
}
