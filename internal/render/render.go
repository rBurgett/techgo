// Package render turns the loaded site + episode data into the static HTML pages
// of the Tech.Go website using html/template. The RSS feed and sitemap.xml are
// not templates — they are produced from encoding/xml structs in feed.go and
// sitemap.go, because text/template does not escape XML and a stray '&' or '<'
// in a title would corrupt the feed.
package render

import (
	"fmt"
	"html/template"
	"io"
	"path/filepath"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// TemplatesDir is the project subdirectory holding the *.html.tmpl files.
const TemplatesDir = "templates"

// baseTemplate is the layout every page is rendered through; the page templates
// fill its {{block "content" .}}.
const baseTemplate = "base.html.tmpl"

// pageTemplates are the per-page content templates parsed on top of baseTemplate.
var pageTemplates = []string{
	"index.html.tmpl",
	"episode.html.tmpl",
	"about.html.tmpl",
	"404.html.tmpl",
}

// Page is the view model every template receives. The shared <head> in
// base.html.tmpl reads the metadata fields to emit page-specific canonical /
// Open Graph / Twitter / JSON-LD tags; the page-specific payload fields below
// carry the data each content template needs.
type Page struct {
	Site         *config.Site
	Title        string      // <title> + og:title + twitter:title
	Description  string      // <meta name=description> + og/twitter description (plain text)
	Canonical    string      // absolute canonical URL of this page
	OGType       string      // "website" (home/about/404) or "article" (episode page)
	ImageURL     string      // absolute URL of this page's social-share image
	PublishedISO string      // RFC3339; set only on episode pages -> article:published_time
	JSONLD       template.JS // pre-rendered JSON-LD object (valid JSON), or "" for none

	Episodes []config.Episode // index page: every episode, newest first
	Episode  *config.Episode  // episode page: the episode being rendered
	Prev     *config.Episode  // episode page: adjacent episode (may be nil)
	Next     *config.Episode  // episode page: adjacent episode (may be nil)
}

// Renderer holds the parsed templates for one site. It is built once and is safe
// to reuse for many pages: each page has its own pre-cloned template set, so
// rendering does not mutate shared state.
type Renderer struct {
	site  *config.Site
	pages map[string]*template.Template
}

// New parses <projectDir>/templates/*.html.tmpl. The layout (base.html.tmpl) is
// parsed once with the site FuncMap, then a clone of it is paired with each page
// template — html/template names templates globally within a set, so the page
// templates' {{define "content"}} blocks would otherwise collide.
func New(projectDir string, site *config.Site) (*Renderer, error) {
	dir := filepath.Join(projectDir, TemplatesDir)
	basePath := filepath.Join(dir, baseTemplate)
	base, err := template.New(baseTemplate).Funcs(funcMap(site)).ParseFiles(basePath)
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", basePath, err)
	}
	pages := make(map[string]*template.Template, len(pageTemplates))
	for _, name := range pageTemplates {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning base template: %w", err)
		}
		path := filepath.Join(dir, name)
		if _, err := clone.ParseFiles(path); err != nil {
			return nil, fmt.Errorf("loading %s: %w", path, err)
		}
		pages[name] = clone
	}
	return &Renderer{site: site, pages: pages}, nil
}

func (r *Renderer) render(w io.Writer, pageTemplate string, data *Page) error {
	t, ok := r.pages[pageTemplate]
	if !ok {
		return fmt.Errorf("internal: no page template %q", pageTemplate)
	}
	if err := t.ExecuteTemplate(w, baseTemplate, data); err != nil {
		return fmt.Errorf("rendering %s: %w", pageTemplate, err)
	}
	return nil
}

// RenderIndex writes the home page: the episode list (newest first) plus inline
// audio players, with PodcastSeries JSON-LD.
func (r *Renderer) RenderIndex(w io.Writer, eps []config.Episode) error {
	s := r.site
	return r.render(w, "index.html.tmpl", &Page{
		Site:        s,
		Title:       s.Title,
		Description: s.Description,
		Canonical:   s.AbsURL(""), // BaseURL + "/"
		OGType:      "website",
		ImageURL:    s.OGImageURL(),
		JSONLD:      podcastSeriesLD(s, eps),
		Episodes:    eps,
	})
}

// RenderEpisode writes one episode page (full show notes, download link, video),
// with PodcastEpisode JSON-LD and og:type=article. prev/next may be nil.
func (r *Renderer) RenderEpisode(w io.Writer, ep, prev, next *config.Episode) error {
	s := r.site
	return r.render(w, "episode.html.tmpl", &Page{
		Site:         s,
		Title:        ep.Title + " — " + s.Title,
		Description:  ep.ShortDescription,
		Canonical:    s.AbsURL(ep.PagePath()),
		OGType:       "article",
		ImageURL:     ep.SocialImageURL(s),
		PublishedISO: ep.PubDate.UTC().Format(time.RFC3339),
		JSONLD:       podcastEpisodeLD(s, *ep),
		Episode:      ep,
		Prev:         prev,
		Next:         next,
	})
}

// RenderAbout writes the About page.
func (r *Renderer) RenderAbout(w io.Writer) error {
	s := r.site
	return r.render(w, "about.html.tmpl", &Page{
		Site:        s,
		Title:       "About — " + s.Title,
		Description: "About " + s.Title + " — " + s.Description,
		Canonical:   s.AbsURL("about.html"),
		OGType:      "website",
		ImageURL:    s.OGImageURL(),
	})
}

// Render404 writes the 404 page (served by CloudFront for missing keys).
func (r *Renderer) Render404(w io.Writer) error {
	s := r.site
	return r.render(w, "404.html.tmpl", &Page{
		Site:        s,
		Title:       "Not found — " + s.Title,
		Description: "Page not found.",
		Canonical:   s.AbsURL("404.html"),
		OGType:      "website",
		ImageURL:    s.OGImageURL(),
	})
}
