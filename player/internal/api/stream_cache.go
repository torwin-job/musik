package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// disk + singleflight for mobile AAC/MP3 cache under /data/cache/stream.
type flightEntry struct {
	done chan struct{}
	err  error
}

type mobileFlight struct {
	mu   sync.Mutex
	wait map[int64]*flightEntry
}

func (f *mobileFlight) do(id int64, fn func() error) error {
	f.mu.Lock()
	if f.wait == nil {
		f.wait = map[int64]*flightEntry{}
	}
	if e, ok := f.wait[id]; ok {
		f.mu.Unlock()
		<-e.done
		return e.err
	}
	e := &flightEntry{done: make(chan struct{})}
	f.wait[id] = e
	f.mu.Unlock()

	e.err = fn()

	f.mu.Lock()
	delete(f.wait, id)
	close(e.done)
	f.mu.Unlock()
	return e.err
}

func (s *Server) streamCacheDir() string {
	dbDir := filepath.Dir(s.Cfg.DBPath)
	dataDir := filepath.Dir(dbDir)
	return filepath.Join(dataDir, "cache", "stream")
}

func (s *Server) mobileBitrate() string {
	b := strings.TrimSpace(s.Cfg.MobileBitrate)
	if b == "" {
		return "160k"
	}
	return b
}

func (s *Server) mobileFormat() string {
	f := strings.ToLower(strings.TrimSpace(s.Cfg.MobileFormat))
	if f == "mp3" {
		return "mp3"
	}
	return "aac"
}

func (s *Server) mobileCachePath(id int64, format string) string {
	br := strings.TrimSuffix(strings.ToLower(s.mobileBitrate()), "k")
	ext := ".m4a"
	if format == "mp3" {
		ext = ".mp3"
	}
	return filepath.Join(s.streamCacheDir(), fmt.Sprintf("%d_%sk%s", id, br, ext))
}

func (s *Server) mobileContentType(format string) string {
	if format == "mp3" {
		return "audio/mpeg"
	}
	return "audio/mp4"
}

// skipMobileTranscode: tiny already-lossy files need no re-encode.
// Larger MP3/M4A (often 256–320k) still go through ffmpeg → 160k.
func skipMobileTranscode(srcPath string) bool {
	ext := strings.ToLower(filepath.Ext(srcPath))
	switch ext {
	case ".mp3", ".m4a", ".aac", ".opus", ".ogg":
		st, err := os.Stat(srcPath)
		if err != nil {
			return false
		}
		// ~160kbps × ~3.5 min ≈ 4MB — above that, shrink for LTE.
		return st.Size() > 0 && st.Size() <= 4<<20
	default:
		return false
	}
}

// lookupMobileCache returns an existing mobile file without running ffmpeg.
func (s *Server) lookupMobileCache(id int64, srcPath string) (path string, ctype string, ok bool) {
	if skipMobileTranscode(srcPath) {
		return srcPath, contentType(srcPath), true
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", "", false
	}
	format := s.mobileFormat()
	dst := s.mobileCachePath(id, format)
	if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
		return dst, s.mobileContentType(format), true
	}
	if format == "aac" {
		mp3Path := s.mobileCachePath(id, "mp3")
		if dstInfo, err := os.Stat(mp3Path); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
			return mp3Path, s.mobileContentType("mp3"), true
		}
	}
	return "", "", false
}

func (s *Server) scheduleMobileTranscode(id int64, srcPath string) {
	go func() {
		path, ctype, err := s.ensureMobileFile(id, srcPath)
		if err != nil {
			log.Printf("mobile transcode %d: %v", id, err)
			return
		}
		s.loadWarmFromFile(id, path, ctype)
	}()
}

