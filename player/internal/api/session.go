package api

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
)

// PlaySession holds per-tab / per-client playback state.
// Taste profile is shared (one user); only current/queue/exclude are isolated.
type PlaySession struct {
	ID           string
	Mode         string // radio|session|daily|playlist|later|listen|share
	Current      int64
	Prev         int64
	Queue        []queue.Item
	Exclude      map[int64]bool
	Rated        map[int64]string // track_id → like|dislike
	DailyIDs     []int64
	DailyPos     int
	PlaylistName string
	PlaylistKind string
	LastQueueAt  time.Time
	// Throttle SQLite writes for progress events (position still updates in RAM).
	LastProgressWrite time.Time
	UpdatedAt         time.Time
}

func (s *Server) newSession(mode string) *PlaySession {
	id := strconv.FormatInt(time.Now().UnixNano(), 36)
	sess := &PlaySession{
		ID:        id,
		Mode:      mode,
		Exclude:   map[int64]bool{},
		Rated:     map[int64]string{},
		UpdatedAt: time.Now(),
	}
	s.sessions[id] = sess
	s.gcSessionsLocked()
	s.persistSessionLocked(sess)
	return sess
}

func (s *Server) getSession(id string) *PlaySession {
	if id == "" {
		return nil
	}
	sess := s.sessions[id]
	if sess != nil {
		sess.UpdatedAt = time.Now()
		return sess
	}
	// hydrate from SQLite after player restart
	row, ok, err := s.Store.LoadPlaySession(id)
	if err != nil || !ok {
		return nil
	}
	sess = hydrateSession(row)
	s.sessions[id] = sess
	sess.UpdatedAt = time.Now()
	return sess
}

func hydrateSession(row db.PlaySessionRow) *PlaySession {
	sess := &PlaySession{
		ID:           row.ID,
		Mode:         row.Mode,
		Current:      row.CurrentID,
		Exclude:      map[int64]bool{},
		Rated:        map[int64]string{},
		DailyPos:     row.DailyPos,
		PlaylistName: row.PlaylistName,
		PlaylistKind: row.PlaylistKind,
		UpdatedAt:    time.Now(),
	}
	if t, err := time.Parse(time.RFC3339Nano, row.UpdatedAt); err == nil {
		sess.UpdatedAt = t
	}
	if row.QueueJSON != "" {
		_ = json.Unmarshal([]byte(row.QueueJSON), &sess.Queue)
	}
	if row.ExcludeJSON != "" {
		var ids []int64
		if json.Unmarshal([]byte(row.ExcludeJSON), &ids) == nil {
			for _, id := range ids {
				sess.Exclude[id] = true
			}
		}
	}
	if row.RatedJSON != "" {
		_ = json.Unmarshal([]byte(row.RatedJSON), &sess.Rated)
	}
	if row.DailyIDsJSON != "" {
		_ = json.Unmarshal([]byte(row.DailyIDsJSON), &sess.DailyIDs)
	}
	return sess
}

