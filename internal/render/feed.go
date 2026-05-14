package render

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// XML namespace URIs declared on the <rss> element, and the enclosure/feed MIME
// type. The feed is written with prefixed names (itunes:, content:, atom:) — the
// idiom every podcast host emits — rather than encoding/xml's auto-generated
// namespace prefixes.
const (
	itunesNS    = "http://www.itunes.com/dtds/podcast-1.0.dtd"
	contentNS   = "http://purl.org/rss/1.0/modules/content/"
	atomNS      = "http://www.w3.org/2005/Atom"
	rssMIMEType = "application/rss+xml"
	audioMP3    = "audio/mpeg"
)

// cdata wraps a string so it marshals as <![CDATA[ ... ]]> — used for the HTML
// show notes carried in <description> and <content:encoded>, where encoding/xml's
// normal text escaping would turn the markup into &lt;p&gt;… A literal "]]>"
// inside the string is split so the CDATA section stays well-formed.
type cdata struct{ S string }

func (c cdata) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	s := strings.ReplaceAll(c.S, "]]>", "]]]]><![CDATA[>")
	return e.EncodeElement(struct {
		Inner string `xml:",innerxml"`
	}{"<![CDATA[" + s + "]]>"}, start)
}

type rssDoc struct {
	XMLName      xml.Name   `xml:"rss"`
	Version      string     `xml:"version,attr"`
	XMLNSItunes  string     `xml:"xmlns:itunes,attr"`
	XMLNSContent string     `xml:"xmlns:content,attr"`
	XMLNSAtom    string     `xml:"xmlns:atom,attr"`
	Channel      rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title          string         `xml:"title"`
	Link           string         `xml:"link"`
	Language       string         `xml:"language"`
	Description    cdata          `xml:"description"`
	Copyright      string         `xml:"copyright,omitempty"`
	LastBuildDate  string         `xml:"lastBuildDate"`
	Generator      string         `xml:"generator"`
	AtomLink       atomLink       `xml:"atom:link"`
	ItunesAuthor   string         `xml:"itunes:author"`
	ItunesType     string         `xml:"itunes:type"`
	ItunesExplicit string         `xml:"itunes:explicit"`
	ItunesImage    itunesImage    `xml:"itunes:image"`
	ItunesOwner    itunesOwner    `xml:"itunes:owner"`
	ItunesCategory itunesCategory `xml:"itunes:category"`
	Items          []rssItem      `xml:"item"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type itunesImage struct {
	Href string `xml:"href,attr"`
}

type itunesOwner struct {
	Name  string `xml:"itunes:name"`
	Email string `xml:"itunes:email"`
}

// itunesCategory is the Apple Podcasts category; Sub carries an optional nested
// subcategory (omitted when nil).
type itunesCategory struct {
	Text string          `xml:"text,attr"`
	Sub  *itunesCategory `xml:"itunes:category,omitempty"`
}

type enclosure struct {
	URL    string `xml:"url,attr"`
	Length int64  `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type rssGUID struct {
	IsPermaLink string `xml:"isPermaLink,attr"`
	Value       string `xml:",chardata"`
}

type rssItem struct {
	Title             string      `xml:"title"`
	Link              string      `xml:"link"`
	GUID              rssGUID     `xml:"guid"`
	PubDate           string      `xml:"pubDate"`
	Description       cdata       `xml:"description"`
	ContentEncoded    cdata       `xml:"content:encoded"`
	Enclosure         enclosure   `xml:"enclosure"`
	ItunesDuration    int         `xml:"itunes:duration"`
	ItunesEpisode     int         `xml:"itunes:episode"`
	ItunesEpisodeType string      `xml:"itunes:episodeType"`
	ItunesExplicit    string      `xml:"itunes:explicit"`
	ItunesImage       itunesImage `xml:"itunes:image"`
}

// explicitWord renders an Apple <itunes:explicit> flag.
func explicitWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// WriteFeed renders the podcast's RSS 2.0 + iTunes feed (the bytes served at
// /feed.rss). episodes are emitted in the order given (the build passes them
// newest first). The output is the XML declaration followed by an indented
// document with a trailing newline.
//
// The feed is built from encoding/xml structs rather than a text template
// because text/template does not escape XML — a stray '&', '<' or '"' in a
// title, author, or URL would corrupt the feed. encoding/xml escapes element
// text and attribute values; the only thing it can't do natively is CDATA, which
// the cdata type above provides for the HTML show notes.
func WriteFeed(w io.Writer, s *config.Site, episodes []config.Episode) error {
	cat := itunesCategory{Text: s.Category}
	if s.Subcategory != "" {
		cat.Sub = &itunesCategory{Text: s.Subcategory}
	}
	ownerName := s.OwnerName
	if ownerName == "" {
		ownerName = s.Author
	}

	doc := rssDoc{
		Version:      "2.0",
		XMLNSItunes:  itunesNS,
		XMLNSContent: contentNS,
		XMLNSAtom:    atomNS,
		Channel: rssChannel{
			Title:          s.Title,
			Link:           s.AbsURL(""),
			Language:       s.Language,
			Description:    cdata{s.Description},
			Copyright:      s.Copyright,
			LastBuildDate:  rfc2822(time.Now()),
			Generator:      "techgo",
			AtomLink:       atomLink{Href: s.AbsURL("feed.rss"), Rel: "self", Type: rssMIMEType},
			ItunesAuthor:   s.Author,
			ItunesType:     s.ItunesType,
			ItunesExplicit: explicitWord(s.Explicit),
			ItunesImage:    itunesImage{Href: s.CoverURL()},
			ItunesOwner:    itunesOwner{Name: ownerName, Email: s.OwnerEmail},
			ItunesCategory: cat,
		},
	}
	for _, e := range episodes {
		imageURL := s.CoverURL()
		if e.Image != "" {
			imageURL = s.AbsURL(e.Image)
		}
		doc.Channel.Items = append(doc.Channel.Items, rssItem{
			Title:             e.Title,
			Link:              s.AbsURL(e.PagePath()),
			GUID:              rssGUID{IsPermaLink: "false", Value: e.EffectiveGUID()},
			PubDate:           rfc2822(e.PubDate),
			Description:       cdata{e.ShortDescription},
			ContentEncoded:    cdata{e.Description},
			Enclosure:         enclosure{URL: s.AbsURL(e.MP3Path()), Length: e.MP3Size, Type: audioMP3},
			ItunesDuration:    e.AudioDur,
			ItunesEpisode:     e.Number,
			ItunesEpisodeType: "full",
			ItunesExplicit:    explicitWord(e.EffectiveExplicit(s)),
			ItunesImage:       itunesImage{Href: imageURL},
		})
	}

	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("encoding RSS feed: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return err
	}
	return nil
}
