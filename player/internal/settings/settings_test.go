package settings

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateEnvKeepsCommentsAndReplacesInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	orig := "# auth\r\nMUSIK_PASSWORD=secret\r\nMUSIK_LIBRARY=/old\r\n# MUSIK_PLAYER_ADDR=0.0.0.0:8787\r\nMUSIK_PUBLIC_BASE_URL=http://x:1\r\n"
	if err := os.WriteFile(path, []byte(orig), 0o600); err != nil {
		t.Fatal(err)
	}
	err := UpdateEnv(path, map[string]string{
		KeyLibrary:   `C:\My Music`,
		KeyAddr:      "0.0.0.0:9000",
		KeyPublicURL: "",
		"MUSIK_NEW":  "v",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	got := string(raw)
	want := "# auth\r\nMUSIK_PASSWORD=secret\r\nMUSIK_LIBRARY='C:\\My Music'\r\nMUSIK_PLAYER_ADDR=0.0.0.0:9000\r\n# MUSIK_PUBLIC_BASE_URL=\r\nMUSIK_NEW=v\r\n"
	if got != want {
		t.Fatalf("got:\n%q\nwant:\n%q", got, want)
	}
	env, err := ReadEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if env[KeyLibrary] != `C:\My Music` || env[KeyAddr] != "0.0.0.0:9000" || env["MUSIK_PASSWORD"] != "secret" {
		t.Fatalf("env=%v", env)
	}
	if _, ok := env[KeyPublicURL]; ok {
		t.Fatal("emptied key must be commented out")
	}
}

func TestUpdateEnvCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := UpdateEnv(path, map[string]string{KeyLibrary: "/music"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	if strings.TrimSpace(string(raw)) != "MUSIK_LIBRARY=/music" {
		t.Fatalf("got %q", raw)
	}
}

func TestValidateAddr(t *testing.T) {
	for _, ok := range []string{":8787", "0.0.0.0:8787", "127.0.0.1:80", "localhost:8787"} {
		if err := ValidateAddr(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"8787", "0.0.0.0:0", "0.0.0.0:70000", "nonsense:80", "203.0.113.77:8787"} {
		if err := ValidateAddr(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for _, ip := range LocalIPs() {
		if err := ValidateAddr(ip + ":8787"); err != nil {
			t.Errorf("own ip %s: %v", ip, err)
		}
	}
}

func TestValidateLibraryAndPublicURL(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateLibrary(dir); err != nil {
		t.Fatal(err)
	}
	if ValidateLibrary("relative/path") == nil || ValidateLibrary(filepath.Join(dir, "missing")) == nil {
		t.Fatal("bad library accepted")
	}
	if ValidatePublicURL("") != nil || ValidatePublicURL("http://192.168.1.5:8787") != nil {
		t.Fatal("good url rejected")
	}
	if ValidatePublicURL("192.168.1.5:8787") == nil || ValidatePublicURL("ftp://x") == nil {
		t.Fatal("bad url accepted")
	}
}

func TestListDirsSkipsHiddenAndFiles(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"Rock", "alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "file.mp3"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ListDirs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Dirs) != 2 || got.Dirs[0].Name != "alpha" || got.Dirs[1].Name != "Rock" || got.Parent == "" {
		t.Fatalf("listing=%+v", got)
	}
	roots, err := ListDirs("")
	if err != nil || len(roots.Dirs) == 0 {
		t.Fatalf("roots=%+v err=%v", roots, err)
	}
}

func TestCleanEnvDropsMusikValues(t *testing.T) {
	got := cleanEnv([]string{"PATH=/bin", "MUSIK_LIBRARY=/old", "musik_player_addr=x", "MUSIK_ROOT=/r", "MUSIK_SUPERVISOR=systemd"})
	if strings.Join(got, ",") != "PATH=/bin,MUSIK_ROOT=/r,MUSIK_SUPERVISOR=systemd" {
		t.Fatalf("got %v", got)
	}
}

func TestConnectPayloadAndQR(t *testing.T) {
	got := ConnectPayload("https://music.example.com/", "tok en")
	if got != "musik://connect?token=tok+en&url=https%3A%2F%2Fmusic.example.com" {
		t.Fatalf("payload=%q", got)
	}
	if ConnectPayload("http://10.0.0.2:8787", "") != "musik://connect?url=http%3A%2F%2F10.0.0.2%3A8787" {
		t.Fatal("token must be omitted when empty")
	}
	svg, err := QRSVG(got)
	if err != nil || !strings.HasPrefix(svg, "<svg") || !strings.Contains(svg, `fill="#000" d="M`) {
		t.Fatalf("svg=%.80s err=%v", svg, err)
	}
}

func TestIsLANURL(t *testing.T) {
	for _, lan := range []string{"http://192.168.1.5:8787", "http://10.0.0.2", "http://127.0.0.1:8787",
		"http://localhost:8787", "http://musik.local", "http://172.20.0.1", "", "nonsense"} {
		if !IsLANURL(lan) {
			t.Errorf("%q should be LAN", lan)
		}
	}
	for _, ext := range []string{"https://music.example.com", "http://37.49.225.127:8787", "http://100.101.102.103:8787"} {
		if IsLANURL(ext) {
			t.Errorf("%q should be external", ext)
		}
	}
}
