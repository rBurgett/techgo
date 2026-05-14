package images

import (
	"image"
	"image/color"
	"testing"
)

// solidImage returns a w×h *image.RGBA filled with c.
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i+0] = c.R
		img.Pix[i+1] = c.G
		img.Pix[i+2] = c.B
		img.Pix[i+3] = c.A
	}
	return img
}

func TestResizeSquareDimensions(t *testing.T) {
	cases := []struct{ srcW, srcH, want int }{
		{100, 100, 32}, // already square
		{200, 100, 32}, // wider than tall (center-crop)
		{100, 200, 32}, // taller than wide
		{1, 1, 64},     // tiny source → upscale to 64×64
	}
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	for _, c := range cases {
		s := solidImage(c.srcW, c.srcH, color.RGBA{255, 0, 0, 255})
		got := ResizeSquare(s, c.want)
		if got.Bounds().Dx() != c.want || got.Bounds().Dy() != c.want {
			t.Errorf("ResizeSquare(%dx%d, %d) = %dx%d, want %dx%d square",
				c.srcW, c.srcH, c.want, got.Bounds().Dx(), got.Bounds().Dy(), c.want, c.want)
		}
	}
	// size <= 0 clamps to 1 instead of producing a degenerate image.
	if got := ResizeSquare(src, 0); got.Bounds().Dx() != 1 || got.Bounds().Dy() != 1 {
		t.Errorf("ResizeSquare(_, 0) = %v, want a 1×1 image (clamped)", got.Bounds())
	}
}

func TestResizeFitDimensionsAndLetterbox(t *testing.T) {
	// A 1000×1000 red square fit into 1200×630 with black background. The
	// scaled red region preserves aspect, so it's 630×630 centered horizontally;
	// the left/right ~285 px columns are the black letterbox.
	red := color.RGBA{255, 0, 0, 255}
	black := color.RGBA{0, 0, 0, 255}
	src := solidImage(1000, 1000, red)
	got := ResizeFit(src, 1200, 630, black)
	if got.Bounds().Dx() != 1200 || got.Bounds().Dy() != 630 {
		t.Fatalf("ResizeFit dimensions = %v, want 1200x630", got.Bounds())
	}
	// Top-left corner is inside the letterbox: expect black.
	if c := got.RGBAAt(0, 0); c.R != 0 || c.G != 0 || c.B != 0 {
		t.Errorf("ResizeFit top-left = %v, want black (letterbox)", c)
	}
	// Center is inside the scaled red region: expect (close to) red. Catmull-Rom
	// is exact on a flat-color region, so this should be precisely red.
	if c := got.RGBAAt(600, 315); c.R < 250 || c.G > 5 || c.B > 5 {
		t.Errorf("ResizeFit center = %v, want red", c)
	}
}

func TestResizeFitTallSource(t *testing.T) {
	// A tall 100×1000 source fit into 1200×630 scales to roughly 63×630 (height-
	// limited), centered horizontally. Confirm letterbox on the left edge and
	// foreground near the center.
	src := solidImage(100, 1000, color.RGBA{0, 255, 0, 255})
	got := ResizeFit(src, 1200, 630, color.RGBA{30, 30, 30, 255})
	if got.Bounds().Dx() != 1200 || got.Bounds().Dy() != 630 {
		t.Fatalf("dimensions = %v", got.Bounds())
	}
	if c := got.RGBAAt(10, 315); c.R != 30 || c.G != 30 || c.B != 30 {
		t.Errorf("left-edge letterbox = %v, want #1e1e1e", c)
	}
	if c := got.RGBAAt(600, 315); c.G < 250 {
		t.Errorf("center = %v, want green-dominant", c)
	}
}

func TestResizeFitClampsToOne(t *testing.T) {
	src := solidImage(10, 10, color.RGBA{255, 255, 255, 255})
	got := ResizeFit(src, 0, -5, color.RGBA{0, 0, 0, 255})
	if got.Bounds().Dx() != 1 || got.Bounds().Dy() != 1 {
		t.Errorf("ResizeFit(_, 0, -5, _) = %v, want 1×1 (clamped)", got.Bounds())
	}
}
