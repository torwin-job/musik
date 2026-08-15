package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
)

const (
	Version    = "1.0.0"
	APIVersion = "v1"
)

type Server struct {
	Cfg     config.Config
	Store   *db.Store
	Idx     *index.Index
	Taste   *taste.Profile // shared per user (one listener)
	Builder *queue.Builder
	Static  http.FileSystem
	HTTP    *http.Client
	Auth    *auth.Gate

	mu             sync.Mutex
	sessions       map[string]*PlaySession
	shareListeners int32
	loginLimiter   *auth.LoginLimiter
	transMu        sync.RWMutex
	transitions    map[int64]map[int64]float64 // from → to → weight

	Warm          *StreamWarm
	mobileFlight  *mobileFlight
	mobileVariant sync.Map // trackID → "mobile"|"original" (sticky for Range seeks)
}

func New(cfg config.Config, store *db.Store, idx *index.Index, tp *taste.Profile, staticFS http.FileSystem) *Server {
	var secret []byte
	if cfg.SessionSecret != "" {
		secret = []byte(cfg.SessionSecret)
	}
	gate := auth.New(auth.Config{
		Password:      cfg.Password,
		APIToken:      cfg.APIToken,
		SessionSecret: secret,
		Disabled:      cfg.AuthDisabled, // only explicit MUSIK_AUTH_DISABLED=1
		SecureCookie:  cfg.SecureCookie,
	})
	return &Server{
		Cfg: cfg, Store: store, Idx: idx, Taste: tp,
		Builder:      queue.NewBuilder(idx, cfg),
		Static:       staticFS,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		Auth:         gate,
		sessions:     map[string]*PlaySession{},
		loginLimiter: auth.NewLoginLimiter(5, time.Minute),
		Warm:         newStreamWarm(8),
		mobileFlight: &mobileFlight{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /api/auth/me", s.handleAuthMe)
	mux.HandleFunc("POST /api/auth/login", s.handleAuthLogin)
	mux.HandleFunc("POST /api/auth/logout", s.handleAuthLogout)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/profile", s.handleProfile)
	mux.HandleFunc("GET /api/library", s.handleLibrary)
	mux.HandleFunc("GET /api/artists", s.handleArtists)
	mux.HandleFunc("GET /api/albums", s.handleAlbums)
	mux.HandleFunc("GET /api/tracks/{id}", s.handleTrack)
	mux.HandleFunc("GET /api/stream/{id}", s.handleStream)
	mux.HandleFunc("GET /api/artwork/{id}", s.handleArtwork)
	mux.HandleFunc("GET /api/similar/{id}", s.handleSimilar)
	mux.HandleFunc("POST /api/reload", s.handleReload)
	mux.HandleFunc("POST /api/session/start", s.handleSessionStart)
	mux.HandleFunc("POST /api/session/jump", s.handleSessionJump)
	mux.HandleFunc("POST /api/radio/start", s.handleRadioStart)
	mux.HandleFunc("POST /api/share/radio", s.handleShareRadioCreate)
	mux.HandleFunc("GET /api/share/radio", s.handleShareRadioList)
	mux.HandleFunc("DELETE /api/share/radio/{token}", s.handleShareRadioRevoke)
	mux.HandleFunc("GET /listen/{token}", s.handleListenShare)
	mux.HandleFunc("POST /api/play", s.handlePlay)
	mux.HandleFunc("GET /api/queue", s.handleQueue)
	mux.HandleFunc("POST /api/queue/refresh", s.handleQueueRefresh)
	mux.HandleFunc("POST /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/now", s.handleNow)
	mux.HandleFunc("GET /api/tracks/{id}/lyrics", s.handleTrackLyrics)
	mux.HandleFunc("GET /api/playlists/daily/today", s.handleDailyToday)
	mux.HandleFunc("POST /api/playlists/daily/play", s.handleDailyPlay)
	mux.HandleFunc("GET /api/playlists/{kind}/latest", s.handlePlaylistLatest)
	mux.HandleFunc("GET /api/mixes", s.handleMixes)
	mux.HandleFunc("POST /api/mixes/{kind}/play", s.handleMixPlay)
	mux.HandleFunc("GET /api/later", s.handleLaterList)
	mux.HandleFunc("POST /api/later", s.handleLaterAdd)
	mux.HandleFunc("DELETE /api/later", s.handleLaterRemove)
	mux.HandleFunc("GET /api/favorites", s.handleFavoritesList)
	mux.HandleFunc("POST /api/favorites", s.handleFavoritesAdd)
	mux.HandleFunc("DELETE /api/favorites", s.handleFavoritesRemove)
	mux.HandleFunc("POST /api/favorites/toggle", s.handleFavoritesToggle)
	mux.HandleFunc("GET /api/favorites/status", s.handleFavoritesStatus)
	mux.HandleFunc("GET /api/similar/artists", s.handleSimilarArtists)
	mux.HandleFunc("GET /api/similar/albums", s.handleSimilarAlbums)
	mux.HandleFunc("GET /api/recommend/favorites", s.handleRecommendFavorites)
	mux.HandleFunc("GET /api/recommend/seed", s.handleRecommendSeed)
	mux.HandleFunc("GET /api/discover/albums", s.handleDiscoverAlbums)
	mux.HandleFunc("GET /api/discover/resurfaced", s.handleDiscoverResurfaced)
	mux.HandleFunc("POST /api/library/rescan", s.handleLibraryRescan)
	mux.HandleFunc("POST /api/jobs/{kind}", s.handleEnqueueJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("GET /api/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/metrics/weekly", s.handleWeeklyMetrics)
	mux.HandleFunc("GET /manifest.webmanifest", s.handleManifest)
	mux.Handle("/", s.staticHandler())
	var h http.Handler = mux
	if s.Auth != nil {
		h = s.Auth.Middleware(h)
	}
	return withCORS(h, s.Cfg.CORSOrigins)
}

func withCORS(next http.Handler, allowed []string) http.Handler {
	allowSet := map[string]bool{}
	for _, o := range allowed {
		allowSet[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			ok := false
			if len(allowSet) == 0 {
				// same-origin only: reflect if Origin host matches request Host
				if u, err := parseOriginHost(origin); err == nil && u == r.Host {
					ok = true
				}
			} else if allowSet[origin] || allowSet["*"] {
				ok = true
			}
			if ok {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseOriginHost(origin string) (string, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return "", errors.New("bad origin")
	}
	return u.Host, nil
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg, "code": errCode})
}

func (s *Server) staticHandler() http.Handler {
	if s.Static == nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(s.Static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "" {
			f, err := s.Static.Open("index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			stat, err := f.Stat()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			rs, ok := f.(io.ReadSeeker)
			if !ok {
				r.URL.Path = "/index.html"
				fileServer.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", modTime(stat), rs)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func modTime(info fs.FileInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.ModTime()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{
		"ok": true, "version": Version, "api_version": APIVersion,
		"auth": s.Auth != nil && s.Auth.Cfg.Enabled(),
	}
	if s.Auth == nil || !s.Auth.Cfg.Enabled() || s.Auth.Authorized(r) {
		out["tracks"] = s.Idx.Size()
		out["dim"] = s.Idx.Dim()
	}
	writeJSON(w, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mat := s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
	explore := s.Taste.EffectiveExplore(s.Cfg.ExploreRatio, s.Cfg.DiscoverExploreRatio, s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
	out := map[string]any{
		"tracks": s.Idx.Size(), "dim": s.Idx.Dim(),
		"taste_ready":      s.Taste.Ready(),
		"maturity":         mat,
		"sessions":         len(s.sessions),
		"explore":          explore,
		"db":               s.Cfg.DBPath,
		"worker_url":       s.Cfg.WorkerURL,
		"worker_autostart": s.Cfg.WorkerAutostart,
	}
	if sid := r.URL.Query().Get("session_id"); sid != "" {
		if sess := s.getSession(sid); sess != nil {
			out["session_id"] = sess.ID
			out["mode"] = sess.Mode
			out["current"] = sess.Current
			out["queue_len"] = len(sess.Queue)
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	n := s.Idx.Size()
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	type row struct {
		ID       int64   `json:"id"`
		Artist   string  `json:"artist"`
		Title    string  `json:"title"`
		Album    string  `json:"album"`
		Duration float64 `json:"duration"`
		Cluster  int     `json:"cluster_id"`
		Artwork  string  `json:"artwork,omitempty"`
	}
	out := make([]row, 0, n)
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		if artist != "" && !strings.EqualFold(strings.TrimSpace(m.Artist), artist) {
			continue
		}
		if album != "" && !strings.EqualFold(strings.TrimSpace(m.Album), album) {
			continue
		}
		art := ""
		if m.ArtworkPath != "" {
			art = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, row{m.ID, m.Artist, m.Title, m.Album, m.Duration, m.ClusterID, art})
	}
	writeJSON(w, out)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
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
	path := s.Idx.MetaAt(row).Path
	q := strings.ToLower(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.ToLower(r.URL.Query().Get("fmt"))
	}
	if q == "mobile" || q == "aac" || q == "mp3" {
		s.serveMobileStream(w, r, id, path)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "file missing", 404)
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	w.Header().Set("Content-Type", contentType(path))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), f)
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", 400)
		return
	}
	row, ok := s.Idx.RowOf(id)
	if !ok {
		http.Error(w, "not in index", 404)
		return
	}
	sims := s.Idx.SimsTo(s.Idx.Vector(row))
	type nb struct {
		ID     int64   `json:"id"`
		Artist string  `json:"artist"`
		Title  string  `json:"title"`
		Cosine float64 `json:"cosine"`
	}
	type pair struct {
		i int
		s float32
	}
	var all []pair
	md5 := s.Idx.MetaAt(row).FileMD5
	for i, sVal := range sims {
		if i == row {
			continue
		}
		m := s.Idx.MetaAt(i)
		if md5 != "" && m.FileMD5 == md5 {
			continue
		}
		all = append(all, pair{i, sVal})
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].s > all[i].s {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	limit := 10
	if len(all) < limit {
		limit = len(all)
	}
	out := make([]nb, 0, limit)
	for _, p := range all[:limit] {
		m := s.Idx.MetaAt(p.i)
		out = append(out, nb{m.ID, m.Artist, m.Title, float64(p.s)})
	}
	writeJSON(w, out)
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	// Auth gate skips /api/reload so the worker can call it; still require
	// loopback or a valid session/Bearer when auth is enabled.
	if s.Auth != nil && s.Auth.Cfg.Enabled() && !s.Auth.Authorized(r) && !isLoopback(r) {
		writeErr(w, 401, "auth_required", "unauthorized")
		return
	}
	if err := s.Reload(); err != nil {
		writeErr(w, 500, "reload", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "tracks": s.Idx.Size()})
}

func isLoopback(r *http.Request) bool {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func (s *Server) Reload() error {
	rows, err := s.Store.LoadReadyTracks()
	if err != nil {
		return err
	}
	if err := s.Idx.Load(rows); err != nil {
		return err
	}
	pos, neg, _ := s.Store.ListenSignalCounts()
	if blob, err := s.Store.LatestProfile("global"); err == nil && len(blob) > 0 {
		v := index.BytesToFloat32(blob)
		if len(v) == s.Idx.Dim() {
			s.Taste.SetWithMeta(v, pos, neg, "online_ema")
		}
	} else if !s.Taste.Ready() {
		s.Taste.SetWithMeta(s.Idx.Centroid(), pos, neg, "centroid")
	} else {
		s.Taste.SetCounts(pos, neg)
	}
	log.Printf("reloaded index n=%d dim=%d maturity=%s", s.Idx.Size(), s.Idx.Dim(),
		s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt))
	s.reloadTransitions()
	return nil
}

func (s *Server) maturityLocked() string {
	return s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
}

func (s *Server) discoverModeLocked() bool {
	return s.maturityLocked() == taste.StatusDiscovering
}

func (s *Server) exploreLocked() float64 {
	return s.Taste.EffectiveExplore(s.Cfg.ExploreRatio, s.Cfg.DiscoverExploreRatio,
		s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
}

func (s *Server) handleSessionStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SeedTrackID *int64 `json:"seed_track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.newSession("session")
	startID := s.pickStartTrackLocked(sess, req.SeedTrackID)
	sess.Current = startID
	sess.Prev = 0
	s.refreshQueueFor(sess, startID, true)
	writeJSON(w, map[string]any{
		"session_id": sess.ID,
		"mode":       sess.Mode,
		"maturity":   s.maturityLocked(),
		"current":    s.trackJSON(startID),
		"queue":      sess.Queue,
	})
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.requireSession(w, r, "")
	if sess == nil {
		return
	}
	writeJSON(w, map[string]any{
		"session_id": sess.ID,
		"current":    sess.Current, "mode": sess.Mode, "maturity": s.maturityLocked(), "queue": sess.Queue,
	})
}

func (s *Server) handleQueueRefresh(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.requireSession(w, r, "")
	if sess == nil {
		return
	}
	s.refreshQueueFor(sess, sess.Current, true)
	writeJSON(w, map[string]any{"session_id": sess.ID, "queue": sess.Queue, "maturity": s.maturityLocked()})
}

func (s *Server) handleNow(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.requireSession(w, r, "")
	if sess == nil {
		return
	}
	out := map[string]any{
		"session_id": sess.ID,
		"mode":       sess.Mode,
		"maturity":   s.maturityLocked(),
		"current":    s.trackJSON(sess.Current),
		"queue":      sess.Queue,
		"name":       sess.PlaylistName,
		"kind":       sess.PlaylistKind,
		"index":      sess.DailyPos,
		"count":      len(sess.DailyIDs),
		"fixed":      isFixedMode(sess.Mode),
	}
	if isFixedMode(sess.Mode) {
		out["tracks"] = s.sessionTracks(sess)
	}
	writeJSON(w, out)
}

type eventReq struct {
	Type        string   `json:"type"`
	TrackID     int64    `json:"track_id"`
	SessionID   string   `json:"session_id"`
	PositionSec *float64 `json:"position_sec"`
	DurationSec *float64 `json:"duration_sec"`
	ListenedSec *float64 `json:"listened_sec"`
	Reason      string   `json:"reason"`
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	var ev eventReq
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.requireSession(w, r, ev.SessionID)
	if sess == nil {
		return
	}
	ev.SessionID = sess.ID

	switch ev.Type {
	case "track_start":
		if sess.Current != 0 && sess.Current != ev.TrackID {
			sess.Prev = sess.Current
		}
		sess.Current = ev.TrackID
		s.excludeTrackLocked(sess, ev.TrackID)
		_, _ = s.Store.InsertListen(ev.TrackID, "start", "player", ev.SessionID, "",
			ev.PositionSec, ev.DurationSec, nil)
		if sess.Prev != 0 && sess.Prev != ev.TrackID {
			_ = s.Store.BumpTransition(sess.Prev, ev.TrackID, 1.0)
			s.bumpTransitionMem(sess.Prev, ev.TrackID, 1.0)
		}
		// Queue was built on radio/start or last advance — don't rescore on every start.
		if len(sess.DailyIDs) == 0 && len(sess.Queue) < s.Cfg.QueueSize/2 {
			s.refreshQueueFor(sess, ev.TrackID, true)
		}

	case "progress":
		// Clients may tick often; persist to SQLite at most every 45s.
		if sess.LastProgressWrite.IsZero() ||
			time.Since(sess.LastProgressWrite) >= 45*time.Second {
			_, _ = s.Store.InsertListen(ev.TrackID, "progress", "player", ev.SessionID, "",
				ev.PositionSec, ev.DurationSec, ev.ListenedSec)
			sess.LastProgressWrite = time.Now()
		}
		// Do not reshuffle the visible queue mid-track — that desynced
		// "далее" from the track that actually played on skip/end.
		// Top up only when the queue ran short (e.g. after jumps).
		if len(sess.DailyIDs) == 0 &&
			sess.Current != 0 &&
			len(sess.Queue) < s.Cfg.QueueSize/2 &&
			time.Since(sess.LastQueueAt) >= 15*time.Second {
			s.refreshQueueFor(sess, sess.Current, false)
		}

	case "track_end", "skip":
		reason := ev.Reason
		if ev.Type == "skip" && reason == "" {
			reason = "skipped"
		}
		listened := 0.0
		if ev.ListenedSec != nil {
			listened = *ev.ListenedSec
		} else if ev.PositionSec != nil {
			listened = *ev.PositionSec
		}
		dur := 0.0
		if ev.DurationSec != nil {
			dur = *ev.DurationSec
		} else if row, ok := s.Idx.RowOf(ev.TrackID); ok {
			dur = s.Idx.MetaAt(row).Duration
		}
		_, _ = s.Store.InsertListen(ev.TrackID, "track_end", "player", ev.SessionID, reason,
			ev.PositionSec, ev.DurationSec, &listened)
		signed, _ := taste.WeightFromListen(listened, dur, reason)
		if row, ok := s.Idx.RowOf(ev.TrackID); ok && signed != 0 {
			s.Taste.UpdateEMA(s.Idx.Vector(row), signed, s.Cfg.TasteAlpha)
			s.persistTasteProfiles(s.Idx.Vector(row), signed, s.Cfg.TasteAlpha)
		}
		if reason == "skipped" && dur > 0 && listened/dur < 0.3 {
			_ = s.Store.BumpRecStats(ev.TrackID, 0, 1, 0)
			s.Idx.BumpSkipEarlyLocal(ev.TrackID)
		}
		if reason == "completed" || (dur > 0 && listened/dur >= 0.8) {
			_ = s.Store.BumpRecStats(ev.TrackID, 0, 0, 1)
			s.Idx.BumpCompletedLocal(ev.TrackID)
		}
		s.excludeTrackLocked(sess, ev.TrackID)
		nextID := s.advanceSession(sess)
		var next any
		if nextID != 0 {
			next = s.trackJSON(nextID)
		}
		out := map[string]any{
			"ok": true, "signed_weight": signed,
			"session_id": sess.ID,
			"maturity":   s.maturityLocked(),
			"next":       next,
			"queue":      sess.Queue,
			"mode":       sess.Mode,
			"next_id":    nextID,
			"ended":      nextID == 0,
			"index":      sess.DailyPos,
			"name":       sess.PlaylistName,
			"fixed":      isFixedMode(sess.Mode),
		}
		if isFixedMode(sess.Mode) {
			out["tracks"] = s.sessionTracks(sess)
		}
		writeJSON(w, out)
		return

	case "like", "dislike":
		if sess.Rated == nil {
			sess.Rated = map[int64]string{}
		}
		prev := sess.Rated[ev.TrackID]
		if prev == ev.Type {
			// duplicate like/dislike on same track in this session — no-op
			writeJSON(w, map[string]any{
				"ok": true, "ignored": true, "reason": "already_" + ev.Type,
				"session_id": sess.ID, "current": sess.Current, "queue": sess.Queue,
				"maturity": s.maturityLocked(), "rating": prev,
			})
			return
		}
		wSign := taste.LikeWeight()
		if ev.Type == "dislike" {
			wSign = taste.DislikeWeight()
		}
		// flip: cancel previous explicit rating before applying the new one
		if prev != "" {
			undo := taste.DislikeWeight()
			if prev == "dislike" {
				undo = taste.LikeWeight()
			}
			if row, ok := s.Idx.RowOf(ev.TrackID); ok {
				vec := s.Idx.Vector(row)
				s.Taste.UpdateEMA(vec, undo, s.Cfg.TasteAlpha)
				s.persistTasteProfiles(vec, undo, s.Cfg.TasteAlpha)
			}
		}
		_, _ = s.Store.InsertListen(ev.TrackID, ev.Type, "player", ev.SessionID, "",
			nil, nil, nil)
		if row, ok := s.Idx.RowOf(ev.TrackID); ok {
			vec := s.Idx.Vector(row)
			s.Taste.UpdateEMA(vec, wSign, s.Cfg.TasteAlpha)
			s.persistTasteProfiles(vec, wSign, s.Cfg.TasteAlpha)
		}
		sess.Rated[ev.TrackID] = ev.Type
		if len(sess.DailyIDs) == 0 {
			s.refreshQueueFor(sess, sess.Current, true)
		}
		writeJSON(w, map[string]any{
			"ok": true, "ignored": false, "rating": ev.Type, "flipped": prev != "",
			"session_id": sess.ID, "current": sess.Current, "queue": sess.Queue,
			"maturity": s.maturityLocked(),
		})
		return

	default:
		http.Error(w, "unknown event type", 400)
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "session_id": sess.ID, "current": sess.Current, "queue": sess.Queue, "maturity": s.maturityLocked(),
	})
}

func (s *Server) trackJSON(id int64) any {
	row, ok := s.Idx.RowOf(id)
	if !ok {
		return nil
	}
	m := s.Idx.MetaAt(row)
	out := map[string]any{
		"id": m.ID, "artist": m.Artist, "title": m.Title, "album": m.Album,
		"duration": m.Duration, "cluster_id": m.ClusterID,
		"stream": "/api/stream/" + strconv.FormatInt(m.ID, 10),
	}
	if m.ArtworkPath != "" {
		out["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
	}
	return out
}

func (s *Server) proxyWorker(method, path string, body any) (map[string]any, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.Cfg.WorkerURL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func contentType(path string) string {
	switch filepath.Ext(path) {
	case ".mp3":
		return "audio/mpeg"
	case ".flac":
		return "audio/flac"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".m4a":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	default:
		return "application/octet-stream"
	}
}
