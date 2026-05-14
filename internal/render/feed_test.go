package render

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// parsedFeed is a relaxed view of the feed, just enough to re-parse what
// WriteFeed produced. Namespaced elements (itunes:*, content:encoded, atom:link)
// are matched by local name, which encoding/xml allows. <description> arrives as
// chardata regardless of whether it was a CDATA section, so the show-notes HTML
// round-trips back to its original (unescaped) form here.
type parsedFeed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title       string `xml:"title"`
		Description string `xml:"description"`
		Language    string `xml:"language"`
		Copyright   string `xml:"copyright"`
		Author      string `xml:"author"` // itunes:author
		Explicit    string `xml:"explicit"`
		Type        string `xml:"type"`
		// The RSS <link> (chardata) and the <atom:link rel="self"> (href attr)
		// share a local name, so they both land here in document order.
		Links []struct {
			Chardata string `xml:",chardata"`
			Href     string `xml:"href,attr"`
			Rel      string `xml:"rel,attr"`
			Type     string `xml:"type,attr"`
		} `xml:"link"`
		Image struct {
			Href string `xml:"href,attr"`
		} `xml:"image"`
		Owner struct {
			Name  string `xml:"name"`
			Email string `xml:"email"`
		} `xml:"owner"`
		Category struct {
			Text string `xml:"text,attr"`
			Sub  *struct {
				Text string `xml:"text,attr"`
			} `xml:"category"`
		} `xml:"category"`
		Items []parsedItem `xml:"item"`
	} `xml:"channel"`
}

type parsedItem struct {
	Title string `xml:"title"`
	Link  string `xml:"link"`
	GUID  struct {
		IsPermaLink string `xml:"isPermaLink,attr"`
		Value       string `xml:",chardata"`
	} `xml:"guid"`
	PubDate     string `xml:"pubDate"`
	Description string `xml:"description"`
	Encoded     string `xml:"encoded"` // content:encoded
	Enclosure   struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Duration    int    `xml:"duration"` // itunes:duration
	Episode     int    `xml:"episode"`  // itunes:episode
	EpisodeType string `xml:"episodeType"`
	Explicit    string `xml:"explicit"`
}

func feedTestSite() *config.Site {
	s := testSite()
	s.Title = `Tech & "Go" <podcast>`
	s.Description = "A nerdy show with <markup> & \"quotes\" in the blurb."
	s.OwnerName = "Isaac & Dad"
	s.Subcategory = "Tech News"
	s.Copyright = "© 2026 Tech & Go"
	return s
}

func feedTestEpisodes() []config.Episode {
	yes := true
	return []config.Episode{
		{
			Number:           2,
			Title:            `Second & "Best" <ever>`,
			ShortDescription: `Episode two — <tricky> & "quoted" teaser.`,
			Description:      `<p>Notes with <em>markup</em>, an &amp; entity, and a "]]>" sequence.</p>`,
			PubDate:          time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC),
			GUID:             "techgo-0002",
			Explicit:         &yes,
			AudioDur:         3661,
			MP3Size:          7_654_321,
		},
		{
			Number:           1,
			Title:            "Hello, Tech.Go",
			Slug:             "hello-techgo",
			ShortDescription: "The first one.",
			Description:      "<p>Welcome.</p>",
			PubDate:          time.Date(2026, 5, 1, 9, 30, 0, 0, time.UTC),
			AudioDur:         65,
			MP3Size:          1_048_576,
		},
	}
}

