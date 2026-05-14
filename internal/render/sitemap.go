package render

import (
	"encoding/xml"
	"fmt"
	"io"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

const sitemapNS = "http://www.sitemaps.org/schemas/sitemap/0.9"

type urlSet struct {
	XMLName xml.Name     `xml:"urlset"`
	XMLNS   string       `xml:"xmlns,attr"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

// w3cDate formats t as a W3C date (YYYY-MM-DD) in UTC — the granularity sitemaps
// conventionally use for <lastmod>.
func w3cDate(t time.Time) string { return t.UTC().Format("2006-01-02") }

// WriteSitemap renders sitemap.xml: the home page, the About page, and every
// episode page (linked by EffectiveSlug, matching the generated file names).
// <lastmod> is the episode's pubDate for episode pages and the build time for
// the static pages. The 404 page is intentionally omitted. Built with
// encoding/xml so URLs containing '&' etc. are escaped correctly.
func WriteSitemap(w io.Writer, s *config.Site, episodes []config.Episode) error {
	now := w3cDate(time.Now())
	set := urlSet{XMLNS: sitemapNS}
	set.URLs = append(set.URLs,
		sitemapURL{Loc: s.AbsURL(""), LastMod: now},
		sitemapURL{Loc: s.AbsURL("about.html"), LastMod: now},
	)
	for _, e := range episodes {
		set.URLs = append(set.URLs, sitemapURL{
			Loc:     s.AbsURL(e.PagePath()),
			LastMod: w3cDate(e.PubDate),
		})
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(set); err != nil {
		return fmt.Errorf("encoding sitemap: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return nil
}
