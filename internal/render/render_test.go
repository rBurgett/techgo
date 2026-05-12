package render

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// repoRoot is the project directory relative to this package's directory; it has
// the templates/ dir the renderer parses.
const repoRoot = "../.."

func testSite() *config.Site {
	return &config.Site{
		Title:       "Tech.Go",
		Tagline:     "A tech podcast by Isaac and his dad",
		Description: "A nerdy, tech-focused podcast.",
		Author:      "Isaac and his dad",
		OwnerName:   "Ryan Burgett",
		OwnerEmail:  "ryan@example.com",
		BaseURL:     "https://techgo.example.com",
		Language:    "en-us",
		Copyright:   "Tech.Go",
		Category:    "Technology",
		ItunesType:  "episodic",
		Social:      map[string]string{"github": "https://github.com/rBurgett/techgo"},
	}
}

func testEpisodes() []config.Episode {
	return []config.Episode{
		{
			Number:           2,
			Title:            `Second & "Best" <ever>`,
			ShortDescription: "Episode two, with <tricky> & \"quoted\" characters.",
			Description:      "<p>Trusted <em>HTML</em> show notes.</p>",
			PubDate:          time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
			AudioDur:         65,
			VideoDur:         65,
			MP3Size:          1024 * 1024,
		},
		{
			Number:           1,
			Title:            "Hello, Tech.Go",
			Slug:             "hello-techgo",
			ShortDescription: "The first one.",
			Description:      "<p>Welcome.</p>",
			PubDate:          time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			AudioDur:         3661,
		},
	}
}

func mustNew(t *testing.T) *Renderer {
	t.Helper()
	r, err := New(repoRoot, testSite())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return r
}

func TestRenderIndex(t *testing.T) {
	var buf bytes.Buffer
	if err := mustNew(t).RenderIndex(&buf, testEpisodes()); err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"<!doctype html>",
		`<html lang="en-us">`,
		"<title>Tech.Go</title>",
		`<link rel="canonical" href="https://techgo.example.com/">`,
		`<meta property="og:type" content="website">`,
		`<link rel="alternate" type="application/rss+xml" title="Tech.Go podcast feed" href="https://techgo.example.com/feed.rss">`,
		`href="/episodes/0002.html"`,         // default slug == zero-padded number
		`href="/episodes/hello-techgo.html"`, // explicit slug
		`src="/media/0002.mp3"`,
		`download>Download MP3</a>`,
		`<span class="badge">#2</span>`,
		"Mon, 11 May 2026 12:00:00 ", // rfc2822 (html/template renders the "+0000" offset as "&#43;0000")
		"0:01:05",                    // durationHMS(65)
		"1:01:01",                    // durationHMS(3661)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("index output missing %q", want)
		}
	}
	// The episode title has & < > " — they must be HTML-escaped on the page.
	if strings.Contains(out, `Second & "Best" <ever>`) {
		t.Error("episode title was not HTML-escaped on the index page")
	}
	if !strings.Contains(out, "Second &amp; &#34;Best&#34; &lt;ever&gt;") {
		t.Errorf("escaped episode title not found in index output:\n%s", out)
	}
}

func TestRenderIndexJSONLD(t *testing.T) {
	var buf bytes.Buffer
	if err := mustNew(t).RenderIndex(&buf, testEpisodes()); err != nil {
		t.Fatalf("RenderIndex: %v", err)
	}
	obj := extractJSONLD(t, buf.String())
	if obj["@type"] != "PodcastSeries" {
		t.Errorf("JSON-LD @type = %v, want PodcastSeries", obj["@type"])
	}
	if obj["url"] != "https://techgo.example.com" {
		t.Errorf("JSON-LD url = %v, want the base URL", obj["url"])
	}
	if obj["webFeed"] != "https://techgo.example.com/feed.rss" {
		t.Errorf("JSON-LD webFeed = %v", obj["webFeed"])
	}
}

