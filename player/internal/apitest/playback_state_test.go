package apitest

import (
	"encoding/json"
	"testing"
)

func TestPlaybackStateRoundTrip(t *testing.T) {
	server := openTestServer(t)

	rec := serve(server, jsonReq("GET", "/api/playback/state", ""))
	if rec.Code != 200 || rec.Body.String() == "" {
		t.Fatalf("empty state status=%d body=%s", rec.Code, rec.Body.String())
	}
	var empty struct {
		State *struct{} `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil || empty.State != nil {
		t.Fatalf("want null state, got %s (%v)", rec.Body.String(), err)
	}

	rec = serve(server, jsonReq("PUT", "/api/playback/state", `{"track_id":0}`))
	if rec.Code != 400 {
		t.Fatalf("missing track status=%d", rec.Code)
	}

	put := func(body string, wantOK bool) {
		t.Helper()
		rec := serve(server, jsonReq("PUT", "/api/playback/state", body))
		var out struct {
			OK bool `json:"ok"`
		}
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.OK != wantOK {
			t.Fatalf("put %s: status=%d body=%s, want ok=%v", body, rec.Code, rec.Body.String(), wantOK)
		}
	}
	// First writer creates the row even without a claim.
	put(`{"session_id":"s1","track_id":11,"position_sec":42.5,"listened_sec":40,"playing":true,"client_id":"pc"}`, true)
	// A heartbeat from a device that does not own the state is not applied.
	put(`{"session_id":"s1","track_id":12,"position_sec":1,"playing":true,"client_id":"phone"}`, false)
	// Pressing play on the phone claims the state...
	put(`{"session_id":"s1","track_id":11,"position_sec":45,"listened_sec":43,"playing":true,"client_id":"phone","claim":true}`, true)
	// ...so the PC's late heartbeat can no longer overwrite it.
	put(`{"session_id":"s1","track_id":11,"position_sec":60,"listened_sec":58,"playing":true,"client_id":"pc"}`, false)
	put(`{"session_id":"s1","track_id":11,"position_sec":50,"listened_sec":47,"playing":false,"client_id":"phone"}`, true)

	rec = serve(server, jsonReq("GET", "/api/playback/state", ""))
	var got struct {
		State struct {
			SessionID   string  `json:"session_id"`
			TrackID     int64   `json:"track_id"`
			PositionSec float64 `json:"position_sec"`
			ListenedSec float64 `json:"listened_sec"`
			Playing     bool    `json:"playing"`
			ClientID    string  `json:"client_id"`
			UpdatedAt   string  `json:"updated_at"`
		} `json:"state"`
		Track map[string]any `json:"track"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	s := got.State
	if s.SessionID != "s1" || s.TrackID != 11 || s.PositionSec != 50 || s.ListenedSec != 47 ||
		s.Playing || s.ClientID != "phone" || s.UpdatedAt == "" {
		t.Fatalf("state=%+v", s)
	}
	if got.Track == nil || got.Track["id"].(float64) != 11 {
		t.Fatalf("track=%v", got.Track)
	}
}