func (s *Server) persistSessionLocked(sess *PlaySession) {
	if sess == nil {
		return
	}
	ex := make([]int64, 0, len(sess.Exclude))
	for id := range sess.Exclude {
		ex = append(ex, id)
	}
	qj, _ := json.Marshal(sess.Queue)
	ej, _ := json.Marshal(ex)
	rj, _ := json.Marshal(sess.Rated)
	dj, _ := json.Marshal(sess.DailyIDs)
	_ = s.Store.UpsertPlaySession(db.PlaySessionRow{
		ID: sess.ID, Mode: sess.Mode, CurrentID: sess.Current,
		QueueJSON: string(qj), ExcludeJSON: string(ej), RatedJSON: string(rj),
		DailyIDsJSON: string(dj), DailyPos: sess.DailyPos,
		PlaylistName: sess.PlaylistName, PlaylistKind: sess.PlaylistKind,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request, bodySessionID string) *PlaySession {
	id := bodySessionID
	if id == "" {
		id = r.URL.Query().Get("session_id")
	}
	if id == "" {
		id = r.Header.Get("X-Session-Id")
	}
	sess := s.getSession(id)
	if sess == nil {
		http.Error(w, `{"error":"unknown or missing session_id — call /api/radio/start or /api/session/start"}`, 404)
		return nil
	}
	return sess
}

func (s *Server) gcSessionsLocked() {
	cutoffRAM := time.Now().Add(-6 * time.Hour)
	for id, sess := range s.sessions {
		if sess.UpdatedAt.Before(cutoffRAM) {
			delete(s.sessions, id)
		}
	}
	_ = s.Store.DeleteStalePlaySessions(time.Now().Add(-7 * 24 * time.Hour))
	const maxSessions = 64
	if len(s.sessions) <= maxSessions {
		if n, err := s.Store.CountPlaySessions(); err == nil && n > maxSessions {
			ids, _ := s.Store.OldestPlaySessionIDs(n - maxSessions)
			for _, id := range ids {
				_ = s.Store.DeletePlaySession(id)
				delete(s.sessions, id)
			}
		}
		return
	}
	for len(s.sessions) > maxSessions {
		var oldestID string
		var oldestTime time.Time
		first := true
		for id, sess := range s.sessions {
			if first || sess.UpdatedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = sess.UpdatedAt
				first = false
			}
		}
		delete(s.sessions, oldestID)
		_ = s.Store.DeletePlaySession(oldestID)
	}
}

// excludeTrackLocked marks trackID and all MD5 / artist+title clones as heard.
func (s *Server) excludeTrackLocked(sess *PlaySession, trackID int64) {
	if trackID == 0 || sess == nil {
		return
	}
	if sess.Exclude == nil {
		sess.Exclude = map[int64]bool{}
	}
	for _, id := range s.Idx.CloneIDs(trackID) {
		sess.Exclude[id] = true
	}
}

// seedRadioExcludeLocked blocks recently played songs (and their clones) for this session.
func (s *Server) seedRadioExcludeLocked(sess *PlaySession) {
	ids, err := s.Store.RecentTrackIDs(48, 120)
	if err != nil {
		return
	}
	for _, id := range ids {
		s.excludeTrackLocked(sess, id)
	}
}

func (s *Server) pickStartTrackLocked(sess *PlaySession, seed *int64) int64 {
	var startID int64
	if seed != nil {
		startID = *seed
	} else if s.discoverModeLocked() {
		startID = s.Builder.PickRandom(sess.Exclude)
	} else {
		startID = s.pickTasteStartLocked(sess)
	}
	if _, ok := s.Idx.RowOf(startID); !ok && s.Idx.Size() > 0 {
		startID = s.Idx.MetaAt(0).ID
	}
	return startID
}

func (s *Server) pickTasteStartLocked(sess *PlaySession) int64 {
	n := s.Idx.Size()
	if n == 0 {
		return 0
	}
	recent := map[int64]bool{}
	if ids, err := s.Store.RecentTrackIDs(36, 50); err == nil {
		for _, id := range ids {
			recent[id] = true
		}
	}
	for id := range sess.Exclude {
		recent[id] = true
	}

	sims := s.Idx.SimsTo(s.tasteForQueueLocked())
	type pair struct {
		row int
		sim float32
	}
	all := make([]pair, 0, n)
	for i, v := range sims {
		all = append(all, pair{i, v})
	}
	sort.Slice(all, func(a, b int) bool { return all[a].sim > all[b].sim })

	if s.Builder.Rng.Float64() < 0.12 && n > 8 {
		lo := n / 2
		hi := min(n, lo+max(8, n/5))
		pool := all[lo:hi]
		for tries := 0; tries < 12 && len(pool) > 0; tries++ {
			p := pool[s.Builder.Rng.Intn(len(pool))]
			id := s.Idx.MetaAt(p.row).ID
			if !recent[id] {
				return id
			}
		}
	}

	k := 20
	if k > len(all) {
		k = len(all)
	}
	candidates := make([]pair, 0, k)
	for _, p := range all[:k] {
		id := s.Idx.MetaAt(p.row).ID
		sim := p.sim
		if recent[id] {
			sim -= 0.35
		}
		candidates = append(candidates, pair{p.row, sim})
	}
	allRecent := true
	for _, p := range candidates {
		if !recent[s.Idx.MetaAt(p.row).ID] {
			allRecent = false
			break
		}
	}
	if allRecent {
		candidates = candidates[:0]
		for _, p := range all[:k] {
			candidates = append(candidates, p)
		}
	}

	const temp = 0.08
	weights := make([]float64, len(candidates))
	var sum float64
	maxSim := candidates[0].sim
	for i, p := range candidates {
		w := math.Exp(float64(p.sim-maxSim) / temp)
		weights[i] = w
		sum += w
	}
	if sum <= 0 {
		return s.Idx.MetaAt(candidates[0].row).ID
	}
	r := s.Builder.Rng.Float64() * sum
	for i, w := range weights {
		r -= w
		if r <= 0 {
			return s.Idx.MetaAt(candidates[i].row).ID
		}
	}
	return s.Idx.MetaAt(candidates[len(candidates)-1].row).ID
}

func (s *Server) tasteForQueueLocked() []float32 {
	g := s.Taste.Get()
	if len(g) == 0 {
		return g
	}
	dp := db.DayPart(time.Now().Hour())
	blob, err := s.Store.LatestProfile(dp)
	if err != nil || len(blob) == 0 {
		return g
	}
	d := index.BytesToFloat32(blob)
	if len(d) != len(g) {
		return g
	}
	out := make([]float32, len(g))
	for i := range g {
		out[i] = 0.7*g[i] + 0.3*d[i]
	}
	index.Normalize(out)
	return out
}

func (s *Server) transitionsFromLocked(currentID int64) map[int64]float64 {
	s.transMu.RLock()
	defer s.transMu.RUnlock()
	if s.transitions == nil {
		return nil
	}
	src := s.transitions[currentID]
	if len(src) == 0 {
		return nil
	}
	out := make(map[int64]float64, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func (s *Server) refreshQueueFor(sess *PlaySession, currentID int64, countImpressions bool) {
	opts := queue.BuildOpts{
		ExploreRatio:    s.exploreLocked(),
		Discover:        s.discoverModeLocked(),
		TransitionsFrom: s.transitionsFromLocked(currentID),
	}
	sess.Queue = s.Builder.BuildOpts(currentID, s.tasteForQueueLocked(), sess.Exclude, opts)
	sess.LastQueueAt = time.Now()
	sess.UpdatedAt = time.Now()
	s.persistSessionLocked(sess)
	s.scheduleWarm(sess)
	if !countImpressions {
		return
	}
	for _, q := range sess.Queue {
		_ = s.Store.BumpRecStats(q.TrackID, 1, 0, 0)
		s.Idx.BumpShownLocal(q.TrackID)
	}
}

func (s *Server) rebuildDailyQueueFor(sess *PlaySession) {
	sess.Queue = nil
	limit := s.Cfg.QueueSize
	if isFixedMode(sess.Mode) {
		limit = 50
	}
	for i := sess.DailyPos + 1; i < len(sess.DailyIDs) && len(sess.Queue) < limit; i++ {
		id := sess.DailyIDs[i]
		row, ok := s.Idx.RowOf(id)
		if !ok {
			continue
		}
		m := s.Idx.MetaAt(row)
		label := sess.PlaylistName
		if label == "" {
			label = "плейлист"
		}
		sess.Queue = append(sess.Queue, queue.Item{
			TrackID: m.ID, Artist: m.Artist, Title: m.Title, Album: m.Album,
			Path: m.Path, Duration: m.Duration, Explanation: label,
		})
	}
	s.persistSessionLocked(sess)
	s.scheduleWarm(sess)
}

func (s *Server) advanceSession(sess *PlaySession) int64 {
	nextID := int64(0)
	if len(sess.DailyIDs) > 0 {
		sess.DailyPos++
		if sess.DailyPos < len(sess.DailyIDs) {
			nextID = sess.DailyIDs[sess.DailyPos]
			sess.Prev = sess.Current
			sess.Current = nextID
			s.excludeTrackLocked(sess, nextID)
			s.rebuildDailyQueueFor(sess)
			sess.UpdatedAt = time.Now()
			s.persistSessionLocked(sess)
			s.scheduleWarm(sess)
			return nextID
		}
		if isFixedMode(sess.Mode) {
			sess.DailyPos = len(sess.DailyIDs) - 1
			sess.Queue = nil
			sess.UpdatedAt = time.Now()
			s.persistSessionLocked(sess)
			return 0
		}
		sess.DailyIDs = nil
		sess.Mode = "radio"
	}
	// Honor the queue the UI already showed — do NOT rebuild before dequeue
	// (that swapped queue[0] and made "далее" lie about the next track).
	if len(sess.Queue) > 0 {
		nextID = sess.Queue[0].TrackID
		sess.Queue = sess.Queue[1:]
	} else if sess.Mode == "radio" || sess.Mode == "session" || sess.Mode == "share" {
		nextID = s.Builder.PickRandom(sess.Exclude)
	}
	sess.Prev = sess.Current
	if nextID != 0 {
		sess.Current = nextID
		s.excludeTrackLocked(sess, nextID)
		// Keep skip fast: only rebuild when the visible tail is short.
		if len(sess.Queue) < s.Cfg.QueueSize/2 {
			s.refreshQueueFor(sess, nextID, true)
		} else {
			sess.UpdatedAt = time.Now()
			s.persistSessionLocked(sess)
			s.scheduleWarm(sess)
		}
	} else {
		sess.UpdatedAt = time.Now()
		s.persistSessionLocked(sess)
	}
	return nextID
}

// persistTasteProfiles saves global + daypart EMA snapshots (with prune).
func (s *Server) persistTasteProfiles(trackVec []float32, signed, alpha float64) {
	blob := index.Float32Bytes(s.Taste.Get())
	_ = s.Store.SaveProfile("global", blob)
	_ = s.Store.PruneProfiles("global", 50)

	dp := db.DayPart(time.Now().Hour())
	prev, err := s.Store.LatestProfile(dp)
	if err != nil || len(prev) == 0 || len(trackVec) == 0 {
		_ = s.Store.SaveProfile(dp, blob)
		_ = s.Store.PruneProfiles(dp, 50)
		return
	}
	v := index.BytesToFloat32(prev)
	if len(v) != len(trackVec) {
		_ = s.Store.SaveProfile(dp, blob)
		_ = s.Store.PruneProfiles(dp, 50)
		return
	}
	// one EMA step on daypart copy (same signed weight as global)
	a := float32(alpha)
	if a <= 0 {
		a = 0.1
	}
	w := float32(signed)
	for i := range v {
		v[i] = (1-a)*v[i] + a*w*trackVec[i]
	}
	index.Normalize(v)
	_ = s.Store.SaveProfile(dp, index.Float32Bytes(v))
	_ = s.Store.PruneProfiles(dp, 50)
}

func (s *Server) reloadTransitions() {
	g, err := s.Store.LoadTransitionGraph()
	if err != nil {
		return
	}
	s.transMu.Lock()
	s.transitions = g
	s.transMu.Unlock()
}

func (s *Server) bumpTransitionMem(from, to int64, w float64) {
	if from == 0 || to == 0 {
		return
	}
	s.transMu.Lock()
	defer s.transMu.Unlock()
	if s.transitions == nil {
		s.transitions = map[int64]map[int64]float64{}
	}
	m := s.transitions[from]
	if m == nil {
		m = map[int64]float64{}
		s.transitions[from] = m
	}
	m[to] += w
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
