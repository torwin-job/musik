package api

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "image/gif"
	_ "image/png"
)

func (s *Server) artThumbDir() string {
	// DB at …/data/db/musik.db → thumbs at …/data/cache/art
	dbDir := filepath.Dir(s.Cfg.DBPath)
	dataDir := filepath.Dir(dbDir)
	return filepath.Join(dataDir, "cache", "art")
}

func (s *Server) handleArtwork(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	row, ok := s.Idx.RowOf(id)
	if !ok {
		http.Error(w, "not found", 404)
		return
	}
	path := s.Idx.MetaAt(row).ArtworkPath
	if path == "" {
		http.Error(w, "no artwork", 404)
		return
	}

	maxW := 0
	if qs := r.URL.Query().Get("w"); qs != "" {
		if n, err := strconv.Atoi(qs); err == nil {
			maxW = n
		}
	}
	// Clamp to sensible mobile sizes.
	if maxW > 0 {
		if maxW < 48 {
			maxW = 48
		}
		if maxW > 512 {
			maxW = 512
		}
	}

	w.Header().Set("Cache-Control", "private, max-age=86400")

	if maxW > 0 {
		if thumb, err := s.ensureArtThumb(id, path, maxW); err == nil && thumb != "" {
			f, err := os.Open(thumb)
			if err == nil {
				defer f.Close()
				stat, _ := f.Stat()
				w.Header().Set("Content-Type", "image/jpeg")
				http.ServeContent(w, r, filepath.Base(thumb), stat.ModTime(), f)
				return
			}
		}
		// Fall through to original on thumb failure.
	}

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file missing", 404)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	if ct := imageContentType(path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), f)
}

func imageContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return ""
	}
}

func (s *Server) ensureArtThumb(id int64, srcPath string, maxW int) (string, error) {
	dir := s.artThumbDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, strconv.FormatInt(id, 10)+"_w"+strconv.Itoa(maxW)+".jpg")

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", err
	}
	if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) {
		return dst, nil
	}

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return "", err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	out := resizeMax(img, maxW)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: 78}); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func resizeMax(src image.Image, maxW int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return src
	}
	if w <= maxW && h <= maxW {
		return src
	}
	nw, nh := maxW, maxW
	if w > h {
		nh = h * maxW / w
		if nh < 1 {
			nh = 1
		}
	} else {
		nw = w * maxW / h
		if nw < 1 {
			nw = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy := b.Min.Y + y*h/nh
		for x := 0; x < nw; x++ {
			sx := b.Min.X + x*w/nw
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}
