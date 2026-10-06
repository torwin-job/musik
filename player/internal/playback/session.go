package playback

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
)

// Session holds per-tab / per-client playback state.
// Taste profile is shared (one user); only current/queue/exclude are isolated.
type Session struct {
	mu                sync.Mutex
	ID                string
	Mode              string // radio|session|daily|playlist|later|listen|share
	Current           int64
	CurrentItem       queue.Item
	Prev              int64
	BackStack         []queue.Item
	Queue             []queue.Item
	Exclude           map[int64]bool
	Rated             map[int64]string // track_id → like|dislike
	DailyIDs          []int64
	DailyPos          int
	PlaylistName      string
	PlaylistKind      string
	LastQueueAt       time.Time
	LastProgressWrite time.Time
	UpdatedAt         time.Time
	TasteState         taste.SessionState
	ActiveContextIDs   []string
	TransitionProfile  string
}

func (s *Session) Lock()   { s.mu.Lock() }
func (s *Session) Unlock() { s.mu.Unlock() }

func (e *Engine) NewSession(mode string) *Session {
	id := newSessionID()
	sess := &Session{
		ID:        id,
		Mode:      mode,
		Exclude:   map[int64]bool{},
		Rated:     map[int64]string{},
		UpdatedAt: time.Now(),
	}
	e.sessionsMu.Lock()
	e.sessions[id] = sess
	e.gcLocked()
	e.sessionsMu.Unlock()
	e.persistLocked(sess)
	return sess
}

// newSessionID keeps the readable, time-ordered prefix but adds a random
// suffix: the clock alone repeats under concurrent starts (Windows ticks in
// ~0.5 ms steps), and two clients then shared — and overwrote — one session.
func newSessionID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36) + db.NewID()[:8]
}

func (e *Engine) Get(id string) *Session {
	if id == "" {
		return nil
	}
	e.sessionsMu.RLock()
	sess := e.sessions[id]
	e.sessionsMu.RUnlock()
	if sess != nil {
		return sess
	}
	row, ok, err := e.Store.LoadPlaySession(id)
	if err != nil || !ok {
		return nil
	}
	sess = hydrate(row)
	e.sessionsMu.Lock()
	if existing := e.sessions[id]; existing != nil {
		e.sessionsMu.Unlock()
		return existing
	}
	e.sessions[id] = sess
	e.sessionsMu.Unlock()
	return sess
}

func hydrate(row db.PlaySessionRow) *Session {
	sess := &Session{
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
	if row.CurrentItemJSON != "" {
		_ = json.Unmarshal([]byte(row.CurrentItemJSON), &sess.CurrentItem)
	}
	if row.TasteStateJSON != "" {
		_ = json.Unmarshal([]byte(row.TasteStateJSON), &sess.TasteState)
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
	if row.ActiveContextsJSON != "" {
		_ = json.Unmarshal([]byte(row.ActiveContextsJSON), &sess.ActiveContextIDs)
	}
	sess.TransitionProfile = row.TransitionProfile
	return sess
}

func (e *Engine) persistLocked(sess *Session) {
	if sess == nil {
		return
	}
	id := sess.ID
	mode := sess.Mode
	current := sess.Current
	dailyPos := sess.DailyPos
	playlistName := sess.PlaylistName
	playlistKind := sess.PlaylistKind
	queueItems := append([]queue.Item(nil), sess.Queue...)
	currentItem := sess.CurrentItem
	tasteState := sess.TasteState
	exclude := make([]int64, 0, len(sess.Exclude))
	for id := range sess.Exclude {
		exclude = append(exclude, id)
	}
	rated := make(map[int64]string, len(sess.Rated))
	for trackID, rating := range sess.Rated {
		rated[trackID] = rating
	}
	dailyIDs := append([]int64(nil), sess.DailyIDs...)
	contextIDs := append([]string(nil), sess.ActiveContextIDs...)
	profile := sess.TransitionProfile
	updatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	e.enqueue(func() {
		qj, _ := json.Marshal(queueItems)
		cij, _ := json.Marshal(currentItem)
		tsj, _ := json.Marshal(tasteState)
		ej, _ := json.Marshal(exclude)
		rj, _ := json.Marshal(rated)
		dj, _ := json.Marshal(dailyIDs)
		cj, _ := json.Marshal(contextIDs)
		_ = e.Store.UpsertPlaySession(db.PlaySessionRow{
			ID: id, Mode: mode, CurrentID: current,
			QueueJSON: string(qj), ExcludeJSON: string(ej), RatedJSON: string(rj),
			DailyIDsJSON: string(dj), DailyPos: dailyPos,
			PlaylistName: playlistName, PlaylistKind: playlistKind,
			CurrentItemJSON: string(cij), TasteStateJSON: string(tsj),
			ActiveContextsJSON: string(cj), TransitionProfile: profile,
			UpdatedAt: updatedAt,
		})
	})
}

func (e *Engine) gcLocked() {
	cutoffRAM := time.Now().Add(-6 * time.Hour)
	for id, sess := range e.sessions {
		sess.mu.Lock()
		stale := sess.UpdatedAt.Before(cutoffRAM)
		sess.mu.Unlock()
		if stale {
			_ = e.Store.AbandonSessionImpressions(id)
			delete(e.sessions, id)
		}
	}
	_ = e.Store.DeleteStalePlaySessions(time.Now().Add(-7 * 24 * time.Hour))
	const maxSessions = 64
	if len(e.sessions) <= maxSessions {
		if n, err := e.Store.CountPlaySessions(); err == nil && n > maxSessions {
			ids, _ := e.Store.OldestPlaySessionIDs(n - maxSessions)
			for _, id := range ids {
				_ = e.Store.AbandonSessionImpressions(id)
				_ = e.Store.DeletePlaySession(id)
				delete(e.sessions, id)
			}
		}
		return
	}
	for len(e.sessions) > maxSessions {
		var oldestID string
		var oldestTime time.Time
		first := true
		for id, sess := range e.sessions {
			sess.mu.Lock()
			updatedAt := sess.UpdatedAt
			sess.mu.Unlock()
			if first || updatedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = updatedAt
				first = false
			}
		}
		delete(e.sessions, oldestID)
		_ = e.Store.AbandonSessionImpressions(oldestID)
		_ = e.Store.DeletePlaySession(oldestID)
	}
}
