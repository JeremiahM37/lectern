package tui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noisePNG is an opaque PNG of random pixels. Noise does not compress, so the
// PNG is much bigger than the attachment limit.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < len(img.Pix); i += 4 {
		v := rng.Uint64()
		img.Pix[i] = byte(v)
		img.Pix[i+1] = byte(v >> 8)
		img.Pix[i+2] = byte(v >> 16)
		img.Pix[i+3] = 0xFF
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFitImageShrinksLargeNoisePNG(t *testing.T) {
	data := noisePNG(t, 2000, 1500)
	if len(data) <= clipImageMax {
		t.Fatalf("setup: noise PNG is %d bytes, want it over the limit", len(data))
	}

	f, err := fitImage(data, "image/png")
	if err != nil {
		t.Fatalf("fitImage: %v", err)
	}

	if len(f.data) > clipImageMax {
		t.Fatalf("shrunk image is %d bytes, want at most %d", len(f.data), clipImageMax)
	}
	if f.mediaType != "image/jpeg" {
		t.Fatalf("media type = %q, want image/jpeg (no transparency)", f.mediaType)
	}
	if !f.resized {
		t.Fatal("resized = false for an image that was shrunk")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(f.data))
	if err != nil || format != "jpeg" {
		t.Fatalf("output is not a decodable JPEG: format %q, err %v", format, err)
	}
	if cfg.Width != f.width || cfg.Height != f.height {
		t.Fatalf("output is %dx%d, fittedImage says %dx%d", cfg.Width, cfg.Height, f.width, f.height)
	}
	// The source is 4:3 and must come down to one of the ladder sides.
	ok := false
	for _, want := range [][2]int{{1600, 1200}, {1200, 900}, {800, 600}} {
		ok = ok || (f.width == want[0] && f.height == want[1])
	}
	if !ok {
		t.Fatalf("output is %dx%d, want one of the 4:3 ladder sizes", f.width, f.height)
	}
}

func TestResizeNoticeNamesSizeAndLimit(t *testing.T) {
	f := fittedImage{data: make([]byte, 2_100_000), width: 1600, height: 1200, resized: true}

	got := resizeNotice(f)

	want := "Image resized to 1600x1200 (2.1 MB) to fit the 3.75 MB limit"
	if got != want {
		t.Fatalf("notice = %q, want %q", got, want)
	}
}

func TestFitImageLeavesSmallImageUnchanged(t *testing.T) {
	data := pngBytes(t)

	f, err := fitImage(data, "image/png")
	if err != nil {
		t.Fatalf("fitImage: %v", err)
	}

	if !bytes.Equal(f.data, data) {
		t.Fatal("a small image was re-encoded")
	}
	if f.mediaType != "image/png" || f.resized {
		t.Fatalf("got media %q resized %v, want the original png untouched", f.mediaType, f.resized)
	}
}

