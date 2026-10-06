package playback

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	path := filepath.Join(t.TempDir(), "playback-test.db")
	if err := testdb.Create(path); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{QueueSize: 6, ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0.15}
	idx := index.New(cfg)
	rows := []db.TrackRow{
		{ID: 11, Title: "One", Artist: "Artist", Album: "Album", Embedding: index.Float32Bytes([]float32{1, 11}), Dim: 2},
		{ID: 22, Title: "Two", Artist: "Artist", Album: "Album", Embedding: index.Float32Bytes([]float32{1, 22}), Dim: 2},
		{ID: 33, Title: "Three", Artist: "Artist", Album: "Album", Embedding: index.Float32Bytes([]float32{1, 33}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return New(cfg, store, idx, taste.New(), queue.NewBuilder(idx, cfg))
}

func TestStartFixedPreservesOrderAndStartPosition(t *testing.T) {
	tests := []struct {
		name         string
		startIndex   int
		startTrackID int64
		wantIndex    int
		wantCurrent  int64
	}{
		{name: "index", startIndex: 2, wantIndex: 2, wantCurrent: 33},
		{name: "track id overrides index", startIndex: 0, startTrackID: 22, wantIndex: 1, wantCurrent: 22},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			engine := testEngine(t)
			order := []int64{11, 22, 33}
			session := engine.StartFixed(
				append([]int64(nil), order...), "playlist", "Ordered", "daily",
				test.startIndex, test.startTrackID,
			)
			if !reflect.DeepEqual(session.DailyIDs, order) {
				t.Fatalf("session order = %v, want %v", session.DailyIDs, order)
			}
			if session.DailyPos != test.wantIndex || session.Current != test.wantCurrent {
				t.Fatalf("position/current = %d/%d, want %d/%d",
					session.DailyPos, session.Current, test.wantIndex, test.wantCurrent)
			}

			row, ok, err := engine.Store.LoadPlaySession(session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("fixed session was not persisted")
			}
			var persistedOrder []int64
			if err := json.Unmarshal([]byte(row.DailyIDsJSON), &persistedOrder); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(persistedOrder, order) {
				t.Fatalf("persisted order = %v, want %v", persistedOrder, order)
			}
			if row.DailyPos != test.wantIndex || row.CurrentID != test.wantCurrent {
				t.Fatalf("persisted position/current = %d/%d, want %d/%d",
					row.DailyPos, row.CurrentID, test.wantIndex, test.wantCurrent)
			}
		})
	}
}

func TestJumpPrefersTrackIDAndRadioQueue(t *testing.T) {
	engine := testEngine(t)
	session := engine.StartFixed([]int64{11, 22, 33}, "playlist", "Ordered", "daily", 0, 0)
	wrong := 0
	if err := engine.Jump(session, &wrong, 33); err != nil {
		t.Fatal(err)
	}
	if session.Current != 33 || session.DailyPos != 2 {
		t.Fatalf("playlist jump current/pos=%d/%d", session.Current, session.DailyPos)
	}

	radio := engine.StartRadio(nil)
	radio.Lock()
	radio.Current = 11
	radio.Queue = []queue.Item{
		{TrackID: 22, Title: "Two"},
		{TrackID: 33, Title: "Three"},
	}
	if err := engine.Jump(radio, nil, 33); err != nil {
		t.Fatal(err)
	}
	if radio.Current != 33 {
		t.Fatalf("radio jump current=%d", radio.Current)
	}
	for _, item := range radio.Queue {
		if item.TrackID == 33 {
			t.Fatal("jumped radio track stayed in queue")
		}
	}
	radio.Unlock()
}

