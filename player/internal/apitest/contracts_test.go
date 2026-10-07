package apitest

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestShareRadioCRUD(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq(http.MethodPost, "/api/share/radio", `{"name":"Kitchen"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		OK    bool   `json:"ok"`
		Token string `json:"token"`
		Name  string `json:"name"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.Token == "" || created.Name != "Kitchen" || created.URL == "" {
		t.Fatalf("created=%+v", created)
	}

	rec = serve(server, jsonReq(http.MethodGet, "/api/share/radio", ""))
	var active struct {
		Count  int `json:"count"`
		Shares []struct {
			Token  string `json:"token"`
			Active bool   `json:"active"`
		} `json:"shares"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.Count != 1 || active.Shares[0].Token != created.Token || !active.Shares[0].Active {
		t.Fatalf("active shares=%+v", active)
	}

	rec = serve(server, jsonReq(http.MethodDelete, "/api/share/radio/"+created.Token, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = serve(server, jsonReq(http.MethodGet, "/api/share/radio", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.Count != 0 {
		t.Fatalf("active count after revoke=%d", active.Count)
	}

	rec = serve(server, jsonReq(http.MethodGet, "/api/share/radio?all=1", ""))
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil {
		t.Fatal(err)
	}
	if active.Count != 1 || active.Shares[0].Active {
		t.Fatalf("all shares after revoke=%+v", active)
	}
}

func TestStreamOriginalAndErrors(t *testing.T) {
	server := openTestServer(t)
	audio := []byte("not-real-audio-but-served-verbatim")
	path := filepath.Join(t.TempDir(), "sample.flac")
	if err := os.WriteFile(path, audio, 0o600); err != nil {
		t.Fatal(err)
	}
	loadIndex(t, server, []db.TrackRow{{
		ID: 11, Path: path, Title: "Track", Artist: "Artist", Album: "Album",
		Duration: 180, Embedding: index.Float32Bytes([]float32{1, 11}), Dim: 2,
	}})

	rec := serve(server, jsonReq(http.MethodGet, "/api/stream/11", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); string(got) != string(audio) {
		t.Fatalf("stream body=%q, want %q", got, audio)
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Fatalf("Accept-Ranges=%q", got)
	}

	for _, tc := range []struct {
		path string
		want int
		code string
	}{
		{"/api/stream/999", http.StatusNotFound, "not_found"},
		{"/api/stream/not-an-id", http.StatusBadRequest, "bad_id"},
	} {
		rec = serve(server, jsonReq(http.MethodGet, tc.path, ""))
		if rec.Code != tc.want {
			t.Fatalf("%s status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
		var apiErr struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &apiErr); err != nil {
			t.Fatal(err)
		}
		if apiErr.Code != tc.code {
			t.Fatalf("%s code=%q, want %q", tc.path, apiErr.Code, tc.code)
		}
	}
}

// Browsers refuse a FLAC that starts with an ID3v2 tag; the stream skips the
// tag, ranges included, and leaves other files byte for byte.
func TestStreamSkipsID3InFrontOfFLAC(t *testing.T) {
	server := openTestServer(t)
	tag := []byte{'I', 'D', '3', 4, 0, 0, 0, 0, 0, 5, 'p', 'a', 'd', 0, 0}
	flac := []byte("fLaC-stream-body")
	path := filepath.Join(t.TempDir(), "tagged.flac")
	if err := os.WriteFile(path, append(append([]byte{}, tag...), flac...), 0o600); err != nil {
		t.Fatal(err)
	}
	loadIndex(t, server, []db.TrackRow{{
		ID: 12, Path: path, Title: "Tagged", Artist: "Artist", Album: "Album",
		Duration: 180, Embedding: index.Float32Bytes([]float32{1, 12}), Dim: 2,
	}})

	rec := serve(server, jsonReq(http.MethodGet, "/api/stream/12", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != string(flac) {
		t.Fatalf("stream status=%d body=%q, want %q", rec.Code, rec.Body.String(), flac)
	}
	req := jsonReq(http.MethodGet, "/api/stream/12", "")
	req.Header.Set("Range", "bytes=4-7")
	rec = serve(server, req)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "-str" {
		t.Fatalf("range status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 4-7/16" {
		t.Fatalf("Content-Range=%q", got)
	}
}

func TestReloadRequiresAuthOrLoopback(t *testing.T) {
	server := openTestServer(t)
	server.Auth = auth.New(auth.Config{APIToken: "test-token"})

	req := jsonReq(http.MethodPost, "/api/reload", "")
	req.RemoteAddr = "203.0.113.10:1234"
	rec := serve(server, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("remote unauthenticated status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = jsonReq(http.MethodPost, "/api/reload", "")
	req.RemoteAddr = "127.0.0.1:1234"
	rec = serve(server, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = jsonReq(http.MethodPost, "/api/reload", "")
	req.RemoteAddr = "203.0.113.10:1234"
	req.Header.Set("Authorization", "Bearer test-token")
	rec = serve(server, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer status=%d body=%s", rec.Code, rec.Body.String())
	}
}
