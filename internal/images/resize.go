// Package images generates the site's web image set — favicons, app icons, the
// social-share card, and the square podcast cover art — from a single source PNG.
// Downscales use golang.org/x/image/draw's Catmull-Rom resampler for clean
// results, and the PNG encoder writes no ancillary metadata chunks, so the
// outputs are "web ready" by construction.
//
// This file holds the resampling/encoding primitives. The favicon.ico encoder
// and the `techgo images` command (Phase 6) build on top of them; `techgo build`
// uses LoadPNG/ResizeSquare/SavePNG to auto-generate public/cover.png.
package images

import (
	"fmt"
	"image"
	"image/png"
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

// SavePNG writes img to path as a PNG with maximum compression and no ancillary
// metadata chunks. The write is atomic: the encoded bytes go to a sibling temp
// file first and are renamed into place only on success, so a failed encode
// (out of disk, panic, signal) never leaves a partial/corrupt PNG at path. That
// matters because `techgo build` skips regenerating an existing public/cover.png,
// so a half-written one would otherwise persist across subsequent builds.
func SavePNG(img image.Image, path string) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	tmp, err := os.CreateTemp(dir, "."+strings.TrimSuffix(base, ext)+".tmp-*"+ext)
	if err != nil {
		return fmt.Errorf("creating temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op once renamed; removes a leftover/partial file otherwise
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(tmp, img); err != nil {
		tmp.Close()
		return fmt.Errorf("encoding %s as PNG: %w", path, err)
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
