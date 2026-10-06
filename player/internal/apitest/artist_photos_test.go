package apitest

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestArtistPhotosMapAndThumb(t *testing.T) {
	server := openTestServer(t)
	photo := filepath.Join(t.TempDir(), "splin.png")
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1000))
	for y := 0; y < 1000; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	f, err := os.Create(photo)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	if _, err := server.Store.DB.Exec(`
INSERT INTO artist_photos(id, name_key, artist, path, status, source, checked_at) VALUES
 (5, 'сплин', 'Сплин', ?, 'found', 'deezer', 'now'),
 (6, 'thomas', 'Thomas', NULL, 'missing', 'deezer', 'now')`, photo); err != nil {
		t.Fatal(err)
	}

	rec := serve(server, jsonReq("GET", "/api/artist-photos", ""))
	var got struct {
		Photos map[string]string `json:"photos"`
		Count  int               `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Misses are not listed; the UI falls back to an album cover for them.
	if got.Count != 1 || got.Photos["сплин"] != "/api/artist-photos/5" {
		t.Fatalf("photos=%+v", got)
	}

	rec = serve(server, httptest.NewRequest("GET", "/api/artist-photos/5?w=256", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	decoded, err := jpeg.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil || decoded.Bounds().Dx() != 256 {
		t.Fatalf("thumb decode err=%v", err)
	}

	for _, path := range []string{"/api/artist-photos/6", "/api/artist-photos/999", "/api/artist-photos/x"} {
		rec = serve(server, httptest.NewRequest("GET", path, nil))
		if rec.Code != 404 && rec.Code != 400 {
			t.Fatalf("%s status=%d", path, rec.Code)
		}
	}
}
