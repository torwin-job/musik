package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/torwin-job/musik/player/internal/library"
)

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	type row struct {
		ID       int64    `json:"id"`
		Artist   string   `json:"artist"`
		Artists  []string `json:"artists,omitempty"`
		Title    string   `json:"title"`
		Album    string   `json:"album"`
		Duration float64  `json:"duration"`
		Cluster  int      `json:"cluster_id"`
		Artwork  string   `json:"artwork,omitempty"`
		Ready    bool     `json:"ready"`
		Status   string   `json:"status,omitempty"`
	}
	limit := queryLimit(r, 0, 2000)
	needAll := artist != "" || album != ""
	scanLimit := 0
	if !needAll {
		scanLimit = limit
	}
	catalog, err := s.Store.ListCatalogTracksLimit(scanLimit)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	out := make([]row, 0, len(catalog))
	if len(catalog) > 0 {
		for _, m := range catalog {
			if !library.MatchArtistAlbum(m.Artist, m.Artists, m.Album, artist, album) {
				continue
			}
			art := ""
			if m.Artwork != "" {
				art = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
			}
			out = append(out, row{
				ID: m.ID, Artist: m.Artist, Artists: m.Artists, Title: m.Title, Album: m.Album,
				Duration: m.Duration, Cluster: m.Cluster, Artwork: art,
				Ready: m.Status == "ready", Status: m.Status,
			})
			if needAll && limit > 0 && len(out) >= limit {
				break
			}
		}
		writeJSON(w, out)
		return
	}
	n := s.Idx.Size()
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		if !library.MatchArtistAlbum(m.Artist, m.Artists, m.Album, artist, album) {
			continue
		}
		art := ""
		if m.ArtworkPath != "" {
			art = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, row{m.ID, m.Artist, m.Artists, m.Title, m.Album, m.Duration, m.ClusterID, art, true, "ready"})
		if limit > 0 && len(out) >= limit {
			break
		}
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
	return s.App.Reload()
}
