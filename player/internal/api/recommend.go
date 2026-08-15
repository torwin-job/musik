package api

import (
	"math/rand"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/index"
)

type simPair struct {
	row int
	sim float32
}

func (s *Server) topTrackSims(vec []float32, exclude map[int64]bool, limit int) []map[string]any {
	if len(vec) == 0 || limit <= 0 {
		return nil
	}
	sims := s.Idx.SimsTo(vec)
	pairs := make([]simPair, 0, len(sims))
	for i, v := range sims {
		m := s.Idx.MetaAt(i)
		if exclude[m.ID] {
			continue
		}
		pairs = append(pairs, simPair{i, v})
	}
	sort.Slice(pairs, func(a, b int) bool { return pairs[a].sim > pairs[b].sim })
	if len(pairs) > limit {
		pairs = pairs[:limit]
	}
	out := make([]map[string]any, 0, len(pairs))
	for _, p := range pairs {
		m := s.Idx.MetaAt(p.row)
		item := map[string]any{
			"id": m.ID, "artist": m.Artist, "title": m.Title, "album": m.Album,
			"duration": m.Duration, "cosine": float64(p.sim),
			"stream":      "/api/stream/" + strconv.FormatInt(m.ID, 10),
			"explanation": "похоже по звучанию",
		}
		if m.ArtworkPath != "" {
			item["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) similarArtists(seedArtist string, limit int) []map[string]any {
	seedRows := s.Idx.RowsForArtist(seedArtist)
	seedVec := s.Idx.CentroidOf(seedRows)
	if seedVec == nil {
		return nil
	}
	type ag struct {
		artist string
		rows   []int
	}
	by := map[string]*ag{}
	n := s.Idx.Size()
	seedKey := strings.ToLower(strings.TrimSpace(seedArtist))
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		name := strings.TrimSpace(m.Artist)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if key == seedKey {
			continue
		}
		a := by[key]
		if a == nil {
			a = &ag{artist: name}
			by[key] = a
		}
		a.rows = append(a.rows, i)
	}
	type scored struct {
		artist string
		rows   []int
		sim    float32
	}
	var all []scored
	for _, a := range by {
		v := s.Idx.CentroidOf(a.rows)
		if v == nil {
			continue
		}
		var sim float32
		for d := range v {
			sim += v[d] * seedVec[d]
		}
		all = append(all, scored{a.artist, a.rows, sim})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]map[string]any, 0, len(all))
	for _, a := range all {
		m := s.Idx.MetaAt(a.rows[0])
		item := map[string]any{
			"type": "artist", "artist": a.artist, "tracks": len(a.rows),
			"cosine": float64(a.sim), "cover_track_id": m.ID,
			"explanation": "похоже на «" + seedArtist + "»",
		}
		if m.ArtworkPath != "" {
			item["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) similarAlbums(seedArtist, seedAlbum string, limit int) []map[string]any {
	seedRows := s.Idx.RowsForAlbum(seedArtist, seedAlbum)
	seedVec := s.Idx.CentroidOf(seedRows)
	if seedVec == nil {
		return nil
	}
	type alb struct {
		artist, album string
		rows          []int
	}
	by := map[string]*alb{}
	n := s.Idx.Size()
	seedAl := strings.ToLower(strings.TrimSpace(seedAlbum))
	seedAr := strings.ToLower(strings.TrimSpace(seedArtist))
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		album := strings.TrimSpace(m.Album)
		if album == "" {
			continue
		}
		artist := strings.TrimSpace(m.Artist)
		if strings.ToLower(album) == seedAl && (seedAr == "" || strings.ToLower(artist) == seedAr) {
			continue
		}
		key := strings.ToLower(artist) + "\x00" + strings.ToLower(album)
		a := by[key]
		if a == nil {
			a = &alb{artist: artist, album: album}
			by[key] = a
		}
		a.rows = append(a.rows, i)
	}
	type scored struct {
		artist, album string
		rows          []int
		sim           float32
	}
	var all []scored
	for _, a := range by {
		v := s.Idx.CentroidOf(a.rows)
		if v == nil {
			continue
		}
		var sim float32
		for d := range v {
			sim += v[d] * seedVec[d]
		}
		all = append(all, scored{a.artist, a.album, a.rows, sim})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]map[string]any, 0, len(all))
	for _, a := range all {
		m := s.Idx.MetaAt(a.rows[0])
		item := map[string]any{
			"type": "album", "artist": a.artist, "album": a.album, "tracks": len(a.rows),
			"cosine": float64(a.sim), "cover_track_id": m.ID,
			"explanation": "похоже на «" + seedAlbum + "»",
		}
		if m.ArtworkPath != "" {
			item["artwork"] = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
		}
		out = append(out, item)
	}
	return out
}

func (s *Server) handleSimilarArtists(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	if artist == "" {
		http.Error(w, "artist required", 400)
		return
	}
	writeJSON(w, map[string]any{
		"seed":    artist,
		"artists": s.similarArtists(artist, 12),
	})
}

func (s *Server) handleSimilarAlbums(w http.ResponseWriter, r *http.Request) {
	artist := strings.TrimSpace(r.URL.Query().Get("artist"))
	album := strings.TrimSpace(r.URL.Query().Get("album"))
	if album == "" {
		http.Error(w, "album required", 400)
		return
	}
	writeJSON(w, map[string]any{
		"seed":   map[string]string{"artist": artist, "album": album},
		"albums": s.similarAlbums(artist, album, 12),
	})
}

func (s *Server) handleRecommendSeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := strings.ToLower(strings.TrimSpace(q.Get("type")))
	limit := 20
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 50 {
			limit = n
		}
	}
	exclude := map[int64]bool{}
	var vec []float32
	seed := map[string]any{"type": typ}

	switch typ {
	case "track", "song", "":
		id, _ := strconv.ParseInt(q.Get("track_id"), 10, 64)
		if id == 0 {
			id, _ = strconv.ParseInt(q.Get("id"), 10, 64)
		}
		if id == 0 {
			writeErr(w, 400, "track_id", "track_id required")
			return
		}
		row, ok := s.Idx.RowOf(id)
		if !ok {
			writeErr(w, 404, "not_found", "track not in index")
			return
		}
		exclude[id] = true
		vec = s.Idx.Vector(row)
		seed["track_id"] = id
		seed["track"] = s.trackJSON(id)
	case "artist":
		artist := strings.TrimSpace(q.Get("artist"))
		if artist == "" {
			writeErr(w, 400, "artist", "artist required")
			return
		}
		rows := s.Idx.RowsForArtist(artist)
		for _, ri := range rows {
			exclude[s.Idx.MetaAt(ri).ID] = true
		}
		vec = s.Idx.CentroidOf(rows)
		seed["artist"] = artist
	case "album":
		artist := strings.TrimSpace(q.Get("artist"))
		album := strings.TrimSpace(q.Get("album"))
		if album == "" {
			writeErr(w, 400, "album", "album required")
			return
		}
		rows := s.Idx.RowsForAlbum(artist, album)
		for _, ri := range rows {
			exclude[s.Idx.MetaAt(ri).ID] = true
		}
		vec = s.Idx.CentroidOf(rows)
		seed["artist"] = artist
		seed["album"] = album
	default:
		writeErr(w, 400, "type", "type must be track|artist|album")
		return
	}
	if vec == nil {
		writeErr(w, 404, "empty", "no embeddings for seed")
		return
	}
	writeJSON(w, map[string]any{
		"ok": true, "seed": seed,
		"tracks": s.topTrackSims(vec, exclude, limit),
	})
}

