package tui

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"

	// The GIF decoder is registered so a GIF attaches; its first frame is kept.
	_ "image/gif"

	// The WebP decoder is registered so a WebP can be shrunk. x/image only
	// decodes WebP, so the output is always PNG or JPEG.
	_ "golang.org/x/image/webp"
)

// An image over the attachment limit is shrunk instead of refused. PNG, JPEG,
// GIF and WebP are decoded and re-encoded as PNG or JPEG. A file that cannot be
// decoded still gets the size error.

const (
	// shrinkMaxInput bounds the bytes read from a file to shrink it. Anything
	// bigger is refused without being read.
	shrinkMaxInput = 64 << 20

	// shrinkMaxPixels bounds the decoded size, so a small file that declares a
	// huge canvas cannot exhaust memory.
	shrinkMaxPixels = 40_000_000
)

// shrinkSides are the longest-side limits tried in order. 1600 keeps a
// screenshot's text readable; the smaller sides are for photos that are still
// too big at 1600.
var shrinkSides = []int{1600, 1200, 800}

// jpegQualities are tried highest first, stepping down until the file fits.
var jpegQualities = []int{85, 75, 65, 55, 45}

var errCannotShrink = errors.New("image cannot be shrunk to fit the limit")

// fittedImage is an attachment that is ready to send.
type fittedImage struct {
	data          []byte
	mediaType     string
	width, height int
	resized       bool
}

// fitImage returns data unchanged when it is within clipImageMax. A larger
// image is decoded, downscaled and re-encoded. When that cannot get it under
// the limit, the caller gets the size error.
func fitImage(data []byte, mt string) (fittedImage, error) {
	return fitImageTo(data, mt, clipImageMax)
}

// fitImageTo is fitImage with the byte limit as a parameter, so the shrink path
// can be exercised on a small file.
func fitImageTo(data []byte, mt string, limit int) (fittedImage, error) {
	if len(data) <= limit {
		return fittedImage{data: data, mediaType: mt}, nil
	}
	src, err := decodeForShrink(data)
	if err != nil {
		return fittedImage{}, imageTooBig(len(data))
	}
	f, err := fitDecoded(src, limit)
	if err != nil {
		return fittedImage{}, imageTooBig(len(data))
	}
	return f, nil
}

// decodeForShrink decodes data, refusing a canvas too large to hold in memory.
// The size is read from the header first, so nothing big is allocated.
func decodeForShrink(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > shrinkMaxPixels {
		return nil, errCannotShrink
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}

// fitDecoded downscales src and re-encodes it to at most limit bytes. A source
// with transparency stays PNG when that fits. Otherwise it is flattened onto
// white, since JPEG has no alpha, and written at the highest quality that fits.
func fitDecoded(src image.Image, limit int) (fittedImage, error) {
	transparent := hasTransparency(src)
	b := src.Bounds()
	for _, side := range shrinkSides {
		w, h := scaleToFit(b.Dx(), b.Dy(), side)
		small := resample(src, w, h)
		if transparent {
			buf, err := encodePNG(small)
			if err != nil {
				return fittedImage{}, err
			}
			if len(buf) <= limit {
				return fittedImage{data: buf, mediaType: "image/png", width: w, height: h, resized: true}, nil
			}
		}
		flat := flattenOnWhite(small)
		for _, q := range jpegQualities {
			buf, err := encodeJPEG(flat, q)
			if err != nil {
				return fittedImage{}, err
			}
			if len(buf) <= limit {
				return fittedImage{data: buf, mediaType: "image/jpeg", width: w, height: h, resized: true}, nil
			}
		}
	}
	return fittedImage{}, errCannotShrink
}

// hasTransparency reports whether any pixel of img is not fully opaque. Formats
// without an alpha channel (JPEG, grayscale) have no Opaque method and count
// as opaque.
func hasTransparency(img image.Image) bool {
	o, ok := img.(interface{ Opaque() bool })
	return ok && !o.Opaque()
}

// scaleToFit returns the size of a w x h image scaled so its longest side is
// side, keeping the aspect ratio. An image already within side is unchanged.
func scaleToFit(w, h, side int) (int, int) {
	long := max(w, h)
	if long <= side {
		return w, h
	}
	return max(w*side/long, 1), max(h*side/long, 1)
}

// resample shrinks src to tw x th by area averaging: each output pixel is the
// mean of the source pixels it covers. That keeps detail where a nearest-pixel
// pick would alias. The result is premultiplied, so transparent pixels do not
// darken their neighbours.
func resample(src image.Image, tw, th int) *image.RGBA64 {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA64(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		y0, y1 := span(y, th, sh)
		for x := 0; x < tw; x++ {
			x0, x1 := span(x, tw, sw)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r += uint64(cr)
					g += uint64(cg)
					bl += uint64(cb)
					a += uint64(ca)
					n++
				}
			}
			dst.SetRGBA64(x, y, color.RGBA64{
				R: uint16(r / n),
				G: uint16(g / n),
				B: uint16(bl / n),
				A: uint16(a / n),
			})
		}
	}
	return dst
}

// span maps output index i of n onto the source range [start, end) of a
// dimension with size src. Every output pixel covers at least one source pixel.
func span(i, n, src int) (int, int) {
	start := i * src / n
	end := (i + 1) * src / n
	if end <= start {
		end = start + 1
	}
	return start, end
}

// flattenOnWhite composites a premultiplied image over white and returns it
// opaque, for JPEG. Transparent areas become white rather than black.
func flattenOnWhite(src *image.RGBA64) *image.RGBA {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := src.RGBA64At(x, y)
			// Premultiplied white shows through where the pixel is not opaque.
			rest := uint32(0xFFFF) - uint32(c.A)
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8((uint32(c.R) + rest) >> 8),
				G: uint8((uint32(c.G) + rest) >> 8),
				B: uint8((uint32(c.B) + rest) >> 8),
				A: 0xFF,
			})
		}
	}
	return dst
}

// encodePNG writes img as a PNG at the best compression. A shrunken image is
// mostly flat colour, so this often comes out much smaller than the default.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// encodeJPEG writes img as a JPEG at quality q.
func encodeJPEG(img image.Image, q int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// resizeNotice is the message shown when an image was shrunk to fit.
func resizeNotice(f fittedImage) string {
	return fmt.Sprintf("Image resized to %dx%d (%.1f MB) to fit the %.2f MB limit",
		f.width, f.height, float64(len(f.data))/1e6, float64(clipImageMax)/1e6)
}
