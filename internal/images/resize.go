// Package images generates the site's web image set — favicons, app icons, the
// social-share card, and the square podcast cover art — from a single source
// PNG. Downscales use golang.org/x/image/draw's Catmull-Rom resampler for clean
// results; outputs carry no ancillary metadata chunks, so they're "web ready"
// by construction. Writes are atomic (sibling temp file + rename on success),
// so a failed encode never leaves a partial file behind.
//
// resize.go holds the resampling and PNG primitives; ico.go adds the PNG-in-ICO
// encoder. The `techgo images` command (cmd/images.go) drives them all from
// one source PNG, and `techgo build` calls LoadPNG/ResizeSquare/SavePNG to
// auto-generate public/cover.png when site.coverArt is set.
package images

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"

	xdraw "golang.org/x/image/draw"
)

// LoadPNG decodes the PNG file at path.
func LoadPNG(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decoding %s as PNG: %w", path, err)
	}
	return img, nil
}

// ResizeSquare center-crops src to its largest centered square and scales that
// to size×size using Catmull-Rom resampling, returning a fresh RGBA image. Used
// for the square podcast cover art and the favicon/app-icon set. A size below 1
// is clamped to 1 so the result is always a valid, non-empty image.
func ResizeSquare(src image.Image, size int) *image.RGBA {
	if size < 1 {
		size = 1
	}
	sq := centerCropSquare(src)
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), sq, sq.Bounds(), xdraw.Over, nil)
	return dst
}

// ResizeFit scales src to the largest size that fits inside w×h while
// preserving its aspect ratio, then composites that scaled copy centered on a
// solid bg fill — used for the 1200×630 social card, whose aspect ratio
// differs from typical square cover art. Dimensions below 1 are clamped to 1.
func ResizeFit(src image.Image, w, h int, bg color.Color) *image.RGBA {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.Draw(dst, dst.Bounds(), image.NewUniform(bg), image.Point{}, xdraw.Src)
	sb := src.Bounds()
	if sb.Empty() {
		return dst
	}
	scale := float64(w) / float64(sb.Dx())
	if s := float64(h) / float64(sb.Dy()); s < scale {
		scale = s
	}
	dw := int(float64(sb.Dx()) * scale)
	dh := int(float64(sb.Dy()) * scale)
	if dw < 1 {
		dw = 1
	}
	if dh < 1 {
		dh = 1
	}
	ox, oy := (w-dw)/2, (h-dh)/2
	target := image.Rect(ox, oy, ox+dw, oy+dh)
	xdraw.CatmullRom.Scale(dst, target, src, sb, xdraw.Over, nil)
	return dst
}

// SavePNG writes img to path as a PNG with maximum compression and no
// ancillary metadata chunks. See writeAtomic for the atomicity contract: a
// failed encode never leaves a partial/corrupt file at path, which matters
// because `techgo build` skips regenerating an existing public/cover.png.
func SavePNG(img image.Image, path string) error {
	return writeAtomic(path, func(w io.Writer) error {
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(w, img); err != nil {
			return fmt.Errorf("encoding %s as PNG: %w", path, err)
		}
		return nil
	})
}

// centerCropSquare returns the largest centered square region of src. When src
// supports SubImage (the stdlib image types do) the result shares src's pixels;
// otherwise the region is copied into a fresh RGBA.
func centerCropSquare(src image.Image) image.Image {
	b := src.Bounds()
	side := b.Dx()
	if b.Dy() < side {
		side = b.Dy()
	}
	x0 := b.Min.X + (b.Dx()-side)/2
	y0 := b.Min.Y + (b.Dy()-side)/2
	r := image.Rect(x0, y0, x0+side, y0+side)
	if sub, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(r)
	}
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	xdraw.Draw(dst, dst.Bounds(), src, r.Min, xdraw.Src)
	return dst
}

// writeAtomic creates a sibling temp file in path's directory, hands its
// writer to write, and renames the temp over path only after write returns
// nil and the file is closed cleanly. A failed write (encode error, panic,
// disk full) leaves no partial file at path — the deferred Remove cleans the
// temp. Used by SavePNG and SaveICO.
func writeAtomic(path string, write func(io.Writer) error) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	tmp, err := os.CreateTemp(dir, "."+strings.TrimSuffix(base, ext)+".tmp-*"+ext)
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed; removes a leftover/partial file otherwise
	if err := write(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file for %s: %w", path, err)
	}
	// os.CreateTemp creates the file 0600; match a normal output (best effort).
	_ = os.Chmod(tmpPath, 0o644)
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("finalizing %s: %w", path, err)
	}
	return nil
}
