package deck

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"

	_ "image/gif" // decoders for previews
	_ "image/png"
)

// thumbPx is the width of the preview the phone shows in a file offer.
const thumbPx = 320

// thumbnail returns a small JPEG preview (base64) of an image file, or "" for
// anything that is not a decodable image. Never larger than the inbox allows.
func thumbnail(data []byte) string {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return ""
	}
	tw, th := w, h
	if w > thumbPx {
		tw, th = thumbPx, max(1, h*thumbPx/w)
	}
	dst := image.NewRGBA(image.Rect(0, 0, tw, th))
	// box filter: average the source pixels each target pixel covers
	for y := 0; y < th; y++ {
		y0, y1 := b.Min.Y+y*h/th, b.Min.Y+max((y+1)*h/th, y*h/th+1)
		for x := 0; x < tw; x++ {
			x0, x1 := b.Min.X+x*w/tw, b.Min.X+max((x+1)*w/tw, x*w/tw+1)
			var r, g, bl, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, _ := src.At(sx, sy).RGBA()
					r, g, bl, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), n+1
				}
			}
			dst.Set(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 75}); err != nil {
		return ""
	}
	if out.Len() > 40<<10 {
		return "" // a noisy photo can still be big; the offer works without
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}