// transparentSource is 300x200: the left half is opaque noise and the right half
// is fully transparent.
func transparentSource(opaqueNoise bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	rng := rand.New(rand.NewPCG(3, 4))
	for y := 0; y < 200; y++ {
		for x := 0; x < 150; x++ {
			c := color.NRGBA{R: 200, G: 30, B: 30, A: 255}
			if opaqueNoise {
				v := rng.Uint64()
				c = color.NRGBA{R: uint8(v), G: uint8(v >> 8), B: uint8(v >> 16), A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func TestTransparentImageStaysPNGWhenItFits(t *testing.T) {
	src := transparentSource(false)

	f, err := fitDecoded(src, clipImageMax)
	if err != nil {
		t.Fatalf("fitDecoded: %v", err)
	}

	if f.mediaType != "image/png" {
		t.Fatalf("media type = %q, want image/png so the transparency survives", f.mediaType)
	}
	img, _, err := image.Decode(bytes.NewReader(f.data))
	if err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
	if _, _, _, a := img.At(290, 100).RGBA(); a != 0 {
		t.Fatalf("transparent area has alpha %d, want 0", a)
	}
	if _, _, _, a := img.At(10, 100).RGBA(); a != 0xFFFF {
		t.Fatalf("opaque area has alpha %d, want fully opaque", a)
	}
}

func TestTransparentImageFlattensOntoWhiteWhenPNGTooBig(t *testing.T) {
	src := transparentSource(true)
	// The noise PNG is about 90 KB, so this limit forces the JPEG path.
	const limit = 60_000

	f, err := fitDecoded(src, limit)
	if err != nil {
		t.Fatalf("fitDecoded: %v", err)
	}

	if f.mediaType != "image/jpeg" {
		t.Fatalf("media type = %q, want image/jpeg when the PNG does not fit", f.mediaType)
	}
	if len(f.data) > limit {
		t.Fatalf("output is %d bytes, want at most %d", len(f.data), limit)
	}
	img, _, err := image.Decode(bytes.NewReader(f.data))
	if err != nil {
		t.Fatalf("output does not decode: %v", err)
	}
	// JPEG has no alpha: the transparent half must come out white, not black.
	r, g, b, _ := img.At(260, 100).RGBA()
	for _, c := range []uint32{r >> 8, g >> 8, b >> 8} {
		if c < 240 {
			t.Fatalf("transparent area decoded to %v, want near-white", img.At(260, 100))
		}
	}
}

func TestFitImageRefusesOversizeNonImage(t *testing.T) {
	data := make([]byte, clipImageMax+1)

	_, err := fitImage(data, "")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v, want the size-limit error", err)
	}
}

func TestAttachingAnOversizeFileShrinksItAndSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, noisePNG(t, 2000, 1500), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(t, 120, 40)
	settle(m)
	m.attachImageFile(m.cur(), path)
	if !strings.Contains(m.flash, "Image resized to") || !strings.Contains(m.flash, "Attached image 1") {
		t.Fatalf("flash = %q, want the attach and resize notice", m.flash)
	}
	imgs := m.cur().in.images
	if len(imgs) != 1 || imgs[0].part.MediaType != "image/jpeg" || len(imgs[0].part.Data) > clipImageMax {
		t.Fatalf("attached %d images; want one jpeg under the limit", len(imgs))
	}
}

func TestResampleAveragesPixels(t *testing.T) {
	// Two white and two black pixels average to mid-grey in one output pixel.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	src.SetNRGBA(1, 1, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	src.SetNRGBA(0, 1, color.NRGBA{A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{A: 255})

	got := resample(src, 1, 1).RGBA64At(0, 0)

	if got.R < 32700 || got.R > 32800 || got.A != 0xFFFF {
		t.Fatalf("averaged pixel = %+v, want mid-grey and opaque", got)
	}
}

func TestScaleToFitKeepsAspectRatio(t *testing.T) {
	cases := []struct {
		w, h, side, wantW, wantH int
	}{
		{2000, 1500, 1600, 1600, 1200},
		{1500, 2000, 1600, 1200, 1600},
		{800, 600, 1600, 800, 600}, // already small enough: unchanged
		{4000, 10, 1600, 1600, 4},
	}
	for _, c := range cases {
		gw, gh := scaleToFit(c.w, c.h, c.side)
		if gw != c.wantW || gh != c.wantH {
			t.Errorf("scaleToFit(%d, %d, %d) = %dx%d, want %s", c.w, c.h, c.side, gw, gh,
				fmt.Sprintf("%dx%d", c.wantW, c.wantH))
		}
	}
}

// noiseJPEG is an opaque JPEG of random pixels at quality 100. Noise does not
// compress, so the file is well over the attachment limit.
func noiseJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	rng := rand.New(rand.NewPCG(5, 6))
	for i := 0; i < len(img.Pix); i += 4 {
		v := rng.Uint64()
		img.Pix[i] = byte(v)
		img.Pix[i+1] = byte(v >> 8)
		img.Pix[i+2] = byte(v >> 16)
		img.Pix[i+3] = 0xFF
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFitImageShrinksLargeNoiseJPEG(t *testing.T) {
	data := noiseJPEG(t, 2000, 1500)
	if len(data) <= clipImageMax {
		t.Fatalf("setup: noise JPEG is %d bytes, want it over the limit", len(data))
	}

	f, err := fitImage(data, "image/jpeg")
	if err != nil {
		t.Fatalf("fitImage: %v", err)
	}

	if len(f.data) > clipImageMax {
		t.Fatalf("shrunk image is %d bytes, want at most %d", len(f.data), clipImageMax)
	}
	if f.mediaType != "image/jpeg" || !f.resized {
		t.Fatalf("media %q resized %v, want a resized jpeg", f.mediaType, f.resized)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(f.data))
	if err != nil || format != "jpeg" {
		t.Fatalf("output is not a decodable JPEG: format %q, err %v", format, err)
	}
	if cfg.Width != 1600 || cfg.Height != 1200 {
		t.Fatalf("output is %dx%d, want 1600x1200 (first ladder side)", cfg.Width, cfg.Height)
	}
}

// webpFixture reads a WebP committed under testdata. The files were made with
// Pillow (libwebp), since no cwebp is installed here.
func webpFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if sniffImage(data) != "image/webp" {
		t.Fatalf("fixture %s is not sniffed as WebP", name)
	}
	return data
}

func TestFitImageShrinksLosslessWebPToJPEG(t *testing.T) {
	data := webpFixture(t, "shrink_lossless.webp")
	// Force the shrink path with a limit below the fixture's size. The 320x240
	// source is smaller than every ladder side, so it is re-encoded at its own
	// size and only the JPEG quality moves.
	limit := len(data) / 3
	f, err := fitImageTo(data, "image/webp", limit)
	if err != nil {
		t.Fatalf("fitImageTo: %v", err)
	}
	if f.mediaType != "image/jpeg" || !f.resized {
		t.Fatalf("media %q resized %v, want a resized jpeg (WebP is decode-only)", f.mediaType, f.resized)
	}
	if len(f.data) > limit {
		t.Fatalf("output is %d bytes, want at most %d", len(f.data), limit)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(f.data))
	if err != nil || format != "jpeg" {
		t.Fatalf("output is not a decodable JPEG: format %q, err %v", format, err)
	}
	if cfg.Width != 320 || cfg.Height != 240 || f.width != 320 || f.height != 240 {
		t.Fatalf("output is %dx%d (fitted %dx%d), want 320x240", cfg.Width, cfg.Height, f.width, f.height)
	}
}

// The lossy fixture is too small to shrink below its own size (a JPEG of it is
// bigger than the WebP), so it is checked by decoding and re-encoding directly.
func TestLossyWebPDecodesAndReencodesAsJPEG(t *testing.T) {
	data := webpFixture(t, "shrink_lossy.webp")

	src, err := decodeForShrink(data)
	if err != nil {
		t.Fatalf("decodeForShrink: %v", err)
	}
	if b := src.Bounds(); b.Dx() != 320 || b.Dy() != 240 {
		t.Fatalf("decoded %dx%d, want 320x240", b.Dx(), b.Dy())
	}
	f, err := fitDecoded(src, clipImageMax)
	if err != nil {
		t.Fatalf("fitDecoded: %v", err)
	}
	if f.mediaType != "image/jpeg" {
		t.Fatalf("media %q, want image/jpeg", f.mediaType)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(f.data)); err != nil || format != "jpeg" {
		t.Fatalf("output is not a decodable JPEG: format %q, err %v", format, err)
	}
}

func TestFitImageWebPUnderLimitIsUnchanged(t *testing.T) {
	data := webpFixture(t, "shrink_lossy.webp")

	f, err := fitImage(data, "image/webp")
	if err != nil {
		t.Fatalf("fitImage: %v", err)
	}
	if !bytes.Equal(f.data, data) || f.mediaType != "image/webp" || f.resized {
		t.Fatalf("a WebP under the limit was re-encoded (media %q, resized %v)", f.mediaType, f.resized)
	}
}

func TestFitImageUndecodableWebPGetsSizeError(t *testing.T) {
	// RIFF/WEBP header so it sniffs as WebP, then garbage the decoder rejects.
	data := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), bytes.Repeat([]byte{0xAB}, clipImageMax)...)

	_, err := fitImage(data, "image/webp")
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err = %v, want the size-limit error", err)
	}
}
