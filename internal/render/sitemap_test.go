package render

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestWriteSitemap(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSitemap(&buf, testSite(), testEpisodes()); err != nil {
		t.Fatalf("WriteSitemap: %v", err)
	}
	out := buf.String()

	var sm struct {
		XMLName xml.Name `xml:"urlset"`
		XMLNS   string   `xml:"xmlns,attr"`
		URLs    []struct {
			Loc     string `xml:"loc"`
			LastMod string `xml:"lastmod"`
		} `xml:"url"`
	}
	if err := xml.Unmarshal(buf.Bytes(), &sm); err != nil {
		t.Fatalf("sitemap is not well-formed XML: %v\n%s", err, out)
	}
	if sm.XMLNS != "http://www.sitemaps.org/schemas/sitemap/0.9" {
		t.Errorf("urlset xmlns = %q", sm.XMLNS)
	}
	if !strings.HasPrefix(out, xml.Header) {
		t.Errorf("sitemap does not start with the XML declaration: %.40q", out)
	}

	lastmod := map[string]string{}
	for _, u := range sm.URLs {
		if _, dup := lastmod[u.Loc]; dup {
			t.Errorf("duplicate <loc> %q", u.Loc)
		}
		lastmod[u.Loc] = u.LastMod
	}
	today := time.Now().UTC().Format("2006-01-02")
	for loc, want := range map[string]string{
		"https://techgo.example.com/":                           today,        // home
		"https://techgo.example.com/about.html":                 today,        // about
		"https://techgo.example.com/episodes/0002.html":         "2026-05-11", // default slug == padded number
		"https://techgo.example.com/episodes/hello-techgo.html": "2026-05-01", // explicit slug, episode pubDate
	} {
		got, ok := lastmod[loc]
		if !ok {
			t.Errorf("sitemap missing %q", loc)
			continue
		}
		if got != want {
			t.Errorf("%s lastmod = %q, want %q", loc, got, want)
		}
	}
	// The 404 page must not be advertised to crawlers.
	if _, ok := lastmod["https://techgo.example.com/404.html"]; ok {
		t.Error("sitemap should not include 404.html")
	}
}