func TestWriteFeedRoundTrip(t *testing.T) {
	site := feedTestSite()
	eps := feedTestEpisodes()

	var buf bytes.Buffer
	if err := WriteFeed(&buf, site, eps); err != nil {
		t.Fatalf("WriteFeed: %v", err)
	}
	out := buf.String()

	// The output must be well-formed XML — a successful Unmarshal proves it, and
	// proves the show-notes HTML and special characters round-trip cleanly.
	var feed parsedFeed
	if err := xml.Unmarshal(buf.Bytes(), &feed); err != nil {
		t.Fatalf("feed is not well-formed XML: %v\n%s", err, out)
	}

	if feed.Version != "2.0" {
		t.Errorf("rss version = %q, want 2.0", feed.Version)
	}
	if feed.Channel.Title != site.Title {
		t.Errorf("channel title = %q, want %q", feed.Channel.Title, site.Title)
	}
	if feed.Channel.Description != site.Description {
		t.Errorf("channel description = %q, want %q", feed.Channel.Description, site.Description)
	}
	if feed.Channel.Language != "en-us" {
		t.Errorf("channel language = %q, want en-us", feed.Channel.Language)
	}
	if feed.Channel.Copyright != site.Copyright {
		t.Errorf("channel copyright = %q, want %q", feed.Channel.Copyright, site.Copyright)
	}
	if feed.Channel.Author != site.Author {
		t.Errorf("itunes:author = %q, want %q", feed.Channel.Author, site.Author)
	}
	if feed.Channel.Explicit != "false" {
		t.Errorf("channel itunes:explicit = %q, want false", feed.Channel.Explicit)
	}
	if feed.Channel.Type != "episodic" {
		t.Errorf("channel itunes:type = %q, want episodic", feed.Channel.Type)
	}
	if feed.Channel.Owner.Name != site.OwnerName || feed.Channel.Owner.Email != site.OwnerEmail {
		t.Errorf("itunes:owner = %+v, want name=%q email=%q", feed.Channel.Owner, site.OwnerName, site.OwnerEmail)
	}
	if feed.Channel.Image.Href != "https://techgo.example.com/cover.png" {
		t.Errorf("channel itunes:image = %q", feed.Channel.Image.Href)
	}
	if feed.Channel.Category.Text != "Technology" {
		t.Errorf("itunes:category text = %q, want Technology", feed.Channel.Category.Text)
	}
	if feed.Channel.Category.Sub == nil || feed.Channel.Category.Sub.Text != "Tech News" {
		t.Errorf("nested itunes:category = %+v, want Tech News", feed.Channel.Category.Sub)
	}

	// <link> (RSS, chardata) then <atom:link rel="self"> (href attr).
	if len(feed.Channel.Links) != 2 {
		t.Fatalf("channel has %d <link> elements, want 2 (rss + atom): %+v", len(feed.Channel.Links), feed.Channel.Links)
	}
	if feed.Channel.Links[0].Chardata != "https://techgo.example.com/" {
		t.Errorf("rss <link> = %q, want https://techgo.example.com/", feed.Channel.Links[0].Chardata)
	}
	if a := feed.Channel.Links[1]; a.Href != "https://techgo.example.com/feed.rss" || a.Rel != "self" || a.Type != "application/rss+xml" {
		t.Errorf("atom:link = %+v, want href=feed.rss rel=self type=application/rss+xml", a)
	}

	if len(feed.Channel.Items) != 2 {
		t.Fatalf("got %d items, want 2", len(feed.Channel.Items))
	}
	// Episodes are emitted in the order passed: newest (#2) first.
	it := feed.Channel.Items[0]
	if it.Episode != 2 {
		t.Errorf("first item itunes:episode = %d, want 2", it.Episode)
	}
	if it.Title != eps[0].Title {
		t.Errorf("item title = %q, want %q", it.Title, eps[0].Title)
	}
	if it.Description != eps[0].ShortDescription {
		t.Errorf("item description = %q, want %q", it.Description, eps[0].ShortDescription)
	}
	if it.Encoded != eps[0].Description {
		t.Errorf("item content:encoded = %q, want %q", it.Encoded, eps[0].Description)
	}
	if it.Link != "https://techgo.example.com/episodes/0002.html" {
		t.Errorf("item link = %q", it.Link)
	}
	if it.GUID.Value != "techgo-0002" || it.GUID.IsPermaLink != "false" {
		t.Errorf("item guid = %+v, want techgo-0002 / isPermaLink=false", it.GUID)
	}
	if it.PubDate != "Mon, 11 May 2026 12:00:00 +0000" {
		t.Errorf("item pubDate = %q, want RFC-2822", it.PubDate)
	}
	if it.Enclosure.URL != "https://techgo.example.com/media/0002.mp3" {
		t.Errorf("enclosure url = %q", it.Enclosure.URL)
	}
	if it.Enclosure.Length != 7_654_321 {
		t.Errorf("enclosure length = %d, want 7654321 (the real MP3 byte size)", it.Enclosure.Length)
	}
	if it.Enclosure.Type != "audio/mpeg" {
		t.Errorf("enclosure type = %q, want audio/mpeg", it.Enclosure.Type)
	}
	if it.Duration != 3661 {
		t.Errorf("itunes:duration = %d, want 3661 (integer seconds)", it.Duration)
	}
	if it.EpisodeType != "full" {
		t.Errorf("itunes:episodeType = %q, want full", it.EpisodeType)
	}
	if it.Explicit != "true" {
		t.Errorf("item itunes:explicit = %q, want true (episode override)", it.Explicit)
	}

	second := feed.Channel.Items[1]
	if second.Episode != 1 || second.Link != "https://techgo.example.com/episodes/hello-techgo.html" {
		t.Errorf("second item = ep %d link %q, want ep 1 / hello-techgo.html", second.Episode, second.Link)
	}
	if second.GUID.Value != "techgo-0001" {
		t.Errorf("second item guid = %q, want default techgo-0001", second.GUID.Value)
	}
	if second.Explicit != "false" {
		t.Errorf("second item itunes:explicit = %q, want false (inherits site default)", second.Explicit)
	}

	// Spot-check the raw bytes: the show notes are wrapped in CDATA (not escaped),
	// the literal "]]>" is split so the section stays well-formed, and namespace
	// prefixes are declared on <rss>.
	if !strings.HasPrefix(out, xml.Header) {
		t.Errorf("feed does not start with the XML declaration: %.40q", out)
	}
	if !strings.Contains(out, "<![CDATA[") {
		t.Error("show notes are not wrapped in a CDATA section")
	}
	if strings.Contains(out, "]]><p>") || strings.Contains(out, `a "]]>" sequence`) {
		t.Error("a literal ]]> in the show notes was not split — the CDATA section may be malformed")
	}
	for _, decl := range []string{
		`xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd"`,
		`xmlns:content="http://purl.org/rss/1.0/modules/content/"`,
		`xmlns:atom="http://www.w3.org/2005/Atom"`,
	} {
		if !strings.Contains(out, decl) {
			t.Errorf("missing namespace declaration %q", decl)
		}
	}
}