func TestRenderEpisode(t *testing.T) {
	eps := testEpisodes()
	var buf bytes.Buffer
	// Render episode #2 with #1 as the adjacent (older) episode.
	if err := mustNew(t).RenderEpisode(&buf, &eps[0], &eps[1], nil); err != nil {
		t.Fatalf("RenderEpisode: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`<meta property="og:type" content="article">`,
		`<meta property="article:published_time" content="2026-05-11T12:00:00Z">`,
		`<meta property="article:author" content="Isaac and his dad">`,
		`<link rel="canonical" href="https://techgo.example.com/episodes/0002.html">`,
		`<audio controls preload="none" src="/media/0002.mp3"></audio>`,
		`<video controls preload="metadata" src="/media/0002.mp4"></video>`,
		`<div class="show-notes"><p>Trusted <em>HTML</em> show notes.</p></div>`, // trusted, not escaped
		`href="/episodes/hello-techgo.html"`,                                     // prev link
	} {
		if !strings.Contains(out, want) {
			t.Errorf("episode output missing %q", want)
		}
	}
	obj := extractJSONLD(t, out)
	if obj["@type"] != "PodcastEpisode" {
		t.Errorf("JSON-LD @type = %v, want PodcastEpisode", obj["@type"])
	}
	if obj["duration"] != "PT1M5S" {
		t.Errorf("JSON-LD duration = %v, want PT1M5S", obj["duration"])
	}
	media, _ := obj["associatedMedia"].(map[string]any)
	if media["contentUrl"] != "https://techgo.example.com/media/0002.mp3" {
		t.Errorf("JSON-LD associatedMedia.contentUrl = %v", media["contentUrl"])
	}
}

func TestRenderEpisodeNoAdjacent(t *testing.T) {
	eps := testEpisodes()
	var buf bytes.Buffer
	if err := mustNew(t).RenderEpisode(&buf, &eps[1], nil, nil); err != nil {
		t.Fatalf("RenderEpisode: %v", err)
	}
	if strings.Contains(buf.String(), `class="episode-nav"`) {
		t.Error("episode page rendered a prev/next nav with no adjacent episodes")
	}
}

func TestRenderAboutAnd404(t *testing.T) {
	r := mustNew(t)

	var about bytes.Buffer
	if err := r.RenderAbout(&about); err != nil {
		t.Fatalf("RenderAbout: %v", err)
	}
	for _, want := range []string{
		"<title>About — Tech.Go</title>",
		`<link rel="canonical" href="https://techgo.example.com/about.html">`,
		`<meta property="og:type" content="website">`,
		`href="mailto:ryan@example.com"`,
		`href="https://github.com/rBurgett/techgo"`,
	} {
		if !strings.Contains(about.String(), want) {
			t.Errorf("about output missing %q", want)
		}
	}

	var nf bytes.Buffer
	if err := r.Render404(&nf); err != nil {
		t.Fatalf("Render404: %v", err)
	}
	for _, want := range []string{
		"<title>Not found — Tech.Go</title>",
		`<link rel="canonical" href="https://techgo.example.com/404.html">`,
		"404 — Not found",
	} {
		if !strings.Contains(nf.String(), want) {
			t.Errorf("404 output missing %q", want)
		}
	}
}

func TestNewMissingTemplatesDir(t *testing.T) {
	if _, err := New(t.TempDir(), testSite()); err == nil {
		t.Fatal("New on a project dir with no templates/ should fail")
	}
}

// extractJSONLD pulls the contents of the page's
// <script type="application/ld+json"> element and parses it as JSON, failing
// the test if the tag is missing or the content is not valid JSON (which would
// mean html/template mangled it).
func extractJSONLD(t *testing.T, html string) map[string]any {
	t.Helper()
	const open = `<script type="application/ld+json">`
	i := strings.Index(html, open)
	if i < 0 {
		t.Fatalf("no JSON-LD script tag in output:\n%s", html)
	}
	rest := html[i+len(open):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		t.Fatal("unterminated JSON-LD script tag")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(rest[:j]), &obj); err != nil {
		t.Fatalf("JSON-LD is not valid JSON: %v\ncontent: %q", err, rest[:j])
	}
	return obj
}
