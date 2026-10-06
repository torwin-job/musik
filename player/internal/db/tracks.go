package db

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// parseSegments decodes the JSON array stored in tracks.artist_segments.
// A malformed value yields nil; callers fall back to the raw artist label.
func parseSegments(raw string) []string {
	s := strings.TrimSpace(raw)
	if s == "" || s == "[]" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	clean := out[:0]
	for _, name := range out {
		name = strings.TrimSpace(name)
		if name != "" {
			clean = append(clean, name)
		}
	}
	return append([]string(nil), clean...)
}

func (s *Store) LoadReadyTracks() ([]TrackRow, error) {
	rows, err := s.DB.Query(`
SELECT t.id, t.path, COALESCE(t.title,''), COALESCE(t.artist,''), COALESCE(t.artist_segments,''),
       COALESCE(t.album,''),
       COALESCE(t.duration,0), COALESCE(t.file_md5,''), COALESCE(t.created_at,''),
       COALESCE(t.artwork_path,''), COALESCE(f.cluster_id, -1),
       f.embedding, COALESCE(f.embedding_dim,0),
       COALESCE(rs.shown,0), COALESCE(rs.skipped_early,0), COALESCE(rs.completed,0),
       COALESCE(t.year, 0), f.bpm, COALESCE(f.lufs, t.lufs), COALESCE(f.key_name,''),
       COALESCE(ts.plays,0), COALESCE(ts.finishes,0), COALESCE(ts.early_skips,0),
       COALESCE(ts.last_played_at,'')
FROM tracks t
JOIN features f ON f.track_id = t.id
LEFT JOIN rec_stats rs ON rs.track_id = t.id
LEFT JOIN track_stats ts ON ts.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
  AND f.status = 'ready' AND f.embedding IS NOT NULL
ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackRow
	for rows.Next() {
		var tr TrackRow
		var bpm, lufs sql.NullFloat64
		var segments string
		if err := rows.Scan(&tr.ID, &tr.Path, &tr.Title, &tr.Artist, &segments, &tr.Album,
			&tr.Duration, &tr.FileMD5, &tr.CreatedAt, &tr.ArtworkPath, &tr.ClusterID,
			&tr.Embedding, &tr.Dim, &tr.Shown, &tr.SkipEarly, &tr.Completed,
			&tr.Year, &bpm, &lufs, &tr.KeyName,
			&tr.Plays, &tr.Finishes, &tr.EarlySkips, &tr.LastPlayedAt); err != nil {
			return nil, err
		}
		tr.ArtistSegments = parseSegments(segments)
		if bpm.Valid {
			tr.BPM, tr.HasBPM = bpm.Float64, true
		}
		if lufs.Valid {
			tr.LUFS, tr.HasLUFS = lufs.Float64, true
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

type CatalogTrack struct {
	ID       int64
	Path     string
	Title    string
	Artist   string
	Artists  []string
	Album    string
	Duration float64
	Artwork  string
	Cluster  int
	Status   string
}

func (s *Store) ListCatalogTracks() ([]CatalogTrack, error) {
	return s.ListCatalogTracksLimit(0)
}

func (s *Store) ListCatalogTracksLimit(limit int) ([]CatalogTrack, error) {
	q := `
SELECT t.id, t.path, COALESCE(t.title,''), COALESCE(t.artist,''), COALESCE(t.artist_segments,''),
       COALESCE(t.album,''), COALESCE(t.duration,0), COALESCE(t.artwork_path,''),
       COALESCE(f.cluster_id, -1), COALESCE(f.status, 'pending')
FROM tracks t
LEFT JOIN features f ON f.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
ORDER BY t.id`
	var args []any
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogTrack
	for rows.Next() {
		var tr CatalogTrack
		var segments string
		if err := rows.Scan(&tr.ID, &tr.Path, &tr.Title, &tr.Artist, &segments, &tr.Album,
			&tr.Duration, &tr.Artwork, &tr.Cluster, &tr.Status); err != nil {
			return nil, err
		}
		tr.Artists = parseSegments(segments)
		out = append(out, tr)
	}
	return out, rows.Err()
}

func (s *Store) TrackPath(id int64) (string, error) {
	var path string
	err := s.DB.QueryRow(`SELECT path FROM tracks WHERE id = ? AND is_active = 1`, id).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}
