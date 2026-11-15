package httpapi

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/jpdev/horizon-go/internal/adapters/httpapi/assets"
	"github.com/jpdev/horizon-go/internal/domain"
)

const (
	ogWidth  = 1200
	ogHeight = 630

	// ogFooter is the brand seal drawn at the bottom of every OG card.
	ogFooter = "Radar by JpDev · horizon-web-eight.vercel.app"
)

var (
	ogInitOnce sync.Once
	ogFontBold *opentype.Font
	ogFontReg  *opentype.Font
	ogLogo     image.Image
)

func initOGAssets() {
	ogInitOnce.Do(func() {
		ogFontBold, _ = opentype.Parse(gobold.TTF)
		ogFontReg, _ = opentype.Parse(goregular.TTF)
		if raw, err := png.Decode(bytes.NewReader(assets.LogoPNG)); err == nil {
			scaled := image.NewRGBA(image.Rect(0, 0, 80, 80))
			xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), raw, raw.Bounds(), draw.Over, nil)
			ogLogo = scaled
		}
	})
}

func makeFace(f *opentype.Font, size float64) font.Face {
	if f == nil {
		return nil
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	return face
}

func fillRoundedRect(dst *image.RGBA, rect image.Rectangle, radius int, c color.RGBA) {
	r2 := float64(radius * radius)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			dx, dy := 0.0, 0.0
			if x < rect.Min.X+radius {
				dx = float64(rect.Min.X + radius - x)
			} else if x >= rect.Max.X-radius {
				dx = float64(x - (rect.Max.X - radius - 1))
			}
			if y < rect.Min.Y+radius {
				dy = float64(rect.Min.Y + radius - y)
			} else if y >= rect.Max.Y-radius {
				dy = float64(y - (rect.Max.Y - radius - 1))
			}
			if dx > 0 && dy > 0 {
				dist := math.Sqrt(dx*dx + dy*dy)
				if dist > float64(radius)+0.5 {
					continue
				}
				if dist > float64(radius)-0.5 && r2 > 0 {
					alpha := float64(radius) + 0.5 - dist
					blendPixel(dst, x, y, c, alpha)
					continue
				}
			}
			dst.SetRGBA(x, y, c)
		}
	}
}

func blendPixel(dst *image.RGBA, x, y int, c color.RGBA, a float64) {
	if a <= 0 {
		return
	}
	if a >= 1 {
		dst.SetRGBA(x, y, c)
		return
	}
	bg := dst.RGBAAt(x, y)
	inv := 1 - a
	dst.SetRGBA(x, y, color.RGBA{
		R: uint8(float64(c.R)*a + float64(bg.R)*inv),
		G: uint8(float64(c.G)*a + float64(bg.G)*inv),
		B: uint8(float64(c.B)*a + float64(bg.B)*inv),
		A: 255,
	})
}

func drawText(dst *image.RGBA, face font.Face, col color.RGBA, x, y int, s string) {
	if face == nil || s == "" {
		return
	}
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(col),
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(s)
}

func wrapLines(face font.Face, s string, maxWidthPx, maxLines int) []string {
	words := strings.Fields(strings.TrimSpace(s))
	if len(words) == 0 {
		return nil
	}
	measure := func(line string) int {
		if face == nil {
			return len(line) * 18
		}
		return font.MeasureString(face, line).Ceil()
	}
	truncateToWidth := func(text string) string {
		text = strings.TrimSpace(text)
		if measure(text) <= maxWidthPx {
			return text
		}
		rs := []rune(text)
		for len(rs) > 4 && measure(string(rs)+"…") > maxWidthPx {
			rs = rs[:len(rs)-1]
		}
		return strings.TrimSpace(string(rs)) + "…"
	}
	var lines []string
	cur := words[0]
	for idx, w := range words[1:] {
		cand := cur + " " + w
		if measure(cand) <= maxWidthPx {
			cur = cand
			continue
		}
		lines = append(lines, truncateToWidth(cur))
		if len(lines) == maxLines-1 {
			rest := strings.Join(words[idx+1:], " ")
			lines = append(lines, truncateToWidth(rest))
			return lines
		}
		cur = w
	}
	if cur != "" && len(lines) < maxLines {
		lines = append(lines, truncateToWidth(cur))
	}
	return lines
}

