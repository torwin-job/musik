package playback

import (
	"database/sql"
	"errors"
	"time"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/taste"
)

type Event struct {
	Type         string   `json:"type"`
	EventID      string   `json:"event_id"`
	ImpressionID string   `json:"impression_id"`
	ClientID     string   `json:"client_id"`
	DeviceID     string   `json:"device_id"`
	TrackID      int64    `json:"track_id"`
	SessionID    string   `json:"session_id"`
	PositionSec  *float64 `json:"position_sec"`
	DurationSec  *float64 `json:"duration_sec"`
	ListenedSec  *float64 `json:"listened_sec"`
	Reason       string   `json:"reason"`
}

type EventResult struct {
	OK           bool
	Unknown      bool
	Ignored      bool
	IgnoreReason string
	Rating       string
	Flipped      bool
	SignedWeight float64
	NextID       int64
	Ended        bool
	IsEnd        bool
	IsRate       bool
	Error        string
	Conflict     bool
}

func (e *Engine) ApplyEvent(sess *Session, ev Event) EventResult {
	ev.SessionID = sess.ID
	if sess.Mode == "share" {
		if ev.Type == "track_end" || ev.Type == "skip" {
			nextID := e.Advance(sess)
			return EventResult{OK: true, IsEnd: true, NextID: nextID, Ended: nextID == 0}
		}
		return EventResult{OK: true, Ignored: true, IgnoreReason: "public_share_not_owner_feedback"}
	}
	if ev.EventID == "" {
		ev.EventID = db.NewID()
	}
	ref, err := e.Store.ResolveImpression(ev.ImpressionID, sess.ID, ev.TrackID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		if errors.Is(err, db.ErrAmbiguousImpression) {
			return EventResult{Error: err.Error(), Conflict: true}
		}
		return EventResult{Error: err.Error()}
	}
	if err == nil {
		ev.ImpressionID = ref.ImpressionID
	}
	source := "manual"
	requestID := ""
	played := false
	if err == nil {
		source, requestID = ref.Source, ref.RequestID
		played = ref.Played
	}
	apply := func(outcome string, ratio float64) (db.LifecycleResult, error) {
		return e.Store.ApplyLifecycleEvent(db.LifecycleEvent{
			EventID: ev.EventID, Type: ev.Type, TrackID: ev.TrackID,
			SessionID: sess.ID, ImpressionID: ev.ImpressionID,
			RequestID: requestID, Source: source, ClientID: ev.ClientID,
			DeviceID: ev.DeviceID, Reason: ev.Reason,
			PositionSec: ev.PositionSec, DurationSec: ev.DurationSec,
			ListenedSec: ev.ListenedSec, ListenedRatio: ratio, Outcome: outcome,
		})
	}
	switch ev.Type {
	case "track_start":
		lifecycle, err := apply("", 0)
		if err != nil {
			return EventResult{Error: err.Error()}
		}
		if !lifecycle.Inserted {
			return EventResult{OK: true, Ignored: true, IgnoreReason: "duplicate_event"}
		}
		if sess.Current != 0 && sess.Current != ev.TrackID {
			sess.Prev = sess.Current
		}
		sess.Current = ev.TrackID
		e.ExcludeTrack(sess, ev.TrackID)
		if lifecycle.LifecycleChanged {
			e.Idx.BumpShownLocal(ev.TrackID)
			_ = e.Store.BumpRecStats(ev.TrackID, 1, 0, 0)
		}
		if sess.Prev != 0 && sess.Prev != ev.TrackID {
			_ = e.Store.BumpTransition(sess.Prev, ev.TrackID, 1.0)
			e.BumpTransitionMem(sess.Prev, ev.TrackID, 1.0)
		}
		if len(sess.DailyIDs) == 0 && len(sess.Queue) < e.Cfg.QueueSize/2 {
			e.RefreshQueue(sess, ev.TrackID, "refill")
		}
		return EventResult{OK: true}

	case "progress":
		if sess.LastProgressWrite.IsZero() ||
			time.Since(sess.LastProgressWrite) >= 45*time.Second {
			if _, err := apply("", 0); err != nil {
				return EventResult{Error: err.Error()}
			}
			sess.LastProgressWrite = time.Now()
		}
		if len(sess.DailyIDs) == 0 &&
			sess.Current != 0 &&
			len(sess.Queue) < e.Cfg.QueueSize/2 &&
			time.Since(sess.LastQueueAt) >= 15*time.Second {
			e.RefreshQueue(sess, sess.Current, "refill")
		}
		return EventResult{OK: true}

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
		} else if row, ok := e.Idx.RowOf(ev.TrackID); ok {
			dur = e.Idx.MetaAt(row).Duration
		}
		ratio := 0.0
		if dur > 0 {
			ratio = listened / dur
		}
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		outcome := "partial"
		if reason == "completed" || ratio >= 0.8 {
			outcome = "finished"
		} else if (ev.Type == "skip" || reason == "skipped") && ratio < 0.3 {
			outcome = "early_skip"
		}
		lifecycle, err := apply(outcome, ratio)
		if err != nil {
			return EventResult{Error: err.Error()}
		}
		if !lifecycle.Inserted {
			return EventResult{
				OK: true, IsEnd: true, Ignored: true, IgnoreReason: "duplicate_or_closed_event",
				NextID: sess.Current,
			}
		}
		if !lifecycle.LifecycleChanged {
			if lifecycle.AlreadyClosed {
				return EventResult{
					OK: true, IsEnd: true, Ignored: true, IgnoreReason: "already_closed",
					NextID: sess.Current,
				}
			}
			nextID := e.Advance(sess)
			return EventResult{
				OK: true, IsEnd: true, Ignored: true, IgnoreReason: "already_closed",
				NextID: nextID, Ended: nextID == 0,
			}
		}
		signed, action := taste.WeightFromListen(listened, dur, reason)
		var finishedVec []float32
		if row, ok := e.Idx.RowOf(ev.TrackID); ok {
			vector := e.Idx.Vector(row)
			if outcome == "finished" {
				if signed <= 0 {
					signed = 1
				}
				// Update in-memory taste before Advance so a refill ranks with
				// the new profile; persist to DB after Advance so stream warm
				// for the next track starts before the slower profile writes.
				e.TasteState.UpdatePositive(vector, signed, e.Cfg.TasteAlpha,
					db.DayPart(time.Now().Hour()), &sess.TasteState)
				finishedVec = vector
			} else if outcome == "early_skip" {
				e.TasteState.AddEarlySkip(&sess.TasteState, vector, 1-ratio)
				signed = 0
			} else {
				signed = 0
			}
			_ = action
		}
		if outcome == "early_skip" {
			_ = e.Store.BumpRecStats(ev.TrackID, 0, 1, 0)
			e.Idx.BumpSkipEarlyLocal(ev.TrackID)
			e.noteClusterSkip(sess, ev.TrackID)
		}
		if outcome == "finished" {
			_ = e.Store.BumpRecStats(ev.TrackID, 0, 0, 1)
			e.Idx.BumpCompletedLocal(ev.TrackID)
		}
		if sess.Prev != 0 && sess.Prev != ev.TrackID && sess.Mode != "share" {
			provenance := "radio"
			if sess.CurrentItem.Source == "manual" {
				provenance = "manual"
			}
			_ = e.Store.RecordOutcomeTransition(sess.Prev, ev.TrackID, outcome, provenance)
		}
		e.ObserveExploreOutcome(source, outcome, true)
		e.ExcludeTrack(sess, ev.TrackID)
		nextID := e.Advance(sess)
		if len(finishedVec) > 0 {
			e.PersistTaste(finishedVec, signed, e.Cfg.TasteAlpha)
			e.learnActiveContexts(sess, finishedVec, 1)
		}
		return EventResult{
			OK: true, IsEnd: true, SignedWeight: signed,
			NextID: nextID, Ended: nextID == 0,
		}

	case "like", "dislike":
		if sess.Rated == nil {
			sess.Rated = map[int64]string{}
		}
		prev := sess.Rated[ev.TrackID]
		if prev == ev.Type {
			return EventResult{
				OK: true, IsRate: true, Ignored: true,
				IgnoreReason: "already_" + ev.Type, Rating: prev,
			}
		}
		lifecycle, err := apply("", 0)
		if err != nil {
			return EventResult{Error: err.Error()}
		}
		if !lifecycle.Inserted {
			return EventResult{
				OK: true, IsRate: true, Ignored: true,
				IgnoreReason: "duplicate_event", Rating: prev,
			}
		}
		if row, ok := e.Idx.RowOf(ev.TrackID); ok {
			vec := e.Idx.Vector(row)
			if ev.Type == "like" {
				e.TasteState.UpdatePositive(vec, taste.LikeWeight(), e.Cfg.TasteAlpha,
					db.DayPart(time.Now().Hour()), &sess.TasteState)
				e.PersistTaste(vec, taste.LikeWeight(), e.Cfg.TasteAlpha)
			} else {
				e.TasteState.AddDislike(vec, -taste.DislikeWeight())
				e.persistTasteState(db.DayPart(time.Now().Hour()))
			}
		}
		sess.Rated[ev.TrackID] = ev.Type
		e.ObserveExploreOutcome(source, ev.Type, played)
		if len(sess.DailyIDs) == 0 {
			e.RefreshQueue(sess, sess.Current, ev.Type)
		} else {
			sess.UpdatedAt = time.Now()
			e.persistLocked(sess)
		}
		return EventResult{
			OK: true, IsRate: true, Rating: ev.Type, Flipped: prev != "",
		}

	default:
		return EventResult{Unknown: true}
	}
}
