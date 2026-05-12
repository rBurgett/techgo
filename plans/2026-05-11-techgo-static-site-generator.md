# Plan: `techgo` — static site generator for the Tech.Go podcast

## Context

Tech.Go is a tech podcast by "Isaac and his dad". We need a Go CLI tool (`techgo`) that:
turns per-episode YAML data files + `.wav`/`.mp4` source media into a static website (dark, minimal,
nerdy aesthetic à la <https://technoagorist.com/>), generates a valid podcast RSS feed at `/feed.rss`,
transcodes media with `ffmpeg`, generates a full web image set (favicon/logo/cover/OG) from one source PNG,
and deploys the built site to S3 behind CloudFront (uploading + cache invalidation). A CloudFormation
template provisions the AWS resources. The repo is currently empty (only `README.md`, `.gitignore`,
`.idea/`) — this is a greenfield build. Go 1.25 is installed; `ffmpeg`/`ffprobe`, the AWS CLI, and
ImageMagick are **not** installed (we shell out to `ffmpeg`/`ffprobe` only; AWS is done with pure-Go
SigV4; images are pure Go).

### Decisions locked in (from clarifying questions)
- **Dependencies:** `github.com/spf13/cobra` (CLI), `gopkg.in/yaml.v3` (YAML), `golang.org/x/image` (image resampling). **Everything else is Go standard library** — no AWS SDK, no web framework, no markdown lib, no http router. (`ffmpeg`/`ffprobe` are external *tools*, invoked via `os/exec`.)
- **Video:** source `.mp4` → a single web-optimized "faststart" progressive MP4 (`-movflags +faststart`, H.264/AAC, `yuv420p`), played in a plain HTML5 `<video>` tag. No HLS, no JS player.
- **Deploy:** pure-Go S3 `PutObject` + CloudFront `CreateInvalidation` via hand-rolled AWS Signature V4 (stdlib `crypto/hmac` + `crypto/sha256` + `net/http`). No `aws` CLI required. Secrets read from a `.env` file in the project root.
- **Images:** `golang.org/x/image/draw` (CatmullRom resampling) for downscales; a ~40-line hand-rolled PNG-in-ICO encoder for `favicon.ico` (stdlib has no ICO encoder).

### Corrections to the original request
1. **"128 bit MP3" → 128 *kbps* CBR MP3.** MP3 bitrates are kbps; 128 kbps CBR is the standard podcast bitrate. That's what we target.
2. **Reference image path.** You wrote `~/home/ryan/Pictures/image.png`, which resolves to `/home/ryan/home/ryan/...` (doesn't exist). The actual file is `/home/ryan/Pictures/image.png` — a 1080×1080 dark "Tech.go" wordmark. Palette extracted: background charcoal ≈ `#1a1a1a`, a subtly lighter panel behind the text ≈ `#202020`, "Tech" in a muted brick/rust red ≈ `#a04b3a`, ".go" in a very low-contrast dark grey ≈ `#2c2c2c`.
3. **"Rely solely on the Go standard library" vs. cobra/YAML** — these conflict; resolved as above (cobra + yaml.v3 + x/image, rest stdlib).
4. **`favicon.ico`** cannot be produced with stdlib + x/image alone (no ICO encoder). We hand-roll a tiny PNG-in-ICO encoder (modern browsers + Windows accept PNG payloads inside `.ico`).
5. **The `.go` wordmark colour `#2c2c2c` is essentially invisible on `#1a1a1a`.** On the site we render ".go" in a readable dim grey (≈ `#7a756f`) so it's lower-contrast than "Tech" but still legible. (The literal near-invisible grey only works in the source logo because it has the lighter panel behind it.)
6. **CloudFront + S3:** with Origin Access Control (OAC) the bucket must be **private** (no S3 static-website hosting — incompatible with OAC), and the CloudFront origin is the S3 **REST regional endpoint** (`bucket.s3.<region>.amazonaws.com`), not the website endpoint. Consequence: no automatic directory-index resolution for sub-paths, so episode pages are generated as explicit files `episodes/0001.html` (linked with the `.html` extension). A missing object returns HTTP **403** (not 404) through a REST origin, so the CloudFront custom-error mapping is `403 → /404.html` (plus `404 → /404.html`).
7. **ACM cert** must be in **us-east-1** for CloudFront. The CloudFormation template does **not** create it (as you asked) — it takes the cert ARN + domain name(s) as parameters. Separately: the CloudFront *API* (for invalidations) is signed with region `us-east-1` / service `cloudfront` regardless of the bucket's region; the bucket region (`AWS_REGION` in `.env`) is only used for the S3 endpoint host and S3 signing.
8. **Idempotent transcode** ("skip if output newer than source") compares file mtimes — note this is fragile after a fresh `git clone` (clone resets mtimes to checkout time). Acceptable for a single-author workflow; a `--force` flag is provided.

---

## Repo layout (end state)

```
techgo/
├── go.mod, go.sum                  # module github.com/rBurgett/techgo, go 1.25
├── main.go                         # thin: cmd.Execute()
├── README.md, .gitignore           # exist; .gitignore already ignores .env — add public/, media/out/, /techgo
├── site.yml                        # site + podcast metadata (committed)
├── .env                            # AWS secrets (gitignored already)
├── .env.example                    # committed template
├── cmd/
│   ├── root.go                     # cobra root + persistent --project/-p, --output/-o flags
│   ├── new.go                      # techgo new ["Title"]   → scaffold data/NNNN.yml
│   ├── media.go                    # techgo media           → ffmpeg transcode (idempotent)
│   ├── build.go                    # techgo build           → render site into public/
│   ├── serve.go                    # techgo serve           → local preview http server
│   ├── images.go                   # techgo images [src.png]→ generate favicon/cover/og/etc.
│   └── deploy.go                   # techgo deploy          → S3 upload + CloudFront invalidation
├── internal/
│   ├── config/
│   │   ├── site.go                 # Site struct + LoadSite()
│   │   ├── episode.go              # Episode struct + LoadEpisodes()
│   │   └── dotenv.go               # ParseDotEnv()
│   ├── media/
│   │   └── ffmpeg.go               # transcode + ffprobe wrappers + idempotent skip
│   ├── render/
│   │   ├── render.go               # template loading + Page view model + page rendering
│   │   ├── funcs.go                # template FuncMap (rfc2822, absURL, durationHMS, safeHTML, iso8601, ...)
│   │   ├── jsonld.go               # PodcastSeries / PodcastEpisode JSON-LD builders
│   │   ├── feed.go                 # podcast RSS 2.0 + iTunes feed via encoding/xml (NOT text/template)
│   │   └── sitemap.go              # sitemap.xml via encoding/xml
│   ├── images/
│   │   ├── resize.go               # x/image/draw CatmullRom resampling + PNG/JPEG save
│   │   └── ico.go                  # minimal PNG-in-ICO encoder
│   ├── awssig/
│   │   └── sigv4.go                # AWS SigV4 signer (stdlib hmac/sha256)
│   ├── awss3/
│   │   └── s3.go                   # PutObject via net/http + sigv4
│   └── awscf/
│       └── cloudfront.go           # CreateInvalidation via net/http + sigv4
├── data/                           # episode YAMLs: 0001.yml, 0002.yml, ...
│   └── 0001.yml
├── media/
│   ├── src/                        # raw sources: 0001.wav, 0001.mp4 (paths actually come from YAML)
│   └── out/                        # transcoded: 0001.mp3, 0001.mp4  (gitignored, generated)
├── templates/                      # html/template files (editable, NOT embedded). RSS feed + sitemap are NOT templates — they're built with encoding/xml in internal/render/.
│   ├── base.html.tmpl              # layout: <head> (full social/SEO meta), header/nav, footer
│   ├── index.html.tmpl             # episode list + inline audio players
│   ├── episode.html.tmpl           # full notes + download link + <video>
│   └── about.html.tmpl
├── static/                         # copied verbatim into public/
│   ├── css/style.css
│   ├── favicon.ico, favicon-16.png, favicon-32.png, apple-touch-icon.png, icon-512.png
│   ├── og-image.png                # generated by `techgo images`
│   ├── robots.txt, site.webmanifest
│   └── (cover.png is generated into public/ at build time)
├── infra/
│   └── cloudformation.yml          # private S3 + OAC + CloudFront + bucket policy
└── public/                         # build output (gitignored, generated)
```

### Episode YAML (`data/0001.yml`)
```yaml
number: 1                               # int; orders the feed/index and keys the media filenames (media/0001.mp3)
title: "Hello, Tech.Go"
slug: "hello-techgo"                     # optional; if omitted the page URL is /episodes/0001.html. If set, /episodes/hello-techgo.html. Used for the page filename, canonical URL, sitemap, RSS <link>. Must be unique. (media files stay numbered regardless.)
shortDescription: "One-line teaser shown on the front page and in the feed <description>."
pubDate: 2026-05-11T12:00:00Z           # RFC3339 with offset; yaml.v3 parses to time.Time
guid: "techgo-0001"                     # optional stable id; default "techgo-0001"
# explicit: false                       # OPTIONAL — omit to inherit site.yml's explicit; set true/false only to override (Go field is *bool)
image: ""                               # optional; site-relative path (file lives at static/<image>, served at /<image>) used as this episode's social-share image (else site og-image.png)
sourceWav: "media/src/0001.wav"         # relative to project dir
sourceMp4: "media/src/0001.mp4"         # relative to project dir
description: |                          # show notes — author-written, rendered as TRUSTED HTML
  <p>Welcome to the show. Today we talk about...</p>
  <ul><li>Link one</li><li>Link two</li></ul>
```
Transcoded outputs are `media/out/0001.mp3` and `media/out/0001.mp4`, copied to `public/media/0001.mp3|.mp4`; their byte sizes (for the RSS `<enclosure length>`) and durations (`<itunes:duration>`) are computed at build time, not stored in YAML.

### `site.yml`
```yaml
title: "Tech.Go"
tagline: "A tech podcast by Isaac and his dad"
description: "A nerdy, tech-focused podcast hosted by Isaac and his dad."
author: "Isaac and his dad"             # <itunes:author>
ownerName: "Ryan Burgett"               # <itunes:owner><itunes:name>
ownerEmail: "ryan@burgettdev.net"       # <itunes:owner><itunes:email>  (Apple requires this)
baseURL: "https://techgo.example.com"   # NO trailing slash; used for all absolute URLs in the feed
language: "en-us"
copyright: "© 2026 Tech.Go"
explicit: false
category: "Technology"                  # exact Apple category string
subcategory: ""                         # optional nested Apple subcategory
itunesType: "episodic"                  # or "serial"
coverArt: "static/logo-source.png"      # source PNG for `techgo images` / cover-art generation
coverArtURL: ""                         # optional override; else derived = baseURL + "/cover.png"
twitterHandle: ""                        # optional, e.g. "@techgo" → twitter:site meta tag
social:
  github: "https://github.com/rBurgett/techgo"
```

### `.env.example`
```
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
AWS_SESSION_TOKEN=          # optional — only for STS / assumed-role / IAM Identity Center credentials
AWS_REGION=us-east-1        # the S3 bucket's region (CloudFront API is always signed us-east-1)
S3_BUCKET=techgo-site
S3_PREFIX=                  # optional — deploy under a key prefix instead of the bucket root (if set, CloudFront origin path must be /<prefix>)
CLOUDFRONT_DISTRIBUTION_ID=
```

---

## Phase 1 — Skeleton: module, cobra root, config + `.env` loading

**Create:** `go.mod`, `main.go`, `cmd/root.go`, `internal/config/site.go`, `internal/config/episode.go`, `internal/config/dotenv.go`, `.env.example`, `site.yml`, `data/0001.yml`; append `public/`, `media/out/`, `/techgo` to `.gitignore`.

- `go.mod`: `module github.com/rBurgett/techgo`, `go 1.25`, require `github.com/spf13/cobra`, `gopkg.in/yaml.v3`, `golang.org/x/image`. Run `go mod tidy` (transitive deps will include `spf13/pflag` and `inconshreveable/mousetrap` via cobra — acceptable).
- `main.go`: `package main` → `func main() { cmd.Execute() }`.
- `cmd/root.go`: `rootCmd` (`Use: "techgo"`, short/long help). Persistent flags `--project/-p` (default `.`) and `--output/-o` (default `public`). Helpers `ProjectDir()` / `OutputDir()` returning absolute paths. `Execute()` runs `rootCmd.Execute()`, `os.Exit(1)` on error. All subcommands use `RunE` so failures are non-zero exit codes with actionable messages.
- `internal/config/site.go`: `Site` struct mirroring `site.yml` (fields above, incl. optional `TwitterHandle`). `LoadSite(projectDir string) (*Site, error)` reads `<projectDir>/site.yml` (decode with `yaml` `KnownFields(true)` so typo'd keys are an error), then validates: required non-empty `Title`, `Author`, `OwnerEmail`, `Category`; `Language` defaults to `"en-us"`, `ItunesType` defaults to `"episodic"` (else must be `"serial"`); **`BaseURL` (trimmed) is parsed with `url.Parse`, must have scheme `http`/`https` and a non-empty host, and must NOT carry a query string, fragment, or userinfo** — store normalized as `scheme://host[/path]` with any trailing `/` on the path trimmed; **`CoverArtURL` (if set) is trimmed and validated the same way** (absolute `http(s)://host`). Methods: `(*Site) AbsURL(p string) string` = `strings.TrimRight(BaseURL,"/") + "/" + strings.TrimLeft(p,"/")`; `(*Site) CoverURL() string` = `CoverArtURL` if set else `AbsURL("cover.png")` (square, for the podcast feed `<itunes:image>` + JSON-LD `image`); `(*Site) OGImageURL() string` = `AbsURL("og-image.png")` (1200×630 card, for `og:image`/`twitter:image`).
- `internal/config/episode.go`: `Episode` struct mirroring the episode YAML (decode with `KnownFields(true)`). **`Explicit` must be `*bool`** (`yaml:"explicit"`) so "omitted" is distinguishable from "explicitly false" — required for "episode overrides the site default" to work. Other optional string fields (`Slug`, `GUID`, `Image`) are plain `string` ("" ⇒ use the default). `Image` is a **site-relative path** (e.g. `episodes/0001.png`) — the file lives at `<projectDir>/static/<image>` and is served from `/<image>` after `build` copies `static/` into the site root; it is *not* prefixed with `static/`. Plus `yaml:"-"` build-time fields `MP3Size, MP4Size int64`, `AudioDur, VideoDur int` (seconds), and `SourceFile` (the YAML path, for error messages). Methods: `(Episode) Pad() string` = `fmt.Sprintf("%04d", Number)`; `(Episode) EffectiveSlug() string` = `Slug` if set else `Pad()` (so the no-slug default URL is `episodes/0001.html`); `(Episode) PagePath() string` = `"episodes/" + EffectiveSlug() + ".html"`; `(Episode) MP3Path() string` = `"media/" + Pad() + ".mp3"` (media stays number-keyed — stable, matches the RSS enclosure); `(Episode) MP4Path() string` = `"media/" + Pad() + ".mp4"`; `(Episode) EffectiveGUID() string` = `GUID` if set else `"techgo-"+Pad()`; `(Episode) EffectiveExplicit(s *Site) bool` = `*Explicit` if `Explicit != nil` else `s.Explicit`; `(Episode) SocialImageURL(s *Site) string` = `s.AbsURL(Image)` if `Image != ""` else `s.OGImageURL()`. `LoadEpisodes(projectDir string) ([]Episode, error)`: glob `<projectDir>/data/*.yml`, unmarshal each, then **normalize + validate per episode**: `Number > 0`; `Title` non-empty; `PubDate` non-zero; `SourceWav` and `SourceMp4` non-empty (file existence is checked in `media`/`build`, not here); `Slug` (if set) matches `^[a-z0-9][a-z0-9-]*$`; `Image` (if set) → `path.Clean`'d, must not be absolute / `..`-escaping, must exist at `<projectDir>/static/<image>`, and is stored back in its cleaned forward-slash form (so `AbsURL(Image)` is a sane URL). Error on duplicate `Number` **and on duplicate `EffectiveSlug()`** (this also catches a slug like `"0002"` colliding with another episode's default `Pad()`). Sort by `Number` **descending** (newest first → deterministic index + feed order). Errors name the offending file.
- `internal/config/dotenv.go`: `ParseDotEnv(path string) (map[string]string, error)` — `KEY=VALUE` lines; ignore blanks and `#` comments; trim surrounding single/double quotes from values; no shell expansion. Missing file → clear error ("create a .env file, see .env.example").

**Verify:** `go build ./...` and `go run . --help` work. `go test ./internal/config` round-trips a sample episode YAML and a `.env` string written to `t.TempDir()`.

---

## Phase 2 — `techgo new`

**Create:** `cmd/new.go`.

`techgo new ["Episode Title"]`: `LoadEpisodes` → `next = max(number)+1` (or 1). Target `<projectDir>/data/<%04d next>.yml`; refuse if it exists. Write a YAML scaffold (raw string for nice formatting/comments) pre-filled with `number`, `title` (arg or placeholder), `slug` (`%04d`), empty `shortDescription`, `pubDate` = now (RFC3339 Z), `guid` = `techgo-%04d`, `sourceWav`/`sourceMp4` = `media/src/%04d.wav|.mp4`, and a `description: |` block placeholder. Print the created path. (This is the only command that writes into the project dir during normal use — it's the user's repo, not the system.)

**Verify:** in a scratch dir containing `data/0001.yml`, `techgo new "Test"` creates `data/0002.yml` with `number: 2`; re-running errors (file exists).

---

## Phase 3 — `techgo media`: ffmpeg/ffprobe transcoding (idempotent)

**Create:** `internal/media/ffmpeg.go`, `cmd/media.go`.

`internal/media/ffmpeg.go`:
- `requireTool(name string) error` — `exec.LookPath`; friendly error if `ffmpeg`/`ffprobe` missing.
- `ProbeDurationSeconds(path string) (int, error)`:
  `ffprobe -v error -show_entries format=duration -of default=noprint_wrappers=1:nokey=1 <path>` → parse float → `int(math.Round(f))`.
- `ProbeIsH264AAC(path string) (bool, error)`:
  `ffprobe -v error -select_streams v:0 -show_entries stream=codec_name -of csv=p=0 <path>` (expect `h264`) and the same with `-select_streams a:0` (expect `aac`); both match ⇒ remux is safe.
- `TranscodeWAVtoMP3(src, dst string) error`:
  `ffmpeg -y -i <src> -vn -c:a libmp3lame -b:a 128k -ar 44100 -ac 2 -map_metadata -1 -id3v2_version 3 -write_id3v1 1 <dst>`
  (`-b:a 128k` + libmp3lame = CBR 128 kbps; `-map_metadata -1` strips source tags; `-vn` drops any cover-art stream.)
- `TranscodeMP4Faststart(src, dst string) error`: probe first.
  - Remux path (already H.264/AAC): `ffmpeg -y -i <src> -c copy -map_metadata -1 -movflags +faststart <dst>`
  - Re-encode path (otherwise): `ffmpeg -y -i <src> -c:v libx264 -profile:v high -pix_fmt yuv420p -preset slow -crf 21 -c:a aac -b:a 160k -ar 48000 -ac 2 -map_metadata -1 -movflags +faststart <dst>`
  (`+faststart` moves the moov atom to the front for progressive playback/seeking; `yuv420p` for broad browser/Safari/QuickTime compatibility.)
- `NeedsRebuild(src, dst string, force bool) (bool, error)` — `force` ⇒ true; `dst` missing ⇒ true; else `srcMtime.After(dstMtime)`.

`cmd/media.go`: flags `--force`, `--episode N` (limit to one). First call `requireTool("ffmpeg")` and `requireTool("ffprobe")` — both are required for this command (ffprobe is needed for codec detection and durations); error clearly if either is missing. For each episode: resolve `SourceWav`/`SourceMp4` relative to project dir (error if missing); ensure `<projectDir>/media/out/` exists; outputs `<%04d>.mp3` and `<%04d>.mp4`. Transcode iff `NeedsRebuild`, else print "skip (up to date)". After, print a summary table (episode #, mp3 size + duration, mp4 size + duration) using `ProbeDurationSeconds` + `os.Stat`.

**Verify:** with a tiny sample `.wav` + `.mp4`: `techgo media` produces `media/out/0001.mp3` (`ffprobe` shows ~128 kb/s, 44100 Hz) and `media/out/0001.mp4` (faststart — `ffprobe -v trace ... 2>&1 | grep -n -E 'moov|mdat'` shows `moov` before `mdat`). Re-run ⇒ both skipped. `touch media/src/0001.wav` ⇒ only the mp3 rebuilds.

---

## Phase 4 — Templates, dark theme CSS, render package

**Create:** `internal/render/funcs.go`, `internal/render/render.go`, `internal/render/jsonld.go`, `templates/base.html.tmpl`, `templates/index.html.tmpl`, `templates/episode.html.tmpl`, `templates/about.html.tmpl`, `static/css/style.css`, `static/robots.txt`, `static/site.webmanifest`.

`internal/render/funcs.go` — `FuncMap` (closures over the loaded `*Site`):
- `absURL(p string) string` → `site.AbsURL(p)`
- `rfc2822(t time.Time) string` → `t.UTC().Format("Mon, 02 Jan 2006 15:04:05 -0700")` (RFC 822/2822 — required by RSS + Apple)
- `durationHMS(secs int) string` → `H:MM:SS` for on-page display
- `fileSizeHuman(b int64) string` → e.g. `42.1 MB`
- `safeHTML(s string) template.HTML` → marks episode `description` as trusted HTML (author-written; document the trust assumption in code)
- `pad(n int) string` → `fmt.Sprintf("%04d", n)`
- `year() int` → current year (footer)
- `now() time.Time` (used by feed; harmless here)
- `iso8601(t time.Time) string` → `t.UTC().Format(time.RFC3339)` (for `<meta property="article:published_time">` and JSON-LD `datePublished`)
- `jsonStr(s string) string` → JSON-encode a string (used in inline JSON-LD `<script>` blocks)

**Per-page metadata view model** — every page is rendered with a `Page` wrapper so the shared `<head>` can emit correct, page-specific social tags:
```go
type Page struct {
    Site        *Site
    Title       string        // <title> + og:title + twitter:title  (e.g. "Episode title — Tech.Go", or just site title on home)
    Description  string        // <meta name=description> + og:description + twitter:description (≤ ~200 chars, plain text)
    Canonical    string        // absolute canonical URL of this page
    OGType       string        // "website" (home/about/404) | "article" (episode page)
    ImageURL     string        // absolute URL of the social image for this page
    PublishedISO string        // RFC3339; only set on episode pages → article:published_time
    JSONLD       template.JS    // pre-rendered JSON-LD object for this page (string of valid JSON), or ""
    // page-specific payload:
    Episodes []Episode    // index page
    Episode  *Episode     // episode page
    Prev     *Episode     // episode page
    Next     *Episode     // episode page
}
```

`internal/render/render.go`:
- `type Renderer struct { site *Site; tmpl *template.Template }`
- `New(projectDir string, site *Site) (*Renderer, error)`: `template.New("").Funcs(funcMap(site)).ParseGlob(filepath.Join(projectDir,"templates","*.html.tmpl"))` (`html/template`). The RSS feed and `sitemap.xml` are not templates — they're produced by `internal/render/feed.go` / `sitemap.go` with `encoding/xml`.
- `(*Renderer) RenderIndex(w io.Writer, eps []Episode) error` — builds a `Page{OGType:"website", Title:site.Title, Description:site.Description, Canonical:site.BaseURL+"/", ImageURL:site.OGImageURL(), JSONLD: podcastSeriesLD(site, eps)}` and executes `index.html.tmpl`.
- `(*Renderer) RenderEpisode(w io.Writer, ep, prev, next *Episode) error` — `Page{OGType:"article", Title: ep.Title+" — "+site.Title, Description: ep.ShortDescription, Canonical: site.AbsURL(ep.PagePath()), ImageURL: ep.SocialImageURL(site), PublishedISO: ep.PubDate.UTC().Format(time.RFC3339), JSONLD: podcastEpisodeLD(site, *ep)}`, executes `episode.html.tmpl`.
- `(*Renderer) RenderAbout(w io.Writer) error` — `Page{OGType:"website", Title:"About — "+site.Title, Description:"About Tech.Go — "+site.Description, Canonical: site.AbsURL("about.html"), ImageURL: site.OGImageURL()}`.
- `(*Renderer) Render404(w io.Writer) error` — `Page{OGType:"website", Title:"Not found — "+site.Title, Description:"Page not found.", Canonical: site.AbsURL("404.html"), ImageURL: site.OGImageURL()}`.
- Helpers in `internal/render/jsonld.go`:
  - `podcastSeriesLD(s *Site, eps []Episode) template.JS` → schema.org `PodcastSeries` (`name`, `description`, `url`=BaseURL, `image`=cover URL, `webFeed`=feed URL, `author`/`creator`).
  - `podcastEpisodeLD(s *Site, e Episode) template.JS` → schema.org `PodcastEpisode` (`name`, `description`, `datePublished`, `url`, `episodeNumber`, `partOfSeries`={PodcastSeries name+url}, `associatedMedia`={MediaObject `contentUrl`=mp3 URL, `encodingFormat`="audio/mpeg"}, optional `duration` as ISO-8601 `PT#H#M#S`).
  - `site.OGImageURL()` = `site.AbsURL("og-image.png")` (the 1200×630 card from `techgo images`). `site.CoverURL()` = the square 3000×3000 cover (used for the podcast feed `<itunes:image>` and JSON-LD `image`). `Episode.SocialImageURL(s)` = `s.AbsURL(e.Image)` if `e.Image` set (the optional `image:` field is a site-relative path; the file lives at `static/<image>` and `build` copies `static/` into the site root) else `s.OGImageURL()`.

Templates (`html/template`; `base.html.tmpl` is the layout, page templates fill `{{block "content" .}}` and receive a `Page`):
- `base.html.tmpl` — `<!doctype html>`, `<html lang="{{.Site.Language}}">`, then a `<head>` with the full social/SEO set:
  ```html
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}}</title>
  <meta name="description" content="{{.Description}}">
  <link rel="canonical" href="{{.Canonical}}">
  <meta name="theme-color" content="#1a1a1a">
  <meta name="generator" content="techgo">
  <!-- Open Graph -->
  <meta property="og:site_name" content="{{.Site.Title}}">
  <meta property="og:title" content="{{.Title}}">
  <meta property="og:description" content="{{.Description}}">
  <meta property="og:type" content="{{.OGType}}">
  <meta property="og:url" content="{{.Canonical}}">
  <meta property="og:locale" content="en_US">
  <meta property="og:image" content="{{.ImageURL}}">
  <meta property="og:image:alt" content="{{.Title}}">
  {{if eq .OGType "article"}}<meta property="article:published_time" content="{{.PublishedISO}}">
  <meta property="article:author" content="{{.Site.Author}}">{{end}}
  <!-- Twitter / X card -->
  <meta name="twitter:card" content="summary_large_image">
  <meta name="twitter:title" content="{{.Title}}">
  <meta name="twitter:description" content="{{.Description}}">
  <meta name="twitter:image" content="{{.ImageURL}}">
  {{if .Site.TwitterHandle}}<meta name="twitter:site" content="{{.Site.TwitterHandle}}">{{end}}
  <!-- Icons / feed / manifest -->
  <link rel="icon" href="/favicon.ico" sizes="any">
  <link rel="icon" type="image/png" sizes="32x32" href="/favicon-32.png">
  <link rel="icon" type="image/png" sizes="16x16" href="/favicon-16.png">
  <link rel="apple-touch-icon" href="/apple-touch-icon.png">
  <link rel="manifest" href="/site.webmanifest">
  <link rel="alternate" type="application/rss+xml" title="{{.Site.Title}} podcast feed" href="{{absURL "feed.rss"}}">
  <link rel="stylesheet" href="/css/style.css">
  {{if .JSONLD}}<script type="application/ld+json">{{.JSONLD}}</script>{{end}}
  ```
  `<body>`: `<header><div class="wrap"><a class="wordmark" href="/"><span class="t">Tech</span><span class="g">.go</span></a><nav><a href="/">Episodes</a><a href="/about.html">About</a><a href="/feed.rss">RSS</a></nav></div></header>`, `<main>{{block "content" .}}{{end}}</main>`, `<footer>© {{year}} {{.Site.Copyright}}</footer>`.
- `index.html.tmpl` (`{{define "content"}}`, dot is the `Page`): page heading + `.Site.Tagline`, then `{{range .Episodes}}` → `<article class="episode-card">` with episode-number badge, `<h2><a href="/{{.PagePath}}">{{.Title}}</a></h2>`, `<p class="meta">{{rfc2822 .PubDate}} · {{durationHMS .AudioDur}}</p>`, `<p>{{.ShortDescription}}</p>`, and:
  ```html
  <audio controls preload="none" src="/{{.MP3Path}}"></audio>
  <a class="download" href="/{{.MP3Path}}" download>Download MP3</a>
  ```
- `episode.html.tmpl` (`{{define "content"}}`): `<article>` wrapper with `<h1>#{{.Episode.Number}} — {{.Episode.Title}}</h1>`, the meta line, the audio player + download link (`src`/`href` = `/{{.Episode.MP3Path}}`), then
  ```html
  <video controls preload="metadata" src="/{{.Episode.MP4Path}}"></video>
  ```
  then `<div class="show-notes">{{safeHTML .Episode.Description}}</div>`, then prev/next nav links (`href="/{{.Prev.PagePath}}"` etc., if set). (The `PodcastEpisode` JSON-LD is emitted by `base.html.tmpl` via `.JSONLD`.)
- `about.html.tmpl`: short copy about "Isaac and his dad", contact email, links from `.Site.Social`.

**`site.yml` gains** an optional `twitterHandle: "@techgo"` field (→ `twitter:site`); `Episode` gains an optional `image:` field — a **site-relative path** (the file lives at `static/<image>`, served at `/<image>`) used as that episode's social-share image. Both optional — if absent, the site-wide `og-image.png` is used everywhere.

`static/css/style.css` — dark, minimal, monospace-flavoured (no web fonts):
```css
:root{
  --bg:#1a1a1a; --panel:#202020; --accent:#a04b3a; --accent-hi:#c25f4a;
  --text:#d6d3cd; --text-dim:#8b8784; --go-grey:#7a756f; --border:#2e2e2e; --code-bg:#262320;
  --mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, "Liberation Mono", monospace;
  --sans: system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
}
*{box-sizing:border-box} html,body{margin:0;padding:0}
body{background:var(--bg);color:var(--text);font-family:var(--sans);line-height:1.6;font-size:16px}
main{max-width:760px;margin:0 auto;padding:2rem 1.25rem}
header{border-bottom:1px solid var(--border);background:var(--panel)}
header .wrap{max-width:760px;margin:0 auto;padding:1rem 1.25rem;display:flex;align-items:baseline;gap:1.5rem;flex-wrap:wrap}
.wordmark{font-family:var(--mono);font-weight:700;font-size:1.4rem;letter-spacing:-.02em;text-decoration:none}
.wordmark .t{color:var(--accent)} .wordmark .g{color:var(--go-grey)}
nav a{color:var(--text-dim);text-decoration:none;font-family:var(--mono);font-size:.9rem;margin-right:1rem}
nav a:hover{color:var(--text)}
a{color:var(--accent)} a:hover{color:var(--accent-hi)}
h1,h2,h3{font-family:var(--mono);font-weight:600;line-height:1.25}
.episode-card{background:var(--panel);border:1px solid var(--border);border-radius:6px;padding:1.25rem 1.5rem;margin:1.5rem 0}
.episode-card h2{margin:.25rem 0} .episode-card h2 a{text-decoration:none}
.meta{color:var(--text-dim);font-family:var(--mono);font-size:.8rem;margin:.25rem 0 1rem}
audio,video{width:100%;display:block;margin:.75rem 0;border-radius:4px;background:#000}
.download{font-family:var(--mono);font-size:.85rem}
.show-notes{margin-top:2rem} .show-notes p{margin:1rem 0}
code,pre{font-family:var(--mono);background:var(--code-bg);padding:.1rem .35rem;border-radius:3px}
pre{padding:1rem;overflow:auto}
footer{border-top:1px solid var(--border);color:var(--text-dim);font-size:.8rem;text-align:center;padding:2rem 1rem;font-family:var(--mono)}
```
`static/robots.txt` → `User-agent: *` / `Allow: /` / `Sitemap: <baseURL>/sitemap.xml` (a real sitemap is generated by `build` — see Phase 5; do **not** point this at `feed.rss`). `static/site.webmanifest` → minimal name/short_name/theme_color `#1a1a1a`/background_color `#1a1a1a`/icons `[icon-512.png]`.

**Verify:** real verification deferred to Phase 5 (`build` + `serve`). Optionally a quick `go test` calling `Renderer.RenderIndex(buf, eps)` and asserting no template error + links use `.html`.

---

## Phase 5 — `techgo build` (+ RSS feed) and `techgo serve`

**Create:** `internal/render/feed.go`, `internal/render/sitemap.go`, `cmd/build.go`, `cmd/serve.go`.

### `internal/render/feed.go` — podcast RSS via `encoding/xml` (NOT text/template)

The feed is built from `encoding/xml` structs, **not** a text template — `text/template` does not escape XML element text or attribute values, so titles/authors/URLs containing `&`, `<`, `"` etc. would corrupt the feed. `encoding/xml`'s `Marshal` escapes element text and attributes correctly. The only thing it doesn't do natively is CDATA, which we want for the HTML show notes — handled with a tiny custom type.

```go
// cdata wraps a string so it marshals as <![CDATA[ ... ]]> (used for description / content:encoded).
type cdata struct{ S string }
func (c cdata) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
    // split any literal "]]>" so the CDATA section stays well-formed
    s := strings.ReplaceAll(c.S, "]]>", "]]]]><![CDATA[>")
    return e.EncodeElement(struct {
        Inner string `xml:",innerxml"`
    }{"<![CDATA[" + s + "]]>"}, start)
}

type rss struct {
    XMLName     xml.Name `xml:"rss"`
    Version     string   `xml:"version,attr"`            // "2.0"
    XMLNSItunes string   `xml:"xmlns:itunes,attr"`       // http://www.itunes.com/dtds/podcast-1.0.dtd
    XMLNSContent string  `xml:"xmlns:content,attr"`      // http://purl.org/rss/1.0/modules/content/
    XMLNSAtom   string   `xml:"xmlns:atom,attr"`         // http://www.w3.org/2005/Atom
    Channel     channel  `xml:"channel"`
}
type channel struct {
    Title         string        `xml:"title"`
    Link          string        `xml:"link"`
    Language      string        `xml:"language"`
    Description   cdata         `xml:"description"`
    Copyright     string        `xml:"copyright,omitempty"`
    LastBuildDate string        `xml:"lastBuildDate"`          // RFC-2822
    Generator     string        `xml:"generator"`
    AtomLink      atomLink      `xml:"atom:link"`              // rel="self"
    ItunesAuthor  string        `xml:"itunes:author"`
    ItunesType    string        `xml:"itunes:type"`            // "episodic" | "serial"
    ItunesExplicit string       `xml:"itunes:explicit"`        // "true" | "false"
    ItunesImage   itunesImage   `xml:"itunes:image"`
    ItunesOwner   itunesOwner   `xml:"itunes:owner"`
    ItunesCategory itunesCategory `xml:"itunes:category"`
    Items         []item        `xml:"item"`
}
type atomLink struct { Href string `xml:"href,attr"`; Rel string `xml:"rel,attr"`; Type string `xml:"type,attr"` }
type itunesImage struct { Href string `xml:"href,attr"` }
type itunesOwner struct { Name string `xml:"itunes:name"`; Email string `xml:"itunes:email"` }
type itunesCategory struct { Text string `xml:"text,attr"`; Sub *itunesCategory `xml:"itunes:category,omitempty"` }
type enclosure struct { URL string `xml:"url,attr"`; Length int64 `xml:"length,attr"`; Type string `xml:"type,attr"` }
type guid struct { IsPermaLink string `xml:"isPermaLink,attr"`; Value string `xml:",chardata"` }
type item struct {
    Title           string     `xml:"title"`
    Link            string     `xml:"link"`
    GUID            guid       `xml:"guid"`
    PubDate         string     `xml:"pubDate"`             // RFC-2822
    Description     cdata      `xml:"description"`         // short description
    ContentEncoded  cdata      `xml:"content:encoded"`     // full HTML show notes
    Enclosure       enclosure  `xml:"enclosure"`
    ItunesDuration  int        `xml:"itunes:duration"`     // total integer seconds
    ItunesEpisode   int        `xml:"itunes:episode"`
    ItunesEpisodeType string   `xml:"itunes:episodeType"`  // "full"
    ItunesExplicit  string     `xml:"itunes:explicit"`     // "true" | "false"
    ItunesImage     itunesImage `xml:"itunes:image"`
}

// WriteFeed renders the full RSS 2.0 + iTunes feed.
func WriteFeed(w io.Writer, s *Site, episodes []Episode) error {
    // ...populate the structs from s and episodes...
    io.WriteString(w, xml.Header)               // <?xml version="1.0" encoding="UTF-8"?>\n
    enc := xml.NewEncoder(w); enc.Indent("", "  ")
    return enc.Encode(theRSS)
}
```
Notes: `<itunes:duration>` = total integer seconds; `<itunes:explicit>` = `true`/`false` (use `EffectiveExplicit` per episode, site default at channel level); `<itunes:category text="...">` must be an exact Apple category string (optional nested subcategory); `<itunes:image href>` must be an absolute square JPEG/PNG URL 1400–3000 px (= `s.CoverURL()`); `<atom:link rel="self" type="application/rss+xml" href="<feed url>">` is Apple-required (hence the `xmlns:atom` decl); `<guid isPermaLink="false">` carries the stable id (= `e.EffectiveGUID()`), not the page URL; `<link>` per item = `s.AbsURL(e.PagePath())`; `<enclosure url>` = `s.AbsURL(e.MP3Path())` and `length` = the real MP3 byte size; `itunes:summary`/`itunes:subtitle` are deprecated → omitted. Element *case* matters; order is lenient.
**Heads-up for the implementer:** Go's `encoding/xml` accepts `xml:"itunes:author"`-style names and `xml:"xmlns:itunes,attr"` namespace-decl attrs and emits exactly those bytes (the common podcast-feed idiom) — but verify the output once with `xmllint --noout` and a podcast validator, since the encoder's namespace handling has historically had rough edges.

### `internal/render/sitemap.go`

```go
// WriteSitemap renders a sitemap.xml (urlset) covering "/", "/about.html", and every episode page.
// Built with encoding/xml structs (url{loc, lastmod}); lastmod = episode PubDate (or build time for static pages), W3C-date format.
func WriteSitemap(w io.Writer, s *Site, episodes []Episode) error
```

### `cmd/build.go` — into `<outputDir>` (default `<projectDir>/public`), flag `--no-clean`:
1. **Output-path safety guard** before any cleaning: resolve `outputDir` to an absolute, `filepath.Clean`'d path; refuse (with a clear error) if it is empty, `"/"`, the user's home dir, the project dir itself, or an ancestor of the project dir, or has fewer than 2 path components. Then: only `os.RemoveAll(outputDir)` if either it does not exist OR it contains a `.techgo-output` marker file (every build writes this marker after `MkdirAll`); otherwise error ("refusing to clean <dir>: not a techgo output dir — use --no-clean or point --output elsewhere"). `--no-clean` skips the removal entirely. Then `os.MkdirAll(outputDir)` + write `.techgo-output`.
2. `LoadSite`, `LoadEpisodes`.
3. Copy `static/` recursively into `outputDir` (helper `copyDir` via `filepath.WalkDir` + `io.Copy`, preserving the `css/` subtree etc.). This also carries through any per-episode `image:` files that live under `static/`.
4. **Media**: for each episode, ensure `media/out/<NNNN>.mp3|.mp4` exist — reuse `internal/media`'s idempotent transcode (so a plain `techgo build` works even if `techgo media` wasn't run first; print "skip" for up-to-date ones). **Requires `ffmpeg`+`ffprobe`** (error clearly if missing — they ship together) — `ffprobe` is needed for codec detection (remux-vs-reencode) and for duration. Copy each output to `<outputDir>/media/<NNNN>.mp3|.mp4`. Then `os.Stat` the outputs → set `MP3Size`/`MP4Size`; `media.ProbeDurationSeconds` → set `AudioDur`/`VideoDur` on the in-memory `Episode` slice. (Only if `ProbeDurationSeconds` *fails* on a file that exists — e.g. a malformed file — fall back to `0` for that episode with a warning; a missing `ffprobe` binary is a hard error, not a fallback.)
5. **Cover art**: if `<outputDir>/cover.png` not yet present, generate it from `site.CoverArt` via `internal/images` at 3000×3000 (center-cropped to square) → write `<outputDir>/cover.png`. (Skip with a warning if `site.CoverArt` doesn't exist — feed `<itunes:image>` will still point at it; the user is told to run `techgo images` / supply the source.)
6. **Render pages**: `index.html` (root), `episodes/<EffectiveSlug>.html` for each episode (with prev/next), `about.html`, `404.html`. (Default slug = zero-padded number, so without slugs you get `episodes/0001.html`.)
7. **Render feed + sitemap**: `feed.rss` (root) via `render.WriteFeed`; `sitemap.xml` (root) via `render.WriteSitemap`.
8. Print summary: N episodes, output dir, total bytes.

`cmd/serve.go`: `techgo serve` flag `--addr` (default `:8080`), serves `--output` dir with `http.FileServer(http.Dir(outputDir))` (it serves `index.html` for `/` automatically; episode pages are linked with `.html` so they just work). Optional: 404 handler serving `404.html`. Log the URL.

**Verify:** `techgo build && techgo serve`, open `http://localhost:8080/` → episodes newest-first, audio plays; open `/episodes/0001.html` → video plays, show notes render, download works. View source on both pages and confirm: unique `<title>`, `<meta name=description>`, `<link rel=canonical>`, full `og:*` set (with `og:type=article` + `article:published_time` on episode pages, `website` elsewhere), `twitter:card=summary_large_image` + `twitter:*`, favicons/manifest/RSS `<link>`, and a valid `<script type="application/ld+json">` (`PodcastSeries` on home, `PodcastEpisode` on episode pages — paste into the Schema.org validator / Google Rich Results Test; the OG tags into the Facebook Sharing Debugger or opengraph.xyz). `curl http://localhost:8080/feed.rss` → well-formed (`xmllint --noout`), and run it through a podcast feed validator (e.g. Cast Feed Validator / Podba.se). Confirm `<enclosure length>` == actual MP3 byte size, `<itunes:duration>` is integer seconds, dates are RFC-2822.

---

## Phase 6 — `techgo images`: web image set from one source PNG

**Create:** `internal/images/resize.go`, `internal/images/ico.go`, `cmd/images.go`.

`internal/images/resize.go`:
- `LoadPNG(path string) (image.Image, error)` (stdlib `image/png`).
- `ResizeSquare(src image.Image, size int) *image.RGBA` — center-crop `src` to a square, then `draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)` (`golang.org/x/image/draw`).
- `ResizeFit(src image.Image, w, h int, bg color.Color) *image.RGBA` — scale `src` to fit within `w×h` preserving aspect, composite centered on an opaque `bg` fill (used for the 1200×630 OG image).
- `SavePNG(img image.Image, path string) error` — `png.Encoder{CompressionLevel: png.BestCompression}` (writes no metadata chunks — "web ready" automatically).
- `SaveJPEG(img image.Image, path string) error` — `jpeg.Encode` with `Quality: 85` (optional, only if cover art as JPEG is desired).

`internal/images/ico.go`: `EncodeICO(w io.Writer, src image.Image, sizes ...int) error` — ICO format = `ICONDIR{reserved u16=0, type u16=1, count u16=N}` + N × `ICONDIRENTRY{w u8, h u8 (0⇒256), colorCount u8=0, reserved u8=0, planes u16=1, bitCount u16=32, bytesInRes u32, imageOffset u32}` + N concatenated PNG blobs (each made via `ResizeSquare`+`png.Encode`). Use `encoding/binary` (LittleEndian). ~40 lines. PNG-in-ICO is accepted by Vista+ and all modern browsers.

`cmd/images.go`: `techgo images [sourcePNG]` (default `site.CoverArt`), flags `--out` (default `<projectDir>/static`), `--bg` (default `#1a1a1a`). Validate: source must be PNG; warn if aspect far from 1:1 (icons are center-cropped); warn if shorter side < 3000 px (upscaling looks bad). Generate into `--out`:
- `favicon-16.png` 16×16, `favicon-32.png` 32×32
- `favicon.ico` (sizes 16, 32, 48)
- `apple-touch-icon.png` 180×180
- `icon-512.png` 512×512
- `og-image.png` 1200×630 (`ResizeFit` on `--bg`)
- `cover.png` 3000×3000 (iTunes cover art; ≥1400 required)
Print each filename + dimensions + byte size. (`techgo build` also auto-generates `public/cover.png` from `site.CoverArt` if missing, so `techgo images` is mainly for refreshing the committed `static/` icons.)

**Verify:** `techgo images path/to/logo.png`; `file static/*.png` shows expected dimensions; open `static/favicon.ico` in a viewer/browser tab; `static/cover.png` is exactly 3000×3000 at a reasonable size; `static/og-image.png` is 1200×630 with the logo centered on charcoal. `techgo build` then serves favicons + OG image correctly.

---

## Phase 7 — AWS Signature V4 signer (stdlib crypto)

**Create:** `internal/awssig/sigv4.go`.

```go
type Credentials struct{ AccessKeyID, SecretAccessKey, SessionToken string } // SessionToken usually ""

// SignRequest mutates req: sets X-Amz-Date; for service=="s3" also sets X-Amz-Content-Sha256;
// sets X-Amz-Security-Token if SessionToken != ""; computes and sets the Authorization header.
// body is the full request body (may be nil/empty). service e.g. "s3" | "cloudfront".
// region e.g. the bucket region (S3) or "us-east-1" (CloudFront).
func SignRequest(req *http.Request, body []byte, service, region string, creds Credentials, now time.Time) error
```
Algorithm (implement exactly):
1. `amzDate = now.UTC().Format("20060102T150405Z")`; `dateStamp = now.UTC().Format("20060102")`.
2. Ensure `req.Host`/`req.URL.Host` set. `req.Header.Set("X-Amz-Date", amzDate)`. `payloadHash = hex(sha256(body))` (empty body ⇒ `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`). If `service=="s3"`: `req.Header.Set("X-Amz-Content-Sha256", payloadHash)`. If `SessionToken!=""`: `req.Header.Set("X-Amz-Security-Token", SessionToken)`.
3. **Canonical URI** = the URL path, each segment URI-encoded per AWS rules (unreserved `A-Za-z0-9-._~` pass through; space ⇒ `%20`; everything else ⇒ uppercase `%XX`), `/` preserved between segments, no double-encoding; empty ⇒ `/`. Write your own `uriEncode(s string, encodeSlash bool)`.
4. **Canonical query string** = sort `req.URL.Query()` keys, URI-encode keys+values (`encodeSlash=true`), join `k=v` with `&` sorted by encoded key. (Empty for our requests, but implement it.)
5. **Canonical headers** = the headers you sign — at minimum `host`, all `x-amz-*` present, and `content-type` if present. Lowercase names; trim + collapse internal space runs in values; sort by name; emit `name:value\n` for each. `signedHeaders = strings.Join(sortedNames, ";")`.
6. **Canonical request** = `method + "\n" + canonicalURI + "\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash`.
7. **String to sign**: `credentialScope = dateStamp + "/" + region + "/" + service + "/aws4_request"`; `stringToSign = "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + hex(sha256(canonicalRequest))`.
8. **Signing key** (`hmacSHA256(key,data) = HMAC-SHA256`): `kDate = hmac("AWS4"+secret, dateStamp)` → `kRegion = hmac(kDate, region)` → `kService = hmac(kRegion, service)` → `kSigning = hmac(kService, "aws4_request")`; `signature = hex(hmac(kSigning, stringToSign))`.
9. `req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+AccessKeyID+"/"+credentialScope+", SignedHeaders="+signedHeaders+", Signature="+signature)`.

**Verify:** unit test against the published SigV4 test vector — key `AKIDEXAMPLE`, secret `wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY`, `20150830T123600Z`, region `us-east-1`, service `service`, `GET https://example.amazonaws.com/`, host header `example.amazonaws.com` — expected signature `5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7`. `go test ./internal/awssig` passes.

---

## Phase 8 — `techgo deploy`: S3 upload + CloudFront invalidation

**Create:** `internal/awss3/s3.go`, `internal/awscf/cloudfront.go`, `cmd/deploy.go`.

`internal/awss3/s3.go`:
```go
type Client struct{ Bucket, Region, Prefix string; Creds awssig.Credentials; HTTP *http.Client } // Prefix optional (no leading slash, trailing slash added internally)
func (c *Client) PutObject(ctx context.Context, key string, body []byte, contentType, cacheControl string) error
func (c *Client) GetObject(ctx context.Context, key string) (body []byte, found bool, err error) // found=false on 404 — used for the deploy marker check
func (c *Client) ListObjects(ctx context.Context) ([]string, error) // all keys under Prefix, ListObjectsV2 + pagination; returns keys *relative to Prefix*
func (c *Client) DeleteObject(ctx context.Context, key string) error                 // HTTP DELETE
```
All methods prepend `c.Prefix` to the object key. URLs are virtual-hosted regional: `https://<bucket>.s3.<region>.amazonaws.com/<prefix><key>` (`us-east-1` ⇒ `s3.us-east-1.amazonaws.com`).
- `PutObject`: `PUT`, body `bytes.NewReader(body)`; set `Content-Type`, `Cache-Control`, `req.ContentLength`; `awssig.SignRequest(req, body, "s3", c.Region, c.Creds, time.Now())`; non-2xx ⇒ read+return the XML error body.
- `GetObject`: `GET`, empty body; 200 ⇒ return body; 404 ⇒ `found=false`; other ⇒ error.
- `ListObjects`: `GET .../?list-type=2&prefix=<c.Prefix>` (+ `&continuation-token=<tok>` while `IsTruncated`); empty body; parse `ListBucketResult` XML with `encoding/xml`, collect `<Contents><Key>`, strip `c.Prefix` from each.
- `DeleteObject`: `DELETE`, empty body, signed; expect `204` (also accept `200`).

`internal/awscf/cloudfront.go`:
```go
type Client struct{ Creds awssig.Credentials; HTTP *http.Client }
func (c *Client) CreateInvalidation(ctx context.Context, distributionID string, paths []string) (id string, err error)
```
Body (build via `encoding/xml` structs or `fmt.Sprintf`):
```xml
<?xml version="1.0" encoding="UTF-8"?>
<InvalidationBatch xmlns="http://cloudfront.amazonaws.com/doc/2020-05-31/">
  <CallerReference>techgo-<time.Now().UnixNano()></CallerReference>
  <Paths><Items><Path>/*</Path></Items><Quantity>1</Quantity></Paths>
</InvalidationBatch>
```
(`Quantity` must equal `len(Items)`; `CallerReference` must be unique per call. **Normalize every path** so it starts with `/` — prepend `/` if missing.) URL `https://cloudfront.amazonaws.com/2020-05-31/distribution/<distributionID>/invalidation`, method `POST`, header `Content-Type: text/xml`, `req.ContentLength` set; `awssig.SignRequest(req, xmlBody, "cloudfront", "us-east-1", ...)` (service `cloudfront`, region `us-east-1` regardless of bucket region; CloudFront doesn't need the `x-amz-content-sha256` *header* — `SignRequest` only sets it for `service=="s3"`). HTTP 201 ⇒ parse `<Invalidation><Id>...</Id></Invalidation>` with `encoding/xml`, return `Id`; else return the XML error body.

`cmd/deploy.go` — flags `--output` (built dir, default `public`), `--dry-run`, `--no-invalidate`, `--paths` (default `/*`), `--delete` (default **true** — mirror the build into S3, like `aws s3 sync --delete`):
1. `ParseDotEnv(<projectDir>/.env)` → require `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_REGION`, `S3_BUCKET`, `CLOUDFRONT_DISTRIBUTION_ID`; **optional `AWS_SESSION_TOKEN`** (for STS / assumed-role / IAM Identity Center creds — pass it into `awssig.Credentials.SessionToken`, which makes `SignRequest` add `X-Amz-Security-Token`); **optional `S3_PREFIX`** (default `""` = bucket root; if set, all uploads/lists/deletes/invalidation are scoped under it — and the CloudFront distribution's origin "Origin Path" must be `/<S3_PREFIX>`, see Phase 9 note). Clear error on any required one missing.
2. `filepath.WalkDir(outputDir)`; for each file: S3 key (relative to `outputDir`, forward slashes) → read bytes. Build the set of local keys. Skip the `.techgo-output` marker (don't upload it).
3. **Content-Type**: `mime.TypeByExtension` with explicit overrides — `.html`→`text/html; charset=utf-8`, `.css`→`text/css; charset=utf-8`, `.js`→`text/javascript`, `.rss`→`application/rss+xml`, `.mp3`→`audio/mpeg`, `.mp4`→`video/mp4`, `.png`→`image/png`, `.jpg/.jpeg`→`image/jpeg`, `.ico`→`image/x-icon`, `.svg`→`image/svg+xml`, `.json`→`application/json`, `.xml`→`application/xml`, `.txt`→`text/plain; charset=utf-8`, `.webmanifest`→`application/manifest+json`, fallback `application/octet-stream`.
4. **Cache-Control**: `*.html`/`404.html` → `public, max-age=0, must-revalidate`; `feed.rss` → `public, max-age=300, must-revalidate`; `media/*.mp3`/`media/*.mp4` → `public, max-age=86400, must-revalidate` (**not `immutable`** — an episode may be re-transcoded/corrected after publishing; numbered filenames don't change, so `immutable` would strand stale copies in browsers even after a CloudFront invalidation. 1 day + must-revalidate, plus the deploy-time `/*` invalidation, means a corrected episode is live at the edge immediately and in browsers within a day. If bandwidth ever matters, switch to content-hashed media filenames and then `immutable` is safe — note in code); `sitemap.xml` → `public, max-age=3600`; everything else (css, favicons, og-image, cover.png, webmanifest, robots.txt) → `public, max-age=86400`. (The post-deploy `/*` invalidation busts the edge cache regardless.)
5. **Deploy-marker / prune guard:** before any deletes, `GetObject(".techgo-deploy")`. If it exists OR `ListObjects` returns zero keys (empty bucket/prefix → first deploy, nothing to prune), pruning is permitted; otherwise pruning is **refused** with a clear error ("S3 prefix s3://<bucket>/<prefix> doesn't look like a techgo deploy target — no .techgo-deploy marker and not empty; re-run with --delete=false, or remove unrelated objects and re-deploy"). (Pruning still proceeds with `--delete=false` simply not pruning; uploads always happen.)
6. For each local file: `s3.PutObject(...)` (`--dry-run` ⇒ just print `PUT <key> (<size>, <ct>, <cc>)`). Then write/refresh the `.techgo-deploy` marker object (small text: timestamp + tool version), `no-cache`.
7. **Prune stale objects** (only if `--delete` and the guard in step 5 passed): `existing := s3.ListObjects(ctx)`; for each `k` in `existing` not in the local key set and not `.techgo-deploy`, print `DELETE <k>` and `s3.DeleteObject(ctx, k)` (`--dry-run` ⇒ just print). (This is why the build is a clean build — local `public/` is the source of truth.)
8. Unless `--no-invalidate`: `cf.CreateInvalidation(ctx, distID, normalizePaths(strings.Split(paths,",")))`; print the invalidation Id.
9. Print `Deployed → <site.BaseURL>`.

**Verify:** with `.env` pointing at a real test bucket + distribution: `techgo deploy --dry-run` lists every PUT (with sensible Content-Type/Cache-Control) and every DELETE; a real `techgo deploy` then `curl -I https://<domain>/index.html` shows `max-age=0`, `curl -I https://<domain>/media/0001.mp3` shows `audio/mpeg` + `max-age=86400` (no `immutable`); delete a `data/*.yml`, `techgo build && techgo deploy` → the old `media/NNNN.mp3|.mp4` and `episodes/NNNN.html` are removed from S3; the CloudFront invalidation completes. With no AWS access during dev, the real correctness gate is the Phase-7 SigV4 vector test passing.

---

## Phase 9 — CloudFormation template

**Create:** `infra/cloudformation.yml` (description comment block documents the deploy steps below).

**Parameters:** `DomainNames` (**CommaDelimitedList** — the full list of CNAMEs/aliases for the distribution, primary first, e.g. `techgo.example.com,www.techgo.example.com`; a single value is fine); `AcmCertificateArn` (String — **must be an ACM cert in us-east-1** covering every name in `DomainNames`; NOT created by this template); `BucketName` (String); `PriceClass` (String, default `PriceClass_100`). *(No separate "additional aliases" param and no `Fn::If` gymnastics — `Aliases` takes the list directly.)*

**Resources:**
- `SiteBucket` (`AWS::S3::Bucket`, private — no website hosting): `OwnershipControls.Rules: [{ObjectOwnership: BucketOwnerEnforced}]` (ACLs off, required for OAC); `PublicAccessBlockConfiguration` all `true`; `BucketEncryption` AES256.
- `OAC` (`AWS::CloudFront::OriginAccessControl`): `OriginAccessControlConfig{ Name: !Sub "${BucketName}-oac", OriginAccessControlOriginType: s3, SigningBehavior: always, SigningProtocol: sigv4 }`.
- `SecurityHeaders` (`AWS::CloudFront::ResponseHeadersPolicy`): `ResponseHeadersPolicyConfig{ Name: !Sub "${BucketName}-security-headers", SecurityHeadersConfig: { StrictTransportSecurity: {AccessControlMaxAgeSec: 63072000, IncludeSubdomains: true, Preload: false, Override: true}, ContentTypeOptions: {Override: true}, FrameOptions: {FrameOption: DENY, Override: true}, ReferrerPolicy: {ReferrerPolicy: strict-origin-when-cross-origin, Override: true}, XSSProtection: {Protection: true, ModeBlock: true, Override: true} } }`. (Alternative: reference the AWS-managed `SecurityHeadersPolicy`, id `67f7725c-6f97-4210-82d7-5512b31e9d03`, instead of defining one — the custom one is here so HSTS `max-age`/`includeSubdomains` is explicit and tweakable.)
- `Distribution` (`AWS::CloudFront::Distribution`) `DistributionConfig`: `Enabled: true`; `Comment: !Sub ["Tech.Go - ${D}", {D: !Select [0, !Ref DomainNames]}]`; `DefaultRootObject: index.html`; `PriceClass: !Ref PriceClass`; `HttpVersion: http2and3`; `Aliases: !Ref DomainNames`; `ViewerCertificate{ AcmCertificateArn: !Ref AcmCertificateArn, SslSupportMethod: sni-only, MinimumProtocolVersion: TLSv1.2_2021 }`; `Origins: [{ Id: s3origin, DomainName: !GetAtt SiteBucket.RegionalDomainName, OriginAccessControlId: !GetAtt OAC.Id, S3OriginConfig: { OriginAccessIdentity: "" } }]` (empty `OriginAccessIdentity` is required with OAC; `RegionalDomainName` is the REST endpoint, not the website endpoint. **If you set `S3_PREFIX` in `.env`**, add `OriginPath: "/<prefix>"` to this origin so CloudFront serves from `s3://bucket/<prefix>/...` — leave it unset for a bucket-root deploy); `DefaultCacheBehavior{ TargetOriginId: s3origin, ViewerProtocolPolicy: redirect-to-https, AllowedMethods: [GET, HEAD], CachedMethods: [GET, HEAD], Compress: true, CachePolicyId: 658327ea-f89d-4fab-a63d-7e88639e58f6, ResponseHeadersPolicyId: !Ref SecurityHeaders }` (`658327ea-...` = AWS managed "CachingOptimized"); `CustomErrorResponses: [{ErrorCode: 403, ResponseCode: 404, ResponsePagePath: /404.html, ErrorCachingMinTTL: 60}, {ErrorCode: 404, ResponseCode: 404, ResponsePagePath: /404.html, ErrorCachingMinTTL: 60}]` (REST S3 origin returns 403 for missing keys — that's the important mapping; no SPA-style 200 rewrites, real 404s are correct for a content site).
- `BucketPolicy` (`AWS::S3::BucketPolicy`): allow `s3:GetObject` on `!Sub "${SiteBucket.Arn}/*"` to `Principal{Service: cloudfront.amazonaws.com}` with `Condition.StringEquals."AWS:SourceArn": !Sub "arn:aws:cloudfront::${AWS::AccountId}:distribution/${Distribution.Id}"` (the OAC trust pattern).

**Outputs:** `BucketNameOut: !Ref SiteBucket` (→ `.env` `S3_BUCKET`); `DistributionId: !Ref Distribution` (→ `.env` `CLOUDFRONT_DISTRIBUTION_ID`); `DistributionDomainName: !GetAtt Distribution.DomainName` (→ point DNS CNAME/alias at this).

**Operator steps (in the template's description comment):** (1) request + validate an ACM cert in **us-east-1** covering all the names you'll use; (2) `aws cloudformation deploy --template-file infra/cloudformation.yml --stack-name techgo --parameter-overrides "DomainNames=techgo.example.com,www.techgo.example.com" AcmCertificateArn=arn:... BucketName=techgo-site`; (3) copy the two output IDs into `.env` (and set `S3_PREFIX` only if you added an `OriginPath`); (4) create Route 53 aliases (or external CNAMEs) for each name → `DistributionDomainName`; (5) `techgo images <logo.png>` (once) → commit `static/` icons; (6) `techgo build && techgo deploy`.

**Verify:** `aws cloudformation validate-template --template-body file://infra/cloudformation.yml` passes; deployed to a test account: direct `https://<bucket>.s3.<region>.amazonaws.com/index.html` → 403, the CloudFront URL serves `index.html`, a bogus path → `/404.html` with HTTP 404, HTTPS works with the cert, and `curl -I https://<domain>/` shows `strict-transport-security`, `x-content-type-options: nosniff`, `x-frame-options`, `referrer-policy`.

---

## Cross-cutting notes for implementation

- **Dependency budget:** `go mod tidy` should show only `github.com/spf13/cobra`, `gopkg.in/yaml.v3`, `golang.org/x/image` (+ cobra's transitive `spf13/pflag`, `inconshreveable/mousetrap`). Everything else stdlib: `net/http`, `net/url`, `crypto/hmac`, `crypto/sha256`, `encoding/xml`, `encoding/json`, `encoding/binary`, `html/template`, `image/png`, `image/jpeg`, `mime`, `os/exec`, `path/filepath`.
- **XML safety:** the RSS feed and `sitemap.xml` are built with `encoding/xml` (auto-escapes element text + attributes), not text templates — `text/template` does not escape XML. Show notes use a custom `cdata` marshaler. Only `html/template` (which auto-escapes HTML) is used for the web pages.
- **Trusted HTML:** episode `description` is rendered via `template.HTML` (no escaping) — author-written show notes; document the assumption in code. `shortDescription` is plain text and IS escaped (both in HTML pages via `html/template` and in the feed via `encoding/xml`).
- **Media cache policy:** numbered media files (`media/NNNN.mp3|.mp4`) are served with a finite `max-age` (1 day) + `must-revalidate`, **not** `immutable`, so a re-transcoded episode propagates (immediately at the CloudFront edge via the deploy-time `/*` invalidation, within a day in browsers). Switching to content-hashed media filenames is the prerequisite for ever using `immutable` here.
- **Destructive-op guards:** `build` validates `--output` (not `/`, `$HOME`, the project dir, an ancestor, or a <2-component path) and only `os.RemoveAll`s a dir that contains the `.techgo-output` marker it writes. `deploy --delete` (default on) prunes S3 objects absent from the local build, but **only after** confirming the target carries a `.techgo-deploy` marker object (or is empty) — refuses otherwise; supports `S3_PREFIX` to scope all operations; always preview with `--dry-run` first.
- **Slugs:** episode page filename / canonical URL / sitemap entry / RSS `<link>` all use `EffectiveSlug()` (= the `slug:` field, or the zero-padded number if omitted — so without slugs everything is `episodes/0001.html` as before). Media files stay number-keyed (`media/0001.mp3|.mp4`) so the RSS enclosure URL is stable. Duplicate slugs (and duplicate numbers) are a load-time error.
- **Validation, early:** `LoadSite` checks `baseURL` (and `coverArtURL` if set) is a trimmed, absolute `http(s)://host` with no query/fragment/userinfo and strips a trailing `/` from the path; both files are decoded with `KnownFields(true)` so typo'd keys fail. `LoadEpisodes` checks per-episode `number>0`, slug charset + uniqueness, non-empty title/pubDate/source paths, and that the `image:` site-relative path exists under `static/` (and normalizes it); `CreateInvalidation` normalizes paths to start with `/`. Episode `explicit` is `*bool` so "inherit site default" vs "explicit override" are distinct.
- **Determinism:** `LoadEpisodes` sorts by `number` descending; always `.UTC()` before RFC-2822 formatting in the feed.
- **Errors:** every subcommand uses cobra `RunE` → non-zero exit on failure with actionable messages ("ffmpeg not found in PATH", "missing AWS_SECRET_ACCESS_KEY in .env", etc.).
- **Highest-value tests:** the SigV4 vector test (Phase 7); config round-trip + validation tests (Phase 1); and a feed test (Phase 5) that builds the feed for an episode whose title/description contain `&`, `<`, `>`, `"`, then re-parses `public/feed.rss` with `encoding/xml` and asserts the round-trip. The rest is verified via `techgo build` + `techgo serve` + manual inspection + a podcast feed validator.

## End-to-end verification (after all phases)
1. `go build ./... && go vet ./... && go test ./...` — clean.
2. `techgo images /home/ryan/Pictures/image.png` — populates `static/` icons (note: this image is only 1080×1080, so `cover.png` will be an upscale — fine for a smoke test; real cover art should be ≥3000 px).
3. `techgo new "Pilot"` → edit `data/0002.yml`, point `sourceWav`/`sourceMp4` at real files in `media/src/`.
4. `techgo media` — transcodes; re-run shows "skip".
5. `techgo build && techgo serve` — open `http://localhost:8080/`: dark themed, episodes newest-first, inline audio plays; `/episodes/0001.html`: video plays, show notes render, MP3 download works; `xmllint --noout public/feed.rss` clean; feed validates in a podcast validator.
6. `aws cloudformation validate-template --template-body file://infra/cloudformation.yml` passes; (optionally) deploy the stack, fill `.env`, `techgo deploy --dry-run` then `techgo deploy`.