// ensureMobileFile returns path + content-type for the mobile profile (disk cache).
// May block on ffmpeg — use only from background warm/transcode, not the request path.
func (s *Server) ensureMobileFile(id int64, srcPath string) (path string, ctype string, err error) {
	if path, ctype, ok := s.lookupMobileCache(id, srcPath); ok {
		return path, ctype, nil
	}

	format := s.mobileFormat()
	dst := s.mobileCachePath(id, format)
	ctype = s.mobileContentType(format)

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return "", "", err
	}

	if s.mobileFlight == nil {
		s.mobileFlight = &mobileFlight{}
	}
	ferr := s.mobileFlight.do(id, func() error {
		// Re-check after waiting.
		if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
			return nil
		}
		return s.transcodeMobile(srcPath, dst, format)
	})
	if ferr != nil {
		// Fallback: try mp3 if aac failed.
		if format == "aac" {
			mp3Path := s.mobileCachePath(id, "mp3")
			mp3Type := s.mobileContentType("mp3")
			if dstInfo, err := os.Stat(mp3Path); err == nil && !dstInfo.ModTime().Before(srcInfo.ModTime()) && dstInfo.Size() > 0 {
				return mp3Path, mp3Type, nil
			}
			// Negative key space for mp3 fallback flight (same track).
			if err := s.mobileFlight.do(-id, func() error {
				return s.transcodeMobile(srcPath, mp3Path, "mp3")
			}); err != nil {
				return "", "", ferr
			}
			return mp3Path, mp3Type, nil
		}
		return "", "", ferr
	}
	return dst, ctype, nil
}

func (s *Server) transcodeMobile(src, dst, format string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	ffmpeg := s.Cfg.FFmpegPath
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	tmp := dst + ".tmp"
	_ = os.Remove(tmp)

	bitrate := s.mobileBitrate()
	var args []string
	if format == "mp3" {
		// Explicit -f: temp name ends in .tmp so ffmpeg cannot sniff the muxer.
		args = []string{
			"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-vn",
			"-acodec", "libmp3lame", "-b:a", bitrate, "-ar", "44100", "-ac", "2",
			"-f", "mp3",
			tmp,
		}
	} else {
		args = []string{
			"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-i", src, "-vn",
			"-c:a", "aac", "-b:a", bitrate, "-ar", "44100", "-ac", "2",
			"-movflags", "+faststart",
			"-f", "mp4",
			tmp,
		}
	}
	cmd := exec.Command(ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("ffmpeg mobile: %s", msg)
	}
	st, err := os.Stat(tmp)
	if err != nil || st.Size() == 0 {
		_ = os.Remove(tmp)
		return fmt.Errorf("ffmpeg mobile: empty output")
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// StreamWarm keeps recent mobile stream bytes in RAM for fast Range serves.
type StreamWarm struct {
	mu    sync.Mutex
	max   int
	order []int64
	data  map[int64]warmEntry
}

type warmEntry struct {
	bytes []byte
	ctype string
	mtime time.Time
}

func newStreamWarm(max int) *StreamWarm {
	if max < 4 {
		max = 8
	}
	return &StreamWarm{
		max:  max,
		data: map[int64]warmEntry{},
	}
}

func (w *StreamWarm) Get(id int64) (warmEntry, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e, ok := w.data[id]
	if !ok {
		return warmEntry{}, false
	}
	// bump LRU
	for i, x := range w.order {
		if x == id {
			w.order = append(w.order[:i], w.order[i+1:]...)
			break
		}
	}
	w.order = append(w.order, id)
	return e, true
}

func (w *StreamWarm) Put(id int64, e warmEntry) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.data[id]; ok {
		for i, x := range w.order {
			if x == id {
				w.order = append(w.order[:i], w.order[i+1:]...)
				break
			}
		}
	}
	w.data[id] = e
	w.order = append(w.order, id)
	for len(w.order) > w.max {
		old := w.order[0]
		w.order = w.order[1:]
		delete(w.data, old)
	}
}

func (w *StreamWarm) Pin(ids []int64) {
	// Keep pinned ids from immediate eviction by touching them after load;
	// max capacity already limits total RAM.
	_ = ids
}

func (s *Server) loadWarmFromFile(id int64, path, ctype string) {
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return
	}
	st, _ := os.Stat(path)
	mtime := time.Now()
	if st != nil {
		mtime = st.ModTime()
	}
	if s.Warm == nil {
		s.Warm = newStreamWarm(8)
	}
	s.Warm.Put(id, warmEntry{bytes: raw, ctype: ctype, mtime: mtime})
}

