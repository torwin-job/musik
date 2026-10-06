package db

import (
	"database/sql"
	"errors"
	"time"
)

// PlaybackState is the last known "where am I listening" for the owner: the
// session, track and position a reload or another device resumes from.
type PlaybackState struct {
	SessionID   string  `json:"session_id"`
	TrackID     int64   `json:"track_id"`
	PositionSec float64 `json:"position_sec"`
	ListenedSec float64 `json:"listened_sec"`
	Playing     bool    `json:"playing"`
	ClientID    string  `json:"client_id"`
	UpdatedAt   string  `json:"updated_at"`
}

func (s *Store) LoadPlaybackState() (PlaybackState, bool, error) {
	var st PlaybackState
	var playing int
	err := s.DB.QueryRow(`
SELECT session_id, track_id, position_sec, listened_sec, playing, client_id, updated_at
FROM playback_state WHERE owner_scope = 'local'`).Scan(
		&st.SessionID, &st.TrackID, &st.PositionSec, &st.ListenedSec,
		&playing, &st.ClientID, &st.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return PlaybackState{}, false, nil
	}
	if err != nil {
		return PlaybackState{}, false, err
	}
	st.Playing = playing != 0
	return st, true, nil
}

// SavePlaybackState stamps updated_at and stores st. With claim the writer
// takes over the state (playback started on that device); without it the
// write is a heartbeat and applies only while st.ClientID still owns the row,
// so a device that was taken over cannot overwrite the new owner's position.
// applied=false returns the current (foreign) state.
func (s *Store) SavePlaybackState(st PlaybackState, claim bool) (saved PlaybackState, applied bool, err error) {
	st.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	playing, claimed := 0, 0
	if st.Playing {
		playing = 1
	}
	if claim {
		claimed = 1
	}
	res, err := s.DB.Exec(`
INSERT INTO playback_state(
  owner_scope, session_id, track_id, position_sec, listened_sec, playing, client_id, updated_at
) VALUES ('local',?,?,?,?,?,?,?)
ON CONFLICT(owner_scope) DO UPDATE SET
  session_id = excluded.session_id, track_id = excluded.track_id,
  position_sec = excluded.position_sec, listened_sec = excluded.listened_sec,
  playing = excluded.playing, client_id = excluded.client_id,
  updated_at = excluded.updated_at
WHERE ? = 1 OR playback_state.client_id = excluded.client_id`,
		st.SessionID, st.TrackID, st.PositionSec, st.ListenedSec, playing, st.ClientID, st.UpdatedAt,
		claimed,
	)
	if err != nil {
		return PlaybackState{}, false, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return st, true, nil
	}
	cur, _, err := s.LoadPlaybackState()
	return cur, false, err
}
