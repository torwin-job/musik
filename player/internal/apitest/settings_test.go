package apitest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsSaveToEnvAndListDirs(t *testing.T) {
	server := openTestServer(t)
	envPath := filepath.Join(server.Cfg.Root, ".env")
	if err := os.WriteFile(envPath, []byte("MUSIK_PASSWORD=x\nMUSIK_LIBRARY=/old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	music := t.TempDir()

	rec := serve(server, local(jsonReq("PUT", "/api/settings",
		`{"addr":"0.0.0.0:9001","library":`+jsonString(music)+`,"public_base_url":"http://192.168.1.5:9001"}`)))
	if rec.Code != 200 {
		t.Fatalf("put status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		Saved          map[string]string `json:"saved"`
		RestartPending bool              `json:"restart_pending"`
		Phone          struct {
			Token string `json:"token"`
		} `json:"phone"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Saved["addr"] != "0.0.0.0:9001" || got.Saved["library"] != music || !got.RestartPending {
		t.Fatalf("settings=%+v", got)
	}
	raw, _ := os.ReadFile(envPath)
	if !strings.Contains(string(raw), "MUSIK_PASSWORD=x") || !strings.Contains(string(raw), "MUSIK_PLAYER_ADDR=0.0.0.0:9001") {
		t.Fatalf(".env=%s", raw)
	}

	for _, bad := range []string{`{"addr":"8.8.8.8:80"}`, `{"library":"relative"}`, `{"public_base_url":"nope"}`, `{}`} {
		if rec := serve(server, local(jsonReq("PUT", "/api/settings", bad))); rec.Code != 400 {
			t.Errorf("%s status=%d", bad, rec.Code)
		}
	}

	if err := os.Mkdir(filepath.Join(music, "Rock"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec = serve(server, local(httptest.NewRequest("GET", "/api/settings/dirs?path="+music, nil)))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"Rock"`) {
		t.Fatalf("dirs status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSettingsOpenModeOnlyFromThisComputer(t *testing.T) {
	server := openTestServer(t) // AuthDisabled: open mode
	req := httptest.NewRequest("GET", "/api/settings", nil)
	req.RemoteAddr = "192.168.1.50:5000"
	if rec := serve(server, req); rec.Code != 403 {
		t.Fatalf("LAN client status=%d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/settings", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	if rec := serve(server, req); rec.Code != 200 {
		t.Fatalf("loopback status=%d", rec.Code)
	}
}

// local marks a request as coming from this computer: the test server runs in
// open mode, where settings are loopback-only.
func local(req *http.Request) *http.Request {
	req.RemoteAddr = "127.0.0.1:5000"
	return req
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestSettingsQR(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, local(httptest.NewRequest("GET", "/api/settings/qr?url=http://192.168.1.5:8787", nil)))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(rec.Body.String(), "<svg") {
		t.Fatalf("status=%d type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, bad := range []string{"/api/settings/qr", "/api/settings/qr?url=ftp://x"} {
		if rec := serve(server, local(httptest.NewRequest("GET", bad, nil))); rec.Code != 400 {
			t.Errorf("%s status=%d", bad, rec.Code)
		}
	}
}

func TestSettingsExternalAddressIsTheOnlyPhoneURL(t *testing.T) {
	server := openTestServer(t)
	server.Cfg.Addr = "0.0.0.0:8787"
	phoneURLs := func() []string {
		rec := serve(server, local(jsonReq("GET", "/api/settings", "")))
		var got struct {
			Phone struct {
				URLs []string `json:"urls"`
			} `json:"phone"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got.Phone.URLs
	}
	envPath := filepath.Join(server.Cfg.Root, ".env")
	if err := os.WriteFile(envPath, []byte("MUSIK_PUBLIC_BASE_URL=https://music.example.com/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := phoneURLs(); len(got) != 1 || got[0] != "https://music.example.com" {
		t.Fatalf("external address: urls=%v", got)
	}
	// A public URL inside the home network keeps the LAN addresses next to it.
	if err := os.WriteFile(envPath, []byte("MUSIK_PUBLIC_BASE_URL=http://192.168.1.5:8787\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := phoneURLs(); len(got) == 0 || got[0] != "http://192.168.1.5:8787" {
		t.Fatalf("LAN public url: urls=%v", got)
	}
}
