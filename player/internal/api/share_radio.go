package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
)

func (s *Server) handleShareRadioCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "musik radio"
	}
	token, err := randomToken(16)
	if err != nil {
		writeErr(w, 500, "token", err.Error())
		return
	}
	sh, err := s.Store.CreateRadioShare(token, name)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	base := s.publicBase(r)
	writeJSON(w, map[string]any{
		"ok":         true,
		"token":      sh.Token,
		"name":       sh.Name,
		"created_at": sh.CreatedAt,
		"url":        base + "/listen/" + sh.Token + ".mp3",
		"url_bare":   base + "/listen/" + sh.Token,
	})
}

func (s *Server) handleShareRadioList(w http.ResponseWriter, r *http.Request) {
	include := r.URL.Query().Get("all") == "1"
	list, err := s.Store.ListRadioShares(include)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	base := s.publicBase(r)
	type row struct {
		Token        string  `json:"token"`
		Name         string  `json:"name"`
		CreatedAt    string  `json:"created_at"`
		RevokedAt    *string `json:"revoked_at,omitempty"`
		LastListenAt *string `json:"last_listen_at,omitempty"`
		ListenCount  int     `json:"listen_count"`
		Active       bool    `json:"active"`
		URL          string  `json:"url"`
	}
	out := make([]row, 0, len(list))
	for _, sh := range list {
		out = append(out, row{
			Token: sh.Token, Name: sh.Name, CreatedAt: sh.CreatedAt,
			RevokedAt: sh.RevokedAt, LastListenAt: sh.LastListenAt,
			ListenCount: sh.ListenCount, Active: sh.Active,
			URL: base + "/listen/" + sh.Token + ".mp3",
		})
	}
	writeJSON(w, map[string]any{"shares": out, "count": len(out)})
}

func (s *Server) handleShareRadioRevoke(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.PathValue("token"))
	if token == "" {
		writeErr(w, 400, "token", "token required")
		return
	}
	if err := s.Store.RevokeRadioShare(token); err != nil {
		writeErr(w, 404, "not_found", "share not found")
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleListenShare serves a continuous MP3 radio stream for a share token.
// Public (token is the credential). Does not update the owner's taste profile.
func (s *Server) handleListenShare(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSuffix(r.PathValue("token"), ".mp3")
	token = strings.TrimSpace(token)
	if token == "" {
		writeErr(w, 400, "token", "token required")
		return
	}
	_, ok, err := s.Store.GetActiveRadioShare(token)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if !ok {
		writeErr(w, 404, "not_found", "share not found or revoked")
		return
	}
	if s.Idx.Size() == 0 {
		writeErr(w, 503, "empty", "library empty")
		return
	}
	ffmpeg := s.Cfg.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if _, err := exec.LookPath(ffmpeg); err != nil {
		writeErr(w, 503, "ffmpeg", "ffmpeg not found — install ffmpeg or set MUSIK_FFMPEG")
		return
	}

	max := s.Cfg.ShareMaxListeners
	if max < 1 {
		max = 4
	}
	if int(atomic.LoadInt32(&s.shareListeners)) >= max {
		writeErr(w, 503, "busy", "too many share listeners")
		return
	}
	atomic.AddInt32(&s.shareListeners, 1)
	defer atomic.AddInt32(&s.shareListeners, -1)
	_ = s.Store.TouchRadioShareListen(token)

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("icy-name", "musik radio")
	w.Header().Set("icy-genre", "Various")
	w.Header().Set("icy-pub", "0")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	s.mu.Lock()
	sess := s.newSession("share")
	startID := s.pickStartTrackLocked(sess, nil)
	sess.Current = startID
	s.excludeTrackLocked(sess, startID)
	s.refreshQueueFor(sess, startID, false)
	s.mu.Unlock()

	ctx := r.Context()
	bitrate := s.Cfg.ShareBitrate
	if bitrate == "" {
		bitrate = "192k"
	}
	for tracks := 0; tracks < 5000; tracks++ {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		id := sess.Current
		path := ""
		if row, ok := s.Idx.RowOf(id); ok {
			path = s.Idx.MetaAt(row).Path
		}
		s.mu.Unlock()
		if path == "" {
			return
		}
		if _, err := os.Stat(path); err != nil {
			log.Printf("share listen: missing file id=%d", id)
			s.mu.Lock()
			s.advanceShareSessionLocked(sess)
			s.mu.Unlock()
			continue
		}
		if err := pipeTrackMP3(ctx, w, flusher, ffmpeg, path, bitrate); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("share listen ffmpeg: %v", err)
		}
		s.mu.Lock()
		s.advanceShareSessionLocked(sess)
		s.mu.Unlock()
	}
}

func (s *Server) advanceShareSessionLocked(sess *PlaySession) {
	next := s.advanceSession(sess)
	if next != 0 {
		return
	}
	// Radio share never ends: pick a new seed when the queue is exhausted.
	next = s.pickStartTrackLocked(sess, nil)
	if next == 0 {
		return
	}
	sess.Mode = "share"
	sess.Current = next
	s.excludeTrackLocked(sess, next)
	s.refreshQueueFor(sess, next, false)
}

func pipeTrackMP3(ctx context.Context, w io.Writer, flusher http.Flusher, ffmpeg, path, bitrate string) error {
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", path,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", bitrate,
		"-ar", "44100",
		"-ac", "2",
		"-f", "mp3",
		"pipe:1",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	buf := make([]byte, 32*1024)
	for {
		if ctx.Err() != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return ctx.Err()
		}
		n, rerr := stdout.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return werr
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			waitErr := cmd.Wait()
			if rerr == io.EOF {
				return waitErr
			}
			return rerr
		}
	}
}

func (s *Server) publicBase(r *http.Request) string {
	if s.Cfg.PublicBaseURL != "" {
		return s.Cfg.PublicBaseURL
	}
	scheme := "http"
	if r.TLS != nil || s.Cfg.SecureCookie {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "https" || p == "http" {
		scheme = p
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1:8787"
	}
	return scheme + "://" + host
}

func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