func TestBackRestoresRadioTrackAndFixedPosition(t *testing.T) {
	engine := testEngine(t)
	radio := engine.NewSession("radio")
	radio.Lock()
	radio.Current = 11
	radio.CurrentItem = queue.Item{TrackID: 11, Title: "One"}
	radio.Queue = []queue.Item{
		{TrackID: 22, Title: "Two"},
		{TrackID: 33, Title: "Three"},
		{TrackID: 11, Title: "pad"},
		{TrackID: 22, Title: "pad2"},
	}
	if next := engine.Advance(radio); next != 22 {
		t.Fatalf("advance=%d", next)
	}
	if err := engine.Back(radio); err != nil {
		t.Fatal(err)
	}
	if radio.Current != 11 {
		t.Fatalf("current=%d", radio.Current)
	}
	if len(radio.Queue) == 0 || radio.Queue[0].TrackID != 22 {
		t.Fatalf("queue=%+v", radio.Queue)
	}
	if err := engine.Back(radio); err == nil {
		t.Fatal("second back should fail")
	}
	radio.Unlock()

	fixed := engine.StartFixed([]int64{11, 22, 33}, "playlist", "Ordered", "daily", 0, 0)
	fixed.Lock()
	defer fixed.Unlock()
	if next := engine.Advance(fixed); next != 22 {
		t.Fatalf("fixed advance=%d", next)
	}
	if err := engine.Back(fixed); err != nil {
		t.Fatal(err)
	}
	if fixed.Current != 11 || fixed.DailyPos != 0 {
		t.Fatalf("fixed current/pos=%d/%d", fixed.Current, fixed.DailyPos)
	}
}

