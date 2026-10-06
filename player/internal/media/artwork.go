package media

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/image/draw"

	_ "image/gif"
	_ "image/png"
)

const (
	artworkJPEGQuality    = 75
	artworkDefaultWidth   = 640
	artworkThumbMaxAge    = 2592000
	artworkOriginalMaxAge = 86400
)

var artworkThumbWidths = []int{96, 256, 640}

// ServeArtwork serves a cached JPEG thumbnail (96/256/640) or the original for ?full=1.
func (s *Service) ServeArtwork(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	row, ok := s.idx.RowOf(id)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	path := s.idx.MetaAt(row).ArtworkPath
	if path == "" {
		http.Error(w, "no artwork", http.StatusNotFound)
		return
	}
	s.ServeImage(w, r, strconv.FormatInt(id, 10), path)
}

// ServeImage serves path as a cached JPEG thumbnail (96/256/640, ?w=) or the
// original for ?full=1. key names the thumbnail files and must be unique per
// image source (track id for covers, "artist<id>" for artist photos).
func (s *Service) ServeImage(w http.ResponseWriter, r *http.Request, key, path string) {
	if r.URL.Query().Get("full") == "1" {
		w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(artworkOriginalMaxAge))
		s.serveArtworkFile(w, r, path, imageContentType(path))
		return
	}

	maxWidth := artworkDefaultWidth
	if queryWidth := r.URL.Query().Get("w"); queryWidth != "" {
		if width, err := strconv.Atoi(queryWidth); err == nil && width > 0 {
			maxWidth = snapArtworkWidth(width)
		}
	}

	w.Header().Set("Cache-Control", "private, max-age="+strconv.Itoa(artworkThumbMaxAge))
	if thumb, err := s.ensureArtworkThumbnail(key, path, maxWidth); err == nil && thumb != "" {
		s.serveArtworkFile(w, r, thumb, "image/jpeg")
		return
	}

	s.serveArtworkFile(w, r, path, imageContentType(path))
}

func (s *Service) serveArtworkFile(w http.ResponseWriter, r *http.Request, path, mediaType string) {
	file, err := os.Open(path)
	if err != nil {
		http.Error(w, "file missing", http.StatusNotFound)
		return
	}
	defer file.Close()
	stat, _ := file.Stat()
	if mediaType != "" {
		w.Header().Set("Content-Type", mediaType)
	}
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), file)
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

func snapArtworkWidth(width int) int {
	best := artworkThumbWidths[0]
	bestDist := absInt(width - best)
	for _, candidate := range artworkThumbWidths[1:] {
		dist := absInt(width - candidate)
		if dist < bestDist || (dist == bestDist && candidate > best) {
			best, bestDist = candidate, dist
		}
	}
	return best
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (s *Service) ensureArtworkThumbnail(key, srcPath string, maxWidth int) (string, error) {
	dir := s.artworkCacheDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, key+"_w"+strconv.Itoa(maxWidth)+".jpg")
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
	out := resizeMax(img, maxWidth)
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, out, &jpeg.Options{Quality: artworkJPEGQuality}); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, encoded.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return dst, nil
}

func resizeMax(src image.Image, maxWidth int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return src
	}
	if width <= maxWidth && height <= maxWidth {
		return src
	}
	newWidth, newHeight := maxWidth, maxWidth
	if width > height {
		newHeight = height * maxWidth / width
		if newHeight < 1 {
			newHeight = 1
		}
	} else {
		newWidth = width * maxWidth / height
		if newWidth < 1 {
			newWidth = 1
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)
	return dst
}
