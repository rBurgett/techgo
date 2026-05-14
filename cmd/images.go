package cmd

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/rBurgett/techgo/internal/config"
	"github.com/rBurgett/techgo/internal/images"
	"github.com/spf13/cobra"
)

var (
	imagesOutFlag string
	imagesBgFlag  string
)

var imagesCmd = &cobra.Command{
	Use:   `images [sourcePNG]`,
	Short: "Generate the favicon, app-icon, social card, and cover-art set from one PNG",
	Long: `images turns one source PNG (the show's logo) into every other image the
site needs:

  favicon-16.png         16×16  PNG
  favicon-32.png         32×32  PNG
  favicon.ico            multi-resolution PNG-in-ICO at 16/32/48
  apple-touch-icon.png   180×180 PNG (iOS home screen)
  icon-512.png           512×512 PNG (PWA / android-chrome)
  og-image.png           1200×630 PNG, source letterboxed on --bg
  cover.png              3000×3000 PNG (Apple Podcasts / RSS <itunes:image>)

Outputs land in --out (default <projectDir>/static), the directory "techgo
build" copies verbatim into the built site. The square outputs are
center-cropped from the source; og-image.png preserves the source's aspect
ratio and pads with --bg. Pass the source as the first argument, or omit it to
use site.coverArt from site.yml. Re-run any time the source changes; this
command is intended to be run occasionally and its results committed to the
repo.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runImages,
}

func init() {
	imagesCmd.Flags().StringVar(&imagesOutFlag, "out", "",
		"output directory for the generated images (default <projectDir>/static)")
	imagesCmd.Flags().StringVar(&imagesBgFlag, "bg", "#1a1a1a",
		"background color (hex #RRGGBB or #RRGGBBAA) for letterboxed outputs like og-image.png")
	rootCmd.AddCommand(imagesCmd)
}

func runImages(cmd *cobra.Command, args []string) error {
	projectDir, err := ProjectDir()
	if err != nil {
		return err
	}

	srcPath, err := resolveImagesSource(projectDir, args)
	if err != nil {
		return err
	}

	outDir := imagesOutFlag
	if outDir == "" {
		outDir = filepath.Join(projectDir, "static")
	} else if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(projectDir, filepath.FromSlash(outDir))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", outDir, err)
	}

	bg, err := parseHexColor(imagesBgFlag)
	if err != nil {
		return fmt.Errorf("--bg %w", err)
	}

	src, err := images.LoadPNG(srcPath)
	if err != nil {
		return fmt.Errorf("loading source: %w", err)
	}

	w := cmd.OutOrStdout()
	warnSourceShape(w, src.Bounds())

	type job struct {
		name string
		gen  func() error
		dim  string // for the summary table (the .ico has no single dimension)
	}
	jobs := []job{
		{"favicon-16.png", savePNGAt(src, 16, filepath.Join(outDir, "favicon-16.png")), "16x16"},
		{"favicon-32.png", savePNGAt(src, 32, filepath.Join(outDir, "favicon-32.png")), "32x32"},
		{"favicon.ico", func() error { return images.SaveICO(src, filepath.Join(outDir, "favicon.ico"), 16, 32, 48) }, "16/32/48"},
		{"apple-touch-icon.png", savePNGAt(src, 180, filepath.Join(outDir, "apple-touch-icon.png")), "180x180"},
		{"icon-512.png", savePNGAt(src, 512, filepath.Join(outDir, "icon-512.png")), "512x512"},
		{"og-image.png", func() error {
			return images.SavePNG(images.ResizeFit(src, 1200, 630, bg), filepath.Join(outDir, "og-image.png"))
		}, "1200x630"},
		{"cover.png", savePNGAt(src, 3000, filepath.Join(outDir, "cover.png")), "3000x3000"},
	}

	for _, j := range jobs {
		if err := j.gen(); err != nil {
			return fmt.Errorf("generating %s: %w", j.name, err)
		}
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "FILE\tDIMENSIONS\tSIZE")
	for _, j := range jobs {
		p := filepath.Join(outDir, j.name)
		size := "?"
		if info, err := os.Stat(p); err == nil {
			size = humanBytes(info.Size())
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", j.name, j.dim, size)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\nwrote %d image(s) to %s\n", len(jobs), outDir)
	return nil
}

// resolveImagesSource picks the source PNG: the positional arg if present
// (resolved against projectDir when relative), otherwise site.coverArt from
// site.yml. An empty/missing source is an actionable error, not a panic.
func resolveImagesSource(projectDir string, args []string) (string, error) {
	if len(args) == 1 {
		p := args[0]
		if !filepath.IsAbs(p) {
			p = filepath.Join(projectDir, filepath.FromSlash(p))
		}
		return p, nil
	}
	site, err := config.LoadSite(projectDir)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(site.CoverArt) == "" {
		return "", fmt.Errorf("no source given and site.coverArt is unset in site.yml — pass the path as the first argument or set coverArt")
	}
	return filepath.Join(projectDir, filepath.FromSlash(site.CoverArt)), nil
}

// warnSourceShape prints a non-fatal warning if the source is far from square
// (square icons will be center-cropped, losing content) or its shorter side is
// below the largest output (cover.png at 3000), which will produce a soft
// upscale.
func warnSourceShape(w io.Writer, sb image.Rectangle) {
	sw, sh := sb.Dx(), sb.Dy()
	if sw <= 0 || sh <= 0 {
		return
	}
	ratio := float64(sw) / float64(sh)
	if ratio < 1 {
		ratio = 1 / ratio
	}
	if ratio > 1.1 {
		fmt.Fprintf(w, "warning: source is %dx%d (aspect %.2f:1); square icons will be center-cropped\n", sw, sh, ratio)
	}
	shortSide := sw
	if sh < shortSide {
		shortSide = sh
	}
	if shortSide < 3000 {
		fmt.Fprintf(w, "warning: source shorter side is %d px; cover.png (3000x3000) will be upscaled — fine for a smoke test, less so for production cover art\n", shortSide)
	}
}

// savePNGAt returns a closure that resizes src to a size×size PNG and writes
// it to path — packaged so the job table reads as a flat list of one-liners.
func savePNGAt(src image.Image, size int, path string) func() error {
	return func() error {
		return images.SavePNG(images.ResizeSquare(src, size), path)
	}
}

// parseHexColor parses CSS-style #RRGGBB or #RRGGBBAA into a color.NRGBA — the
// *non*-alpha-premultiplied variant, matching the CSS interpretation of the
// hex digits. (Returning color.RGBA would silently corrupt any value with
// alpha < 0xFF, since color.RGBA is contractually premultiplied; the draw
// routines call .RGBA() which premultiplies NRGBA correctly.) A missing alpha
// defaults to fully opaque. The leading '#' is required.
func parseHexColor(s string) (color.Color, error) {
	raw := s
	if !strings.HasPrefix(s, "#") {
		return nil, fmt.Errorf("invalid color %q: expected #RRGGBB or #RRGGBBAA", raw)
	}
	s = s[1:]
	if len(s) != 6 && len(s) != 8 {
		return nil, fmt.Errorf("invalid color %q: expected #RRGGBB or #RRGGBBAA", raw)
	}
	parseByte := func(off int) (uint8, error) {
		v, err := strconv.ParseUint(s[off:off+2], 16, 8)
		return uint8(v), err
	}
	r, err := parseByte(0)
	if err != nil {
		return nil, fmt.Errorf("invalid color %q: %w", raw, err)
	}
	g, err := parseByte(2)
	if err != nil {
		return nil, fmt.Errorf("invalid color %q: %w", raw, err)
	}
	b, err := parseByte(4)
	if err != nil {
		return nil, fmt.Errorf("invalid color %q: %w", raw, err)
	}
	a := uint8(0xff)
	if len(s) == 8 {
		if a, err = parseByte(6); err != nil {
			return nil, fmt.Errorf("invalid color %q: %w", raw, err)
		}
	}
	return color.NRGBA{R: r, G: g, B: b, A: a}, nil
}
