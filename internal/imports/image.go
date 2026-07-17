package imports

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"

	// Decoder registrations. gen2brain/{avif,webp} register their formats too,
	// so x/image/webp is deliberately absent: pulling it in would register a
	// second "webp" matcher for no gain, and gen2brain's is the one that can
	// also encode.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "github.com/gen2brain/avif"
	_ "github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"
)

// thumbSize is package.py's THUMB. The thumbnail is squashed to a square rather
// than fitted to the aspect ratio, which is what makes the comparison
// scale-invariant; the aspect gate in grouping is what keeps the squash from
// matching two differently-shaped images.
const thumbSize = 64

// lanczos3 reproduces PIL's Image.LANCZOS, which x/image/draw has no preset for
// (it ships Nearest/BiLinear/CatmullRom only). x/image's Kernel scaler widens
// the support when downsampling exactly as PIL does, so a Lanczos-3 kernel here
// matches PIL's resample rather than approximating it.
//
// lanczos3(t) = sinc(t)*sinc(t/3); the closed form below is that product with
// x = pi*t folded in.
var lanczos3 = xdraw.Kernel{Support: 3, At: func(t float64) float64 {
	if t == 0 {
		return 1
	}
	if t < 0 {
		t = -t
	}
	if t >= 3 {
		return 0
	}
	x := math.Pi * t
	return 3 * math.Sin(x) * math.Sin(x/3) / (x * x)
}}

// errImageTooLarge marks a file whose header describes something no decoder
// should be pointed at. It rides the same path as a decode failure: the file is
// reported and skipped, not fatal to the import.
var errImageTooLarge = errors.New("image dimensions exceed the limit")

// maxPixels caps what any decode here may allocate. An image header declares
// its own dimensions and image.Decode believes it, allocating width*height*4
// before the source bytes run out and it gives up: a 100KB PNG whose IHDR says
// 30000x30000 asks for 3.6GB and takes the process with it.
//
// 50MP is ~8600x5800, past any real scan — a double-page spread at 600dpi is
// ~44MP — so nothing a comic import should accept lands above it.
const maxPixels = 50_000_000

// headerDims reads only the image header and vets what it claims. It is the
// gate in front of every decode: the header read is a few hundred bytes and
// ~0.1ms, against a decode that allocates whatever the header asked for.
func headerDims(buf []byte) (image.Point, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return image.Point{}, err
	}
	dims := image.Point{X: cfg.Width, Y: cfg.Height}
	if dims.X <= 0 || dims.Y <= 0 {
		return image.Point{}, errZeroDim
	}
	if int64(dims.X)*int64(dims.Y) > maxPixels {
		return image.Point{}, fmt.Errorf("%w: %dx%d", errImageTooLarge, dims.X, dims.Y)
	}
	return dims, nil
}

// thumbnail decodes buf and returns the image's true dimensions plus a 64x64
// 8-bit grayscale buffer. That raw 4KB buffer is the whole "hash": there is no
// perceptual hash anywhere in this pipeline, and the MAE threshold is tuned to
// this representation.
func thumbnail(buf []byte) (image.Point, []byte, error) {
	if _, err := headerDims(buf); err != nil {
		return image.Point{}, nil, err
	}
	src, _, err := image.Decode(bytes.NewReader(buf))
	if err != nil {
		return image.Point{}, nil, err
	}
	b := src.Bounds()
	dims := image.Point{X: b.Dx(), Y: b.Dy()}
	if dims.X == 0 || dims.Y == 0 {
		return image.Point{}, nil, errZeroDim
	}
	// The header is not the bitmap; a decoder is free to return bounds the
	// header never promised, and those are what gets allocated below.
	if int64(dims.X)*int64(dims.Y) > maxPixels {
		return image.Point{}, nil, fmt.Errorf("%w: %dx%d", errImageTooLarge, dims.X, dims.Y)
	}

	// Grayscale first, then resize, matching PIL's convert("L").resize(...).
	// Both operations are linear so the order barely matters numerically, but
	// converting first means the Lanczos pass runs over one channel instead of
	// four.
	//
	// Note this composites alpha against black, whereas PIL's RGBA->L drops the
	// alpha channel and keeps the raw RGB. Comic pages are opaque in practice,
	// and a transparent page would have to differ only in its transparent
	// region for this to change a grouping decision.
	gray := image.NewGray(image.Rect(0, 0, dims.X, dims.Y))
	draw.Draw(gray, gray.Bounds(), src, b.Min, draw.Src)

	dst := image.NewGray(image.Rect(0, 0, thumbSize, thumbSize))
	lanczos3.Scale(dst, dst.Bounds(), gray, gray.Bounds(), xdraw.Src, nil)

	// dst is freshly allocated and tightly packed, so Pix is exactly the 4KB
	// buffer with no stride padding to strip.
	return dims, dst.Pix, nil
}

// mae is the mean absolute difference between two thumbnails on the 0-255
// grayscale scale, matching ImageStat.Stat(ImageChops.difference(a, b)).mean[0].
func mae(a, b []byte) float64 {
	var sum int64
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += int64(d)
	}
	return float64(sum) / float64(len(a))
}

// aspectCompatible gates the pixel comparison. Without it the squashed square
// thumbnail would happily match a portrait page against a landscape one.
func aspectCompatible(a, b image.Point) bool {
	ra := float64(a.X) / float64(a.Y)
	rb := float64(b.X) / float64(b.Y)
	return math.Abs(ra-rb) <= aspectTolerance
}
