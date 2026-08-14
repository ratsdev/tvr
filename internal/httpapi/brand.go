package httpapi

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ratsdev/tvr/internal/core/store"

	_ "image/gif"
	_ "image/jpeg"
)

const (
	brandIconURL    = "/brand-icon"
	brandIconFile   = "brand-icon.png"
	brandIconSize   = 128
	maxBrandIconSrc = 2048
)

type brandIconPlan struct {
	url    string
	png    []byte
	remove bool
	tmp    string
}

func (s *Server) brandIconPath() string {
	return filepath.Join(s.cfg.DataDir, brandIconFile)
}

func (s *Server) handleGetBrandIcon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, s.brandIconPath())
}

func (s *Server) planBrandIcon(icon string) (brandIconPlan, error) {
	icon = strings.TrimSpace(icon)
	if store.IsDefaultBrandIcon(icon) {
		return brandIconPlan{remove: true}, nil
	}
	if path, _, _ := strings.Cut(icon, "?"); path == brandIconURL {
		if _, err := os.Stat(s.brandIconPath()); err != nil {
			return brandIconPlan{}, fmt.Errorf("%w: uploaded brand icon is missing", store.ErrValidation)
		}
		return brandIconPlan{url: brandIconURL}, nil
	}
	if strings.HasPrefix(icon, "data:image/") {
		img, err := decodeDataImage(icon)
		if err != nil {
			return brandIconPlan{}, fmt.Errorf("%w: %s", store.ErrValidation, err)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, fitSquare(img, brandIconSize)); err != nil {
			return brandIconPlan{}, err
		}
		return brandIconPlan{url: brandIconURL, png: buf.Bytes()}, nil
	}
	return brandIconPlan{url: icon}, nil
}

func (s *Server) prepareBrandUpdate(in *store.BrandSettings) (brandIconPlan, error) {
	if in == nil {
		return brandIconPlan{}, nil
	}
	plan, err := s.planBrandIcon(in.Icon)
	if err != nil {
		return brandIconPlan{}, err
	}
	in.Icon = plan.url
	return plan, store.ValidateBrandSettings(*in)
}

func (s *Server) stageBrandIcon(p brandIconPlan) (brandIconPlan, error) {
	if len(p.png) == 0 {
		return p, nil
	}
	if err := os.MkdirAll(s.cfg.DataDir, 0o755); err != nil {
		return p, err
	}
	tmp := s.brandIconPath() + ".tmp"
	if err := os.WriteFile(tmp, p.png, 0o644); err != nil {
		return p, err
	}
	p.tmp = tmp
	return p, nil
}

func (s *Server) abandonBrandIcon(p brandIconPlan) {
	if p.tmp != "" {
		_ = os.Remove(p.tmp)
	}
}

func (s *Server) commitBrandIcon(p brandIconPlan) error {
	if p.tmp != "" {
		if err := os.Rename(p.tmp, s.brandIconPath()); err != nil {
			_ = os.Remove(p.tmp)
			return err
		}
		return nil
	}
	if p.remove || (p.url != "" && p.url != brandIconURL) {
		_ = os.Remove(s.brandIconPath())
	}
	return nil
}

func (s *Server) publicBrandIcon(icon string) string {
	if store.IsDefaultBrandIcon(icon) {
		return ""
	}
	if path, _, _ := strings.Cut(icon, "?"); path == brandIconURL {
		if fi, err := os.Stat(s.brandIconPath()); err == nil {
			return brandIconURL + "?v=" + strconv.FormatInt(fi.ModTime().Unix(), 10)
		}
		return ""
	}
	return icon
}

func decodeDataImage(dataURL string) (image.Image, error) {
	header, payload, ok := strings.Cut(strings.TrimSpace(dataURL), ",")
	if !ok || !strings.HasPrefix(header, "data:image/") {
		return nil, fmt.Errorf("brand icon must be an image")
	}
	raw := []byte(payload)
	if strings.Contains(header, ";base64") {
		b, err := decodeBase64Payload(payload)
		if err != nil {
			return nil, fmt.Errorf("could not read brand icon")
		}
		raw = b
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("brand icon is not a usable image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxBrandIconSrc || cfg.Height > maxBrandIconSrc {
		return nil, fmt.Errorf("brand icon is too large")
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("brand icon is not a usable image")
	}
	return img, nil
}

func decodeBase64Payload(payload string) ([]byte, error) {
	cleaned := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, payload)
	if b, err := base64.StdEncoding.DecodeString(cleaned); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(cleaned)
}

func fitSquare(src image.Image, size int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	side := w
	if h < w {
		side = h
	}
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	if side <= size && w == h {
		return src
	}
	if size < 1 {
		size = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			sx := x0 + x*side/size
			sy := y0 + y*side/size
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