func (s *Server) handleRecommendFavorites(w http.ResponseWriter, _ *http.Request) {
	exclude := map[int64]bool{}
	if recent, err := s.Store.RecentTrackIDs(24*7, 500); err == nil {
		for _, id := range recent {
			exclude[id] = true
		}
	}
	var vecs [][]float32
	basedOn := map[string]any{}

	favTracks, _ := s.Store.FavoritesList()
	trackTitles := make([]string, 0, len(favTracks))
	for _, t := range favTracks {
		exclude[t.TrackID] = true
		if row, ok := s.Idx.RowOf(t.TrackID); ok {
			vecs = append(vecs, s.Idx.Vector(row))
			trackTitles = append(trackTitles, t.Title)
		}
	}
	basedOn["tracks"] = trackTitles

	favArtists, _ := s.Store.FavArtistsList()
	artistNames := make([]string, 0, len(favArtists))
	for _, a := range favArtists {
		artistNames = append(artistNames, a.Artist)
		rows := s.Idx.RowsForArtist(a.Artist)
		for _, r := range rows {
			exclude[s.Idx.MetaAt(r).ID] = true
		}
		if v := s.Idx.CentroidOf(rows); v != nil {
			vecs = append(vecs, v)
		}
	}
	basedOn["artists"] = artistNames

	favAlbums, _ := s.Store.FavAlbumsList()
	albumNames := make([]string, 0, len(favAlbums))
	for _, a := range favAlbums {
		albumNames = append(albumNames, a.Artist+" — "+a.Album)
		rows := s.Idx.RowsForAlbum(a.Artist, a.Album)
		for _, r := range rows {
			exclude[s.Idx.MetaAt(r).ID] = true
		}
		if v := s.Idx.CentroidOf(rows); v != nil {
			vecs = append(vecs, v)
		}
	}
	basedOn["albums"] = albumNames

	if len(vecs) == 0 {
		writeJSON(w, map[string]any{
			"ok": false, "empty": true,
			"hint":     "Добавь любимые песни, артистов или альбомы (♥)",
			"tracks":   []any{},
			"artists":  []any{},
			"albums":   []any{},
			"based_on": basedOn,
		})
		return
	}

	dim := len(vecs[0])
	sum := make([]float64, dim)
	used := 0
	for _, v := range vecs {
		if len(v) != dim {
			continue
		}
		for d := 0; d < dim; d++ {
			sum[d] += float64(v[d])
		}
		used++
	}
	q := make([]float32, dim)
	inv := 1.0 / float64(used)
	for d := 0; d < dim; d++ {
		q[d] = float32(sum[d] * inv)
	}
	index.Normalize(q)

	tracks := s.topTrackSims(q, exclude, 48)
	daySeed, _ := strconv.ParseInt(time.Now().Format("20060102"), 10, 64)
	rng := rand.New(rand.NewSource(daySeed))
	rng.Shuffle(len(tracks), func(i, j int) { tracks[i], tracks[j] = tracks[j], tracks[i] })
	if len(tracks) > 24 {
		tracks = tracks[:24]
	}

	var simArtists []map[string]any
	var simAlbums []map[string]any
	day := time.Now().YearDay()
	if len(favArtists) > 0 {
		seed := favArtists[day%len(favArtists)]
		simArtists = s.similarArtists(seed.Artist, 10)
	} else if len(favTracks) > 0 {
		seed := favTracks[day%len(favTracks)]
		simArtists = s.similarArtists(seed.Artist, 10)
	}
	if len(favAlbums) > 0 {
		seed := favAlbums[day%len(favAlbums)]
		simAlbums = s.similarAlbums(seed.Artist, seed.Album, 10)
	} else if len(favTracks) > 0 {
		seed := favTracks[day%len(favTracks)]
		if row, ok := s.Idx.RowOf(seed.TrackID); ok {
			m := s.Idx.MetaAt(row)
			if m.Album != "" {
				simAlbums = s.similarAlbums(m.Artist, m.Album, 10)
			}
		}
	}

	writeJSON(w, map[string]any{
		"ok": true, "based_on": basedOn,
		"tracks": tracks, "artists": simArtists, "albums": simAlbums,
		"explanation": "на основе твоих любимых — по звучанию (CLAP)",
	})
}