func TestResolvePlayIDsByArtist(t *testing.T) {
	engine := testEngine(t)
	ids, name, err := engine.ResolvePlayIDs(PlaySpec{Artist: "Artist"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "Artist" || !reflect.DeepEqual(ids, []int64{11, 22, 33}) {
		t.Fatalf("ids=%v name=%q", ids, name)
	}
}

func TestStartShareInitializesSession(t *testing.T) {
	engine := testEngine(t)

	session := engine.StartShare()
	current := engine.ShareCurrentTrackID(session)

	if session.Mode != "share" {
		t.Fatalf("mode = %q, want share", session.Mode)
	}
	if current == 0 {
		t.Fatal("current track was not selected")
	}
	if !session.Exclude[current] {
		t.Fatalf("current track %d was not excluded", current)
	}
	if len(session.Queue) == 0 {
		t.Fatal("share queue was not initialized")
	}
}

func TestAdvanceShareRestartsAfterAdvanceEnds(t *testing.T) {
	engine := testEngine(t)
	session := engine.NewSession("listen")
	session.Lock()
	session.Current = 11
	session.DailyIDs = []int64{11}
	session.DailyPos = 0
	session.Unlock()

	next := engine.AdvanceShare(session)

	if next == 0 {
		t.Fatal("share fallback did not select a track")
	}
	if session.Mode != "share" {
		t.Fatalf("mode = %q, want share", session.Mode)
	}
	if session.Current != next {
		t.Fatalf("current = %d, want %d", session.Current, next)
	}
	if !session.Exclude[next] {
		t.Fatalf("fallback track %d was not excluded", next)
	}
}

func TestSessionPersistsImpressionAndTasteState(t *testing.T) {
	engine := testEngine(t)
	session := engine.NewSession("radio")
	session.Lock()
	session.Current = 11
	session.CurrentItem = queue.Item{
		TrackID: 11, ImpressionID: "imp", RequestID: "req", Source: "exploit",
	}
	session.TasteState.Positive = taste.VectorState{
		Vector: []float32{1, 0}, Samples: 2,
	}
	session.TasteState.Negative = []taste.NegativePrototype{{
		Vector: []float32{0, 1}, Mass: 1, Samples: 1,
	}}
	engine.persistLocked(session)
	session.Unlock()

	row, ok, err := engine.Store.LoadPlaySession(session.ID)
	if err != nil || !ok {
		t.Fatalf("load row ok=%v err=%v", ok, err)
	}
	restored := hydrate(row)
	if restored.CurrentItem.ImpressionID != "imp" ||
		restored.CurrentItem.RequestID != "req" {
		t.Fatalf("restored current item=%+v", restored.CurrentItem)
	}
	if restored.TasteState.Positive.Samples != 2 ||
		len(restored.TasteState.Negative) != 1 {
		t.Fatalf("restored taste=%+v", restored.TasteState)
	}
}

func insertPlaybackTracks(t *testing.T, engine *Engine) {
	t.Helper()
	for _, id := range []int64{11, 22, 33} {
		if _, err := engine.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, duration) VALUES (?,?,?,?,?)`,
			id, "/t.flac", "T", "Artist", 180,
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRadioStartAssignsImpressionAndIdempotentLifecycle(t *testing.T) {
	engine := testEngine(t)
	insertPlaybackTracks(t, engine)
	session := engine.StartRadio(nil)
	session.Lock()
	defer session.Unlock()
	if session.CurrentItem.ImpressionID == "" || session.CurrentItem.Source == "" {
		t.Fatalf("start track missing impression: %+v", session.CurrentItem)
	}
	if session.CurrentItem.Source != "radio_start" {
		t.Fatalf("source=%q, want radio_start", session.CurrentItem.Source)
	}
	for _, item := range session.Queue {
		if item.ImpressionID == "" || item.RequestID == "" {
			t.Fatalf("queue item missing identity: %+v", item)
		}
	}

	start := Event{
		Type: "track_start", EventID: "start-1", TrackID: session.Current,
		ImpressionID: session.CurrentItem.ImpressionID,
	}
	if result := engine.ApplyEvent(session, start); !result.OK || result.Ignored {
		t.Fatalf("start=%+v", result)
	}
	retry := engine.ApplyEvent(session, start)
	if !retry.OK || !retry.Ignored {
		t.Fatalf("retry start should be ignored: %+v", retry)
	}
	var plays, shown int
	if err := engine.Store.DB.QueryRow(
		`SELECT plays FROM track_stats WHERE track_id=?`, session.Current,
	).Scan(&plays); err != nil || plays != 1 {
		t.Fatalf("plays=%d err=%v", plays, err)
	}
	if err := engine.Store.DB.QueryRow(
		`SELECT shown FROM rec_stats WHERE track_id=?`, session.Current,
	).Scan(&shown); err != nil || shown != 1 {
		t.Fatalf("shown=%d err=%v", shown, err)
	}

	extraID, extraReq := db.NewID(), db.NewID()
	if err := engine.Store.CreateRecommendationRequest(db.RecommendationRequest{
		RequestID: extraReq, SessionID: session.ID, Reason: "stale",
		PolicyVersion: "test", CandidateCount: 1,
	}, []db.RecommendationImpression{{
		ImpressionID: extraID, SessionID: session.ID, TrackID: 33, Source: "exploit",
	}}, nil); err != nil {
		t.Fatal(err)
	}
	session.Exclude[33] = true
	session.Queue = append(session.Queue, queue.Item{
		TrackID: 33, ImpressionID: extraID, RequestID: extraReq, Source: "exploit",
	})
	engine.RefreshQueue(session, session.Current, "like")
	var superseded string
	if err := engine.Store.DB.QueryRow(
		`SELECT outcome FROM recommendation_impressions WHERE impression_id=?`, extraID,
	).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != "superseded" {
		t.Fatalf("displaced impression outcome=%q, want superseded", superseded)
	}

	dur, listened := 180.0, 170.0
	end := Event{
		Type: "track_end", EventID: "end-1", TrackID: session.Current,
		ImpressionID: session.CurrentItem.ImpressionID,
		DurationSec: &dur, ListenedSec: &listened, Reason: "completed",
	}
	if result := engine.ApplyEvent(session, end); !result.OK || result.Ignored {
		t.Fatalf("end=%+v", result)
	}
	if result := engine.ApplyEvent(session, end); !result.Ignored {
		t.Fatalf("retry end should be ignored: %+v", result)
	}
	centroids, err := engine.Store.LoadTasteCentroids(taste.CentroidAlgorithmVersion)
	if err != nil || len(centroids) != 1 {
		t.Fatalf("centroids=%d err=%v, want 1 rebuilt spherical centroid", len(centroids), err)
	}
}

func TestManualSeedIsNotAlgorithmicSource(t *testing.T) {
	engine := testEngine(t)
	insertPlaybackTracks(t, engine)
	seed := int64(11)
	session := engine.StartRadio(&seed)
	session.Lock()
	defer session.Unlock()
	if session.CurrentItem.Source != "manual" {
		t.Fatalf("seed source=%q, want manual", session.CurrentItem.Source)
	}
}

func TestSharePlaybackDoesNotWriteOwnerData(t *testing.T) {
	engine := testEngine(t)
	insertPlaybackTracks(t, engine)
	session := engine.StartShare()
	session.Lock()
	defer session.Unlock()
	before := append([]float32(nil), engine.Taste.Get()...)
	result := engine.ApplyEvent(session, Event{
		Type: "track_start", EventID: "share-start", TrackID: session.Current,
	})
	if !result.OK || !result.Ignored {
		t.Fatalf("share start=%+v", result)
	}
	dur, listened := 180.0, 20.0
	end := engine.ApplyEvent(session, Event{
		Type: "skip", EventID: "share-skip", TrackID: session.Current,
		DurationSec: &dur, ListenedSec: &listened, Reason: "skipped",
	})
	if !end.OK {
		t.Fatalf("share skip=%+v", end)
	}
	var history, impressions, stats int
	if err := engine.Store.DB.QueryRow(`SELECT COUNT(*) FROM listening_history`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.DB.QueryRow(`SELECT COUNT(*) FROM recommendation_impressions`).Scan(&impressions); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.DB.QueryRow(`SELECT COUNT(*) FROM track_stats`).Scan(&stats); err != nil {
		t.Fatal(err)
	}
	if history != 0 || impressions != 0 || stats != 0 {
		t.Fatalf("share wrote owner data history=%d impressions=%d stats=%d",
			history, impressions, stats)
	}
	after := engine.Taste.Get()
	if len(before) != len(after) {
		t.Fatal("share changed taste vector length")
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("share changed taste: %v -> %v", before, after)
		}
	}
}

func TestDislikeAndEarlySkipStayOffPositiveTaste(t *testing.T) {
	engine := testEngine(t)
	insertPlaybackTracks(t, engine)
	session := engine.StartRadio(nil)
	session.Lock()
	defer session.Unlock()
	engine.Taste.UpdateEMA([]float32{1, 0}, 1, 0.5)
	before := append([]float32(nil), engine.Taste.Get()...)
	engine.ApplyEvent(session, Event{
		Type: "track_start", EventID: "s", TrackID: session.Current,
		ImpressionID: session.CurrentItem.ImpressionID,
	})
	engine.ApplyEvent(session, Event{
		Type: "dislike", EventID: "d", TrackID: session.Current,
		ImpressionID: session.CurrentItem.ImpressionID,
	})
	after := engine.Taste.Get()
	if after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("dislike mutated long taste: %v -> %v", before, after)
	}
	if len(engine.TasteState.PersistentNegatives()) == 0 {
		t.Fatal("dislike should create a persistent negative prototype")
	}

	dur, listened := 180.0, 20.0
	engine.ApplyEvent(session, Event{
		Type: "skip", EventID: "sk", TrackID: session.Current,
		ImpressionID: session.CurrentItem.ImpressionID,
		DurationSec: &dur, ListenedSec: &listened, Reason: "skipped",
	})
	if len(session.TasteState.Negative) == 0 {
		t.Fatal("early skip should stay on session negatives")
	}
}

func TestNewSessionIDUniqueWithinOneClockTick(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := newSessionID()
		if seen[id] {
			t.Fatalf("duplicate session id %q after %d ids", id, i)
		}
		seen[id] = true
	}
}
