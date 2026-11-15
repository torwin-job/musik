package api

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestMultiUserDeviceTokensPreserveLegacyBearerContractAndScope(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db", "musik-test.db")
	databaseURL, dbPath := testdb.Target(t, dbPath)
	store, err := db.OpenDatabase(databaseURL, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	userA, profileA, err := store.CreateUserWithDefaultProfile(ctx, "Device owner")
	if err != nil {
		t.Fatal(err)
	}
	profileA2, err := store.CreateProfile(ctx, userA, "Tablet profile")
	if err != nil {
		t.Fatal(err)
	}
	userB, _, err := store.CreateUserWithDefaultProfile(ctx, "Administrator")
	if err != nil {
		t.Fatal(err)
	}
	for _, userID := range []string{userA, userB} {
		if _, err := store.DB.Exec(`INSERT INTO user_roles(user_id,role) VALUES (?,'admin')`, userID); err != nil {
			t.Fatal(err)
		}
	}
	cookieA, csrfA, err := store.IssueUserSession(ctx, userA, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cookieB, csrfB, err := store.IssueUserSession(ctx, userB, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	audioPath := filepath.Join(dir, "track.mp3")
	if err := os.WriteFile(audioPath, []byte("0123456789abcdefghijklmnopqrstuvwxyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	artPath := filepath.Join(dir, "cover.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	img.Set(0, 0, color.RGBA{R: 200, A: 255})
	artFile, err := os.Create(artPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(artFile, img, nil); err != nil {
		t.Fatal(err)
	}
	if err := artFile.Close(); err != nil {
		t.Fatal(err)
	}
	for _, trackID := range []int64{11, 22} {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id,path,title,artist,album,duration,artwork_path, created_at, updated_at) VALUES (?,?,?,?,?,?,?, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			trackID, audioPath+map[int64]string{11: "", 22: ".copy"}[trackID], "Track", "Artist", "Album", 36, artPath,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := store.DB.Exec(
			`INSERT INTO features(track_id,embedding,embedding_dim,status,cluster_id) VALUES (?,?,2,'ready',0)`,
			trackID, index.Float32Bytes([]float32{1, 0}),
		); err != nil {
			t.Fatal(err)
		}
	}

	cfg := config.Config{
		DBPath: dbPath, DataDir: dir, QueueSize: 6, MultiUser: true,
		PublicBaseURL: "https://musik.test", WorkerURL: "http://127.0.0.1:1",
		WorkerAutostart: false,
	}
	server := New(cfg, store, index.New(cfg), taste.New(), nil)
	server.multi = &multiUserState{profiles: make(map[string]*profileState)}
	handler := server.Handler()
	call := func(method, path, body, cookie, csrf, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "https://musik.test"+path, strings.NewReader(body))
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		}
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if strings.Contains(method, "POST") || method == http.MethodDelete || method == http.MethodPatch {
			req.Header.Set("Origin", "https://musik.test")
			req.Header.Set("Content-Type", "application/json")
		}
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	check := func(rec *httptest.ResponseRecorder, status int) {
		t.Helper()
		if rec.Code != status {
			t.Fatalf("status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
		}
	}

	badCSRF := call(http.MethodPost, "/api/account/device-tokens", `{"name":"Phone"}`, cookieA, "", "")
	check(badCSRF, http.StatusForbidden)
	foreignProfile := call(http.MethodPost, "/api/account/device-tokens", `{"name":"Foreign","profile_id":"foreign"}`, cookieA, csrfA, "")
	check(foreignProfile, http.StatusNotFound)

	created := call(http.MethodPost, "/api/account/device-tokens", `{"name":"Phone"}`, cookieA, csrfA, "")
	check(created, http.StatusOK)
	var phone struct {
		Token  string             `json:"token"`
		QR     string             `json:"qr"`
		Device db.DeviceTokenInfo `json:"device"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &phone); err != nil || phone.Token == "" {
		t.Fatalf("created token response=%s err=%v", created.Body.String(), err)
	}
	if !strings.HasPrefix(phone.QR, "<svg") {
		t.Fatalf("created token has no connect QR: %.80q", phone.QR)
	}
	if phone.Device.ProfileID != profileA.ID || phone.Device.ExpiresAt == "" {
		t.Fatalf("created device metadata=%+v", phone.Device)
	}
	tabletCreated := call(http.MethodPost, "/api/account/device-tokens", `{"name":"Tablet","profile_id":"`+profileA2.ID+`"}`, cookieA, csrfA, "")
	check(tabletCreated, http.StatusOK)
	var tablet struct {
		Token  string             `json:"token"`
		Device db.DeviceTokenInfo `json:"device"`
	}
	if err := json.Unmarshal(tabletCreated.Body.Bytes(), &tablet); err != nil || tablet.Token == "" {
		t.Fatalf("tablet response=%s err=%v", tabletCreated.Body.String(), err)
	}

	listed := call(http.MethodGet, "/api/account/device-tokens", "", cookieA, "", "")
	check(listed, http.StatusOK)
	if strings.Contains(listed.Body.String(), phone.Token) || strings.Contains(listed.Body.String(), tablet.Token) || strings.Contains(listed.Body.String(), "secret_hash") {
		t.Fatalf("device token list exposed a secret: %s", listed.Body.String())
	}
	var tokenList struct {
		Tokens []db.DeviceTokenInfo `json:"tokens"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &tokenList); err != nil || len(tokenList.Tokens) != 2 {
		t.Fatalf("device list=%s err=%v", listed.Body.String(), err)
	}

	me := call(http.MethodGet, "/api/auth/me", "", "", "", phone.Token)
	check(me, http.StatusOK)
	var meBody map[string]any
	if err := json.Unmarshal(me.Body.Bytes(), &meBody); err != nil {
		t.Fatal(err)
	}
	if len(meBody) != 2 || meBody["ok"] != true || meBody["auth_enabled"] != true {
		t.Fatalf("legacy auth/me response changed: %#v", meBody)
	}

	stream := httptest.NewRequest(http.MethodGet, "https://musik.test/api/stream/11", nil)
	stream.Header.Set("Authorization", "Bearer "+phone.Token)
	stream.Header.Set("Range", "bytes=2-5")
	streamOut := httptest.NewRecorder()
	handler.ServeHTTP(streamOut, stream)
	if streamOut.Code != http.StatusPartialContent || streamOut.Body.String() != "2345" {
		t.Fatalf("Bearer stream status=%d range=%q body=%q", streamOut.Code, streamOut.Header().Get("Content-Range"), streamOut.Body.String())
	}
	artwork := httptest.NewRequest(http.MethodGet, "https://musik.test/api/artwork/11?w=96", nil)
	artwork.Header.Set("Authorization", "Bearer "+phone.Token)
	artworkOut := httptest.NewRecorder()
	handler.ServeHTTP(artworkOut, artwork)
	if artworkOut.Code != http.StatusOK || artworkOut.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("Bearer artwork status=%d content-type=%q body=%s", artworkOut.Code, artworkOut.Header().Get("Content-Type"), artworkOut.Body.String())
	}
	started := call(http.MethodPost, "/api/session/start", "{}", "", "", phone.Token)
	check(started, http.StatusOK)
	var playSession struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &playSession); err != nil || playSession.SessionID == "" {
		t.Fatalf("session start=%s err=%v", started.Body.String(), err)
	}
	crossProfileSession := call(http.MethodGet, "/api/now?session_id="+playSession.SessionID, "", "", "", tablet.Token)
	check(crossProfileSession, http.StatusNotFound)
	legacyEvent := call(http.MethodPost, "/api/events", `{"type":"like","track_id":11,"session_id":"`+playSession.SessionID+`"}`, "", "", phone.Token)
	check(legacyEvent, http.StatusOK)
	lateForeignEvent := call(http.MethodPost, "/api/events", `{"type":"like","track_id":11,"session_id":"`+playSession.SessionID+`"}`, "", "", tablet.Token)
	check(lateForeignEvent, http.StatusNotFound)
	var phoneLikes, tabletLikes int
	if err := store.ForProfile(profileA.ID).DB.QueryRow(`SELECT likes FROM track_stats WHERE track_id=?`, 11).Scan(&phoneLikes); err != nil {
		t.Fatal(err)
	}
	if err := store.ForProfile(profileA2.ID).DB.QueryRow(`SELECT COALESCE((SELECT likes FROM track_stats WHERE track_id=? AND profile_id=?),0)`, 11, profileA2.ID).Scan(&tabletLikes); err != nil {
		t.Fatal(err)
	}
	if phoneLikes != 1 || tabletLikes != 0 {
		t.Fatalf("legacy event crossed profiles: phone likes=%d tablet likes=%d", phoneLikes, tabletLikes)
	}

	// Switching the browser profile must not move either device token.
	switched := call(http.MethodPost, "/api/profiles/"+profileA2.ID+"/activate", "{}", cookieA, csrfA, "")
	check(switched, http.StatusOK)
	addedPhone := call(http.MethodPost, "/api/favorites", `{"track_id":11}`, "", "", phone.Token)
	check(addedPhone, http.StatusOK)
	addedTablet := call(http.MethodPost, "/api/favorites", `{"track_id":22}`, "", "", tablet.Token)
	check(addedTablet, http.StatusOK)
	if !store.ForProfile(profileA.ID).FavoritesHas(11) || store.ForProfile(profileA2.ID).FavoritesHas(11) {
		t.Fatal("phone favorite followed the browser profile instead of the token profile")
	}
	if !store.ForProfile(profileA2.ID).FavoritesHas(22) || store.ForProfile(profileA.ID).FavoritesHas(22) {
		t.Fatal("tablet favorite was not isolated to its bound profile")
	}

	mix := call(http.MethodPost, "/api/jobs/mix_pack", "{}", "", "", phone.Token)
	check(mix, http.StatusOK)
	var mixJob struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(mix.Body.Bytes(), &mixJob); err != nil || mixJob.ID == 0 {
		t.Fatalf("mix job response=%s err=%v", mix.Body.String(), err)
	}
	tabletMix := call(http.MethodPost, "/api/jobs/mix_pack", "{}", "", "", tablet.Token)
	check(tabletMix, http.StatusOK)
	var tabletMixJob struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(tabletMix.Body.Bytes(), &tabletMixJob); err != nil || tabletMixJob.ID == 0 {
		t.Fatalf("tablet mix response=%s err=%v", tabletMix.Body.String(), err)
	}
	job := call(http.MethodGet, "/api/jobs/"+strconv.FormatInt(mixJob.ID, 10), "", "", "", phone.Token)
	check(job, http.StatusOK)
	foreignJob := call(http.MethodGet, "/api/jobs/"+strconv.FormatInt(tabletMixJob.ID, 10), "", "", "", phone.Token)
	check(foreignJob, http.StatusNotFound)
	check(call(http.MethodGet, "/api/jobs", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/jobs/full_rescan", "{}", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodGet, "/api/plugins", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/plugins/example/import", `{"url":"https://example.invalid/track"}`, "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/plugins/example/search", `{"query":"example"}`, "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodGet, "/api/plugin-jobs", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/plugin-jobs/1/cancel", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodGet, "/api/admin/users", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodGet, "/api/settings", "", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/settings/restart", "{}", "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/account/device-tokens", `{"name":"Blocked"}`, "", "", phone.Token), http.StatusForbidden)
	check(call(http.MethodPost, "/api/auth/logout", "{}", "", "", phone.Token), http.StatusOK)
	check(call(http.MethodGet, "/api/favorites", "", "", "", phone.Token), http.StatusOK)

	conflict := call(http.MethodGet, "/api/favorites", "", cookieA, "", phone.Token)
	check(conflict, http.StatusBadRequest)

	replaced := call(http.MethodPost, "/api/account/device-tokens/"+phone.Device.ID+"/replace", "{}", cookieA, csrfA, "")
	check(replaced, http.StatusOK)
	var phoneReplacement struct {
		Token  string             `json:"token"`
		Device db.DeviceTokenInfo `json:"device"`
	}
	if err := json.Unmarshal(replaced.Body.Bytes(), &phoneReplacement); err != nil || phoneReplacement.Token == "" {
		t.Fatalf("replace response=%s err=%v", replaced.Body.String(), err)
	}
	check(call(http.MethodGet, "/api/favorites", "", "", "", phone.Token), http.StatusUnauthorized)
	check(call(http.MethodGet, "/api/favorites", "", "", "", phoneReplacement.Token), http.StatusOK)

	if _, err := store.DB.Exec(`UPDATE device_tokens SET expires_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), tablet.Device.ID); err != nil {
		t.Fatal(err)
	}
	check(call(http.MethodGet, "/api/favorites", "", "", "", tablet.Token), http.StatusUnauthorized)
	deletedProfile := call(http.MethodDelete, "/api/profiles/"+profileA2.ID, "", cookieA, csrfA, "")
	check(deletedProfile, http.StatusOK)
	check(call(http.MethodGet, "/api/favorites", "", "", "", phoneReplacement.Token), http.StatusOK)
	check(call(http.MethodPost, "/api/events", `{"type":"like","track_id":22,"session_id":"`+playSession.SessionID+`"}`, "", "", tablet.Token), http.StatusUnauthorized)

	disabled := call(http.MethodPatch, "/api/admin/users/"+userA, `{"status":"disabled"}`, cookieB, csrfB, "")
	check(disabled, http.StatusOK)
	check(call(http.MethodGet, "/api/favorites", "", "", "", phoneReplacement.Token), http.StatusUnauthorized)
	invalidMe := call(http.MethodGet, "/api/auth/me", "", "", "", phoneReplacement.Token)
	check(invalidMe, http.StatusOK)
	var invalidBody map[string]any
	if err := json.Unmarshal(invalidMe.Body.Bytes(), &invalidBody); err != nil || invalidBody["ok"] != false || invalidBody["auth_enabled"] != true || len(invalidBody) != 2 {
		t.Fatalf("invalid bearer auth/me=%s err=%v", invalidMe.Body.String(), err)
	}

}
