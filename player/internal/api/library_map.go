package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

type libraryMapLabel struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Color    string  `json:"color"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	TrackIDs []int64 `json:"track_ids"`
}

type libraryMapDoc struct {
	Method    string            `json:"method"`
	UpdatedAt string            `json:"updated_at"`
	Genres    []string          `json:"genres"`
	Points    [][]float64       `json:"points"` // [track_id, x, y, genre index or -1, neighbour ids...]
	Labels    []libraryMapLabel `json:"labels"`
}

// mapTransitionLimit caps the transitions layer; the strongest steps come first.
const mapTransitionLimit = 3000

// handleLibraryMap serves the worker-built 2D map, limited to the tracks this
// profile may see: the worker places every track, private folders included.
// The profile's own listened transitions come along; plays, favourites and
// clusters the page already has from /api/library and /api/favorites.
func (s *Server) handleLibraryMap(w http.ResponseWriter, _ *http.Request) {
	catalog, err := s.Store.ListCatalogTracks()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	raw, err := s.Store.LibraryMapJSON()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	doc := libraryMapDoc{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			writeErr(w, 500, "library_map", err.Error())
			return
		}
	}
	visible := make(map[int64]bool, len(catalog))
	for _, t := range catalog {
		if !mapTrackVisible(t.Path, s.Store.ProfileID) {
			continue
		}
		visible[t.ID] = true
	}
	points := make([][]float64, 0, len(doc.Points))
	for _, p := range doc.Points {
		if len(p) < 4 || !visible[int64(p[0])] {
			continue
		}
		// A neighbour in someone else's private folder is not even named.
		for i := 4; i < len(p); i++ {
			if p[i] >= 0 && !visible[int64(p[i])] {
				p[i] = -1
			}
		}
		points = append(points, p)
	}
	steps, err := s.Store.ListMapTransitions(mapTransitionLimit)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	transitions := make([][3]int64, 0, len(steps))
	for _, t := range steps {
		if visible[t.From] && visible[t.To] {
			transitions = append(transitions, [3]int64{t.From, t.To, int64(t.Count)})
		}
	}
	labels := make([]libraryMapLabel, 0, len(doc.Labels))
	for _, l := range doc.Labels {
		ids := make([]int64, 0, len(l.TrackIDs))
		for _, id := range l.TrackIDs {
			if visible[id] {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			continue
		}
		l.TrackIDs = ids
		labels = append(labels, l)
	}
	genres := doc.Genres
	if genres == nil {
		genres = []string{}
	}
	writeJSON(w, map[string]any{
		"ready":       raw != "",
		"method":      doc.Method,
		"updated_at":  doc.UpdatedAt,
		"total":       len(visible),
		"genres":      genres,
		"points":      points,
		"labels":      labels,
		"transitions": transitions,
	})
}

// mapTrackVisible keeps another profile's private folder out of every part of
// the map response. A private path has the form _private/<profile>/....
func mapTrackVisible(path, profileID string) bool {
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	for i, part := range parts {
		if part != "_private" {
			continue
		}
		return profileID != "" && i+2 < len(parts) && parts[i+1] == profileID
	}
	return true
}