// RenderItemOG renders a 1200x630 OpenGraph PNG card using the Horizon theme and the official Radar icon.
func RenderItemOG(it domain.ItemDetail) ([]byte, error) {
	initOGAssets()
	canvas := image.NewRGBA(image.Rect(0, 0, ogWidth, ogHeight))

	bgSage := color.RGBA{238, 242, 238, 255} // #eef2ee
	border := color.RGBA{210, 222, 212, 255} // subtle card border
	white := color.RGBA{255, 255, 255, 255}  // #ffffff
	ink := color.RGBA{18, 26, 20, 255}       // #121a14
	muted := color.RGBA{78, 92, 82, 255}     // readable muted
	accent := color.RGBA{39, 107, 67, 255}   // #276b43
	tintBg := color.RGBA{226, 241, 231, 255} // soft accent pill fill

	draw.Draw(canvas, canvas.Bounds(), image.NewUniform(bgSage), image.Point{}, draw.Src)
	fillRoundedRect(canvas, image.Rect(48, 42, ogWidth-48, ogHeight-42), 34, border)
	fillRoundedRect(canvas, image.Rect(50, 44, ogWidth-50, ogHeight-44), 32, white)

	// Header: official Radar icon (already a rounded square; do not circle-crop) + brand + score pill.
	if ogLogo != nil {
		b := ogLogo.Bounds()
		draw.Draw(canvas, image.Rect(96, 88, 96+b.Dx(), 88+b.Dy()), ogLogo, b.Min, draw.Over)
	}
	brandFace := makeFace(ogFontBold, 26)
	subFace := makeFace(ogFontReg, 20)
	titleFace := makeFace(ogFontBold, 44)
	bodyFace := makeFace(ogFontReg, 24)
	if brandFace != nil {
		defer brandFace.Close()
	}
	if subFace != nil {
		defer subFace.Close()
	}
	if titleFace != nil {
		defer titleFace.Close()
	}
	if bodyFace != nil {
		defer bodyFace.Close()
	}

	drawText(canvas, brandFace, ink, 190, 122, "Radar by JpDev")
	subBrand := it.SourceName
	if host := urlHost(it.URL); host != "" {
		subBrand += " · " + host
	}
	drawText(canvas, subFace, muted, 190, 152, subBrand)

	scoreText := "Curadoria IA"
	if it.Score != nil {
		scoreText = fmt.Sprintf("Nota %.1f/10", *it.Score)
	}
	pillW := 210
	if brandFace != nil {
		pillW = font.MeasureString(brandFace, scoreText).Ceil() + 48
	}
	fillRoundedRect(canvas, image.Rect(ogWidth-96-pillW, 92, ogWidth-96, 154), 30, tintBg)
	drawText(canvas, brandFace, accent, ogWidth-96-pillW+24, 132, scoreText)

	// Main title (word-wrapped up to 4 lines).
	lines := wrapLines(titleFace, it.Title, ogWidth-192, 4)
	y := 246
	for _, ln := range lines {
		drawText(canvas, titleFace, ink, 96, y, ln)
		y += 58
	}

	// Summary excerpt if space permits.
	summary := strings.TrimSpace(it.Summary)
	if summary == "" {
		summary = strings.TrimSpace(it.ScoreReason)
	}
	if summary != "" && y < 480 {
		sumLines := wrapLines(bodyFace, summary, ogWidth-192, 2)
		sy := max(y+14, 460)
		for _, ln := range sumLines {
			if sy > 515 {
				break
			}
			drawText(canvas, bodyFace, muted, 96, sy, ln)
			sy += 34
		}
	}

	// Bottom accent rule + seal.
	fillRoundedRect(canvas, image.Rect(96, ogHeight-92, 240, ogHeight-86), 3, accent)
	drawText(canvas, subFace, accent, 256, ogHeight-82, ogFooter)

	var buf bytes.Buffer
	if err := png.Encode(&buf, canvas); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Server) itemOGImage(w http.ResponseWriter, r *http.Request) {
	rawID := strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/api/og/items/")), ".png")
	if strings.HasPrefix(r.URL.Path, "/og/items/") {
		rawID = strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/og/items/")), ".png")
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	it, err := s.Store.Item(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pngBytes, err := RenderItemOG(it)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=1800, stale-while-revalidate=3600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pngBytes)
}
