package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// podcastSeriesLD returns a schema.org PodcastSeries object as a JSON string,
// emitted in the home page's <script type="application/ld+json"> block.
func podcastSeriesLD(s *config.Site, eps []config.Episode) template.JS {
	obj := map[string]any{
		"@context":    "https://schema.org",
		"@type":       "PodcastSeries",
		"name":        s.Title,
		"description": s.Description,
		"url":         s.BaseURL,
		"image":       s.CoverURL(),
		"webFeed":     s.AbsURL("feed.rss"),
		"author":      person(s.Author),
		"creator":     person(s.Author),
	}
	return marshalLD(obj)
}

// podcastEpisodeLD returns a schema.org PodcastEpisode object as a JSON string,
// emitted in an episode page's <script type="application/ld+json"> block.
func podcastEpisodeLD(s *config.Site, e config.Episode) template.JS {
	obj := map[string]any{
		"@context":      "https://schema.org",
		"@type":         "PodcastEpisode",
		"name":          e.Title,
		"description":   e.ShortDescription,
		"datePublished": e.PubDate.UTC().Format(time.RFC3339),
		"url":           s.AbsURL(e.PagePath()),
		"episodeNumber": e.Number,
		"partOfSeries": map[string]any{
			"@type": "PodcastSeries",
			"name":  s.Title,
			"url":   s.BaseURL,
		},
		"associatedMedia": map[string]any{
			"@type":          "MediaObject",
			"contentUrl":     s.AbsURL(e.MP3Path()),
			"encodingFormat": "audio/mpeg",
		},
	}
	if e.AudioDur > 0 {
		obj["duration"] = iso8601Duration(e.AudioDur)
	}
	return marshalLD(obj)
}

// person wraps a name as a schema.org Person node.
func person(name string) map[string]any {
	return map[string]any{"@type": "Person", "name": name}
}

// marshalLD encodes v as compact JSON for embedding in a <script
// type="application/ld+json"> element. encoding/json sorts map keys (so the
// output is deterministic) and escapes '<', '>' and '&' to \uXXXX, so the result
// can never contain a literal "</script>" — safe to emit verbatim. On the
// (unreachable for these plain maps) marshal error it yields the empty string so
// the page still renders.
func marshalLD(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return template.JS(b)
}

// iso8601Duration formats a whole-second duration as an ISO-8601 duration,
// e.g. 3661 -> "PT1H1M1S", 65 -> "PT1M5S". Zero or negative -> "PT0S".
func iso8601Duration(secs int) string {
	if secs <= 0 {
		return "PT0S"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	var b strings.Builder
	b.WriteString("PT")
	if h > 0 {
		fmt.Fprintf(&b, "%dH", h)
	}
	if m > 0 {
		fmt.Fprintf(&b, "%dM", m)
	}
	if s > 0 {
		fmt.Fprintf(&b, "%dS", s)
	}
	return b.String()
}
