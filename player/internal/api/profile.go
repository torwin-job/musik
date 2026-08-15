package api

import (
	"net/http"

	"github.com/torwin-job/musik/player/internal/taste"
)

func (s *Server) handleProfile(w http.ResponseWriter, _ *http.Request) {
	pos, neg := s.Taste.Counts()
	if dbPos, dbNeg, err := s.Store.ListenSignalCounts(); err == nil {
		// keep RAM counters at least as high as DB (reload may lag)
		if dbPos > pos {
			pos = dbPos
		}
		if dbNeg > neg {
			neg = dbNeg
		}
		s.Taste.SetCounts(pos, neg)
	}
	mat := s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
	explore := s.Taste.EffectiveExplore(s.Cfg.ExploreRatio, s.Cfg.DiscoverExploreRatio,
		s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt)
	artists, _ := s.Store.TopArtists(5)
	clusters, _ := s.Store.TopClusters(5)
	confidence := float64(pos) / float64(s.Cfg.ProfileReadyAt)
	if confidence > 1 {
		confidence = 1
	}
	writeJSON(w, map[string]any{
		"ready":            mat == taste.StatusReady,
		"maturity":         mat,
		"n_positive":       pos,
		"n_negative":       neg,
		"ready_at":         s.Cfg.ProfileReadyAt,
		"forming_at":       s.Cfg.ProfileFormingAt,
		"confidence":       confidence,
		"explore_ratio":    explore,
		"source":           s.Taste.SourceName(),
		"taste_vector":     s.Taste.Ready(),
		"top_artists":      artists,
		"top_clusters":     clusters,
		"online_authority": "go_ema",
		"online_context":   "global",
		"offline_context":  "offline_report",
		"offline_note":     "Python writes only offline_report; Go owns global EMA",
	})
}