func (s *Server) ensureWarm(id int64) {
	if id == 0 {
		return
	}
	if s.Warm != nil {
		if _, ok := s.Warm.Get(id); ok {
			return
		}
	}
	row, ok := s.Idx.RowOf(id)
	if !ok {
		return
	}
	src := s.Idx.MetaAt(row).Path
	path, ctype, err := s.ensureMobileFile(id, src)
	if err != nil {
		log.Printf("warm track %d: %v", id, err)
		return
	}
	s.loadWarmFromFile(id, path, ctype)
}

func collectWarmIDs(sess *PlaySession) []int64 {
	if sess == nil {
		return nil
	}
	seen := map[int64]bool{}
	var ids []int64
	add := func(id int64) {
		if id == 0 || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	add(sess.Current)
	for i, q := range sess.Queue {
		if i >= 6 {
			break
		}
		add(q.TrackID)
	}
	if len(sess.DailyIDs) > 0 {
		for i := sess.DailyPos; i < len(sess.DailyIDs) && len(ids) < 7; i++ {
			add(sess.DailyIDs[i])
		}
	}
	return ids
}

func (s *Server) scheduleWarm(sess *PlaySession) {
	if s.Warm == nil {
		return
	}
	ids := collectWarmIDs(sess)
	if len(ids) == 0 {
		return
	}
	go func(ids []int64) {
		for _, id := range ids {
			s.ensureWarm(id)
		}
	}(append([]int64(nil), ids...))
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, path, ctype string) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file missing", 404)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	} else {
		w.Header().Set("Content-Type", contentType(path))
	}
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), f)
}

func (s *Server) serveMobileCached(w http.ResponseWriter, r *http.Request, id int64, srcPath string) bool {
	if s.Warm != nil {
		if e, ok := s.Warm.Get(id); ok && len(e.bytes) > 0 {
			w.Header().Set("Content-Type", e.ctype)
			http.ServeContent(w, r, "track"+strconv.FormatInt(id, 10), e.mtime, bytes.NewReader(e.bytes))
			return true
		}
	}
	if path, ctype, ok := s.lookupMobileCache(id, srcPath); ok {
		go s.loadWarmFromFile(id, path, ctype)
		s.serveFile(w, r, path, ctype)
		return true
	}
	return false
}

func (s *Server) serveMobileStream(w http.ResponseWriter, r *http.Request, id int64, srcPath string) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Cache-Control", "private, max-age=86400")

	isRange := r.Header.Get("Range") != ""

	// Sticky variant: a mid-track Range must not switch MP3↔AAC (breaks ExoPlayer seek).
	if isRange {
		if v, ok := s.mobileVariant.Load(id); ok {
			if v.(string) == "mobile" {
				if s.serveMobileCached(w, r, id, srcPath) {
					return
				}
			}
			// "original" (or mobile missing) — keep original bytes for this playthrough.
			s.serveFile(w, r, srcPath, contentType(srcPath))
			return
		}
		// Range before any full GET — prefer stable original.
		s.mobileVariant.Store(id, "original")
		s.serveFile(w, r, srcPath, contentType(srcPath))
		return
	}

	// Fresh open (no Range): pick best and stick for subsequent seeks.
	if s.serveMobileCached(w, r, id, srcPath) {
		s.mobileVariant.Store(id, "mobile")
		return
	}
	s.mobileVariant.Store(id, "original")
	s.scheduleMobileTranscode(id, srcPath)
	s.serveFile(w, r, srcPath, contentType(srcPath))
}
