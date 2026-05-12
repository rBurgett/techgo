package render

import (
	"encoding/json"
	"fmt"
	"html/template"
	"time"

	"github.com/rBurgett/techgo/internal/config"
)

// funcMap builds the template FuncMap for a render run. Several entries close
// over the loaded site so templates can produce absolute URLs without threading
// the Site through every call.
func funcMap(site *config.Site) template.FuncMap {
	return template.FuncMap{
		"absURL":        func(p string) string { return site.AbsURL(p) },
		"rfc2822":       rfc2822,
		"durationHMS":   durationHMS,
		"fileSizeHuman": fileSizeHuman,
		"safeHTML":      safeHTML,
		"pad":           func(n int) string { return fmt.Sprintf("%04d", n) },
		"year":          func() int { return time.Now().Year() },
		"now":           time.Now,
		"iso8601":       iso8601,
		"jsonStr":       jsonStr,
	}
}

// rfc2822 formats t as an RFC 822/2822 date in UTC, e.g.
// "Mon, 02 Jan 2006 15:04:05 -0700" — the format RSS and Apple Podcasts require.
func rfc2822(t time.Time) string {
	return t.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700")
}

// iso8601 formats t as an RFC 3339 timestamp in UTC, for <meta
// property="article:published_time"> and JSON-LD datePublished.
func iso8601(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// durationHMS formats a whole-second duration as H:MM:SS for on-page display.
// A negative or zero value renders as "0:00:00".
func durationHMS(secs int) string {
	if secs < 0 {
		secs = 0
	}
	h := secs / 3600
	m := (secs / 60) % 60
	s := secs % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}

// fileSizeHuman formats a byte count with binary-magnitude units (1 KB = 1024 B),
// e.g. 44_150_000 -> "42.1 MB".
func fileSizeHuman(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for v := b / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// safeHTML marks s as trusted HTML so html/template emits it verbatim instead of
// escaping it. It is used ONLY for episode show notes — the `description:` field
// of data/*.yml, which is author-written and assumed to contain safe markup. It
// must never be applied to anything that could carry untrusted input.
func safeHTML(s string) template.HTML { return template.HTML(s) }

// jsonStr JSON-encodes s as a quoted string literal with HTML-safe escaping of
// '<', '>' and '&' (Go's json default), suitable for interpolating into an
// inline JSON-LD <script> block.
func jsonStr(s string) string {
	b, err := json.Marshal(s)
	if err != nil { // unreachable for a plain string, but never break a page over it
		return `""`
	}
	return string(b)
}
