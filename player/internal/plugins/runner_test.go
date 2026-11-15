package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestImportURLWritesAndRedacts(t *testing.T) {
	dir := t.TempDir()
	library := t.TempDir()
	url := "https://example.invalid/musik-plugin-secret?a=1&b=2"
	token := "musik-test-token-value"
	t.Setenv("MUSIK_API_TOKEN", token)
	script := writeScript(t, dir, "plugin; touch pwned", runPlugin("example", `
url = req["input"]["url"]
emit({"type": "progress", "protocol": "1", "id": req["id"], "progress": {"pct": 40, "phase": "download", "message": url}})
lib = pathlib.Path(req["input"]["library"])
dest = req["input"]["dest_dir"]
final = lib.joinpath(*dest.split("/"), "track.flac")
final.parent.mkdir(parents=True, exist_ok=True)
part = final.with_name(final.name + ".part")
part.write_bytes(b"audio")
part.replace(final)
emit({"type": "result", "protocol": "1", "id": req["id"], "result": {"paths": [dest + "/track.flac"]}})
sys.stderr.write(url + "\n")
raise SystemExit(0)
`))
	var progress []Progress
	result, err := NewRunner(testLimits()).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID:      "job-1",
		URL:     url,
		Library: library,
		OnProgress: func(update Progress) {
			progress = append(progress, update)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Paths) != 1 || result.Paths[0] != "imports/example/track.flac" {
		t.Fatalf("paths = %#v", result.Paths)
	}
	body, err := os.ReadFile(result.Absolute[0])
	if err != nil || string(body) != "audio" {
		t.Fatalf("file = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(result.Absolute[0]), "track.flac.part")); !os.IsNotExist(err) {
		t.Fatalf("partial file err = %v", err)
	}
	if len(progress) != 1 || progress[0].Message != "[redacted]" || progress[0].Phase != "download" {
		t.Fatalf("progress = %#v", progress)
	}
	assertNoShell(t, dir, script)
	envText := mustRead(t, filepath.Join(dir, "env"))
	if strings.Contains(envText, token) || strings.Contains(envText, url) || strings.Contains(envText, "MUSIK_API_TOKEN") {
		t.Fatal("plugin environment leaked a secret or the url")
	}
	requestText := mustRead(t, filepath.Join(dir, "raw-request.txt"))
	if !strings.Contains(requestText, url) || strings.Contains(requestText, `\u0026`) {
		t.Fatalf("request = %s", requestText)
	}
	if strings.Contains(requestText, `"dest_dir": "imports/example"`) == false && !strings.Contains(requestText, `"dest_dir":"imports/example"`) {
		t.Fatalf("dest dir missing from %s", requestText)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); !os.IsNotExist(err) {
		t.Fatal("shell metacharacter was executed")
	}
	if _, err := os.Stat("pwned"); err == nil {
		t.Fatal("shell metacharacter was executed in the package directory")
	}
}

func TestImportURLPluginErrorIsRedacted(t *testing.T) {
	dir := t.TempDir()
	library := t.TempDir()
	url := "https://example.invalid/musik-plugin-secret"
	script := writeScript(t, dir, "plugin", runPlugin("example", `
url = req["input"]["url"]
sys.stderr.write("failed " + url + "\n")
emit({"type": "error", "protocol": "1", "id": req["id"], "error": {"code": "unsupported_url", "message": url}})
raise SystemExit(1)
`))
	_, err := NewRunner(testLimits()).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID: "job-1", URL: url, Library: library, DestDir: "imports/example",
	})
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != CodeUnsupportedURL || failure.Message != "[redacted]" {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), url) || strings.Contains(failure.Stderr, url) {
		t.Fatalf("url leaked in %#v", failure)
	}
	if !strings.Contains(failure.Stderr, "[redacted]") || failure.ExitCode != 1 {
		t.Fatalf("stderr = %q exit %d", failure.Stderr, failure.ExitCode)
	}
}

func TestImportURLProtocolFailures(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		message string
	}{
		{name: "no result", body: "raise SystemExit(0)\n", message: "plugin exited without a result"},
		{name: "exit 2", body: "sys.stderr.write('unusable')\nraise SystemExit(2)\n", message: "plugin configuration is unusable"},
		{name: "bad json", body: "sys.stdout.write('not-json\\n')\nraise SystemExit(0)\n", message: "plugin protocol is invalid"},
		{name: "wrong id", body: `
lib = pathlib.Path(req["input"]["library"])
dest = req["input"]["dest_dir"]
final = lib.joinpath(*dest.split("/"), "track.flac")
final.parent.mkdir(parents=True, exist_ok=True)
final.write_bytes(b"audio")
emit({"type": "result", "protocol": "1", "id": "other", "result": {"paths": [dest + "/track.flac"]}})
raise SystemExit(0)
`, message: "plugin protocol is invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			script := writeScript(t, dir, "plugin", runPlugin("example", tc.body))
			_, err := NewRunner(testLimits()).ImportURL(context.Background(), examplePlugin(script), ImportJob{
				ID: "job-1", URL: "https://example.invalid/track", Library: t.TempDir(), DestDir: "imports/example",
			})
			if err == nil || err.Error() != tc.message || strings.Contains(err.Error(), "example.invalid") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestImportURLRejectsOutsideDestination(t *testing.T) {
	dir := t.TempDir()
	library := t.TempDir()
	script := writeScript(t, dir, "plugin", runPlugin("example", `
lib = pathlib.Path(req["input"]["library"])
other = lib / "imports" / "other"
other.mkdir(parents=True, exist_ok=True)
(other / "track.flac").write_bytes(b"x")
emit({"type": "result", "protocol": "1", "id": req["id"], "result": {"paths": ["imports/other/track.flac"]}})
raise SystemExit(0)
`))
	_, err := NewRunner(testLimits()).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: library, DestDir: "imports/example",
	})
	if err == nil || err.Error() != "result path is outside the destination" {
		t.Fatalf("err = %v", err)
	}
}

func TestImportURLRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	library := filepath.Join(root, "library")
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(filepath.Join(library, "imports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(library, "imports", "example")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "track.flac"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := writeScript(t, t.TempDir(), "plugin", runPlugin("example", `
dest = req["input"]["dest_dir"]
emit({"type": "result", "protocol": "1", "id": req["id"], "result": {"paths": [dest + "/track.flac"]}})
raise SystemExit(0)
`))
	_, err := NewRunner(testLimits()).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: library, DestDir: "imports/example",
	})
	if err == nil || err.Error() != "result path is outside the library" || strings.Contains(err.Error(), outside) {
		t.Fatalf("err = %v", err)
	}
}

func TestImportURLDoesNotStartForInvalidInput(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "plugin", runPlugin("example", "raise SystemExit(0)\n"))
	runner := NewRunner(testLimits())
	plugin := examplePlugin(script)
	_, err := runner.ImportURL(context.Background(), plugin, ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: "relative",
	})
	if !IsCode(err, CodeInvalidRequest) || err.Error() != "library path is invalid" {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "argv.json")); !os.IsNotExist(statErr) {
		t.Fatal("plugin started for an invalid library")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runner.ImportURL(ctx, plugin, ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: t.TempDir(),
	})
	if !errors.Is(err, context.Canceled) || !IsCode(err, CodeCancelled) {
		t.Fatalf("cancel err = %v", err)
	}
}

func TestImportURLTimesOut(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "plugin", runPlugin("example", `
import signal
(ROOT / "pid").write_text(str(os.getpid()))
signal.signal(signal.SIGTERM, signal.SIG_IGN)
time.sleep(30)
`))
	limits := testLimits()
	limits.Timeout = 200 * time.Millisecond
	limits.KillGrace = 200 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	errc := make(chan error, 1)
	go func() {
		_, err := NewRunner(limits).ImportURL(ctx, examplePlugin(script), ImportJob{
			ID: "job-1", URL: "https://example.invalid/track", Library: t.TempDir(),
		})
		errc <- err
	}()
	waitForFile(t, filepath.Join(dir, "pid"), 2*time.Second)
	var err error
	select {
	case err = <-errc:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out plugin was not stopped")
	}
	if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "plugin timed out" {
		t.Fatalf("err = %v after %s", err, time.Since(started))
	}
	pid := readPID(t, filepath.Join(dir, "pid"))
	if processAlive(pid) {
		t.Fatalf("pid %d still running", pid)
	}
}

func TestImportURLLimitsConcurrency(t *testing.T) {
	library := t.TempDir()
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	body := `
(ROOT / "started").write_text("1")
while not (ROOT / "release").exists():
    time.sleep(0.02)
lib = pathlib.Path(req["input"]["library"])
dest = req["input"]["dest_dir"]
final = lib.joinpath(*dest.split("/"), "track.flac")
final.parent.mkdir(parents=True, exist_ok=True)
final.write_bytes(b"audio")
emit({"type": "result", "protocol": "1", "id": req["id"], "result": {"paths": [dest + "/track.flac"]}})
raise SystemExit(0)
`
	first := writeScript(t, firstDir, "plugin", runPlugin("one", body))
	second := writeScript(t, secondDir, "plugin", runPlugin("two", body))
	limits := testLimits()
	limits.Concurrency = 1
	limits.Timeout = 5 * time.Second
	runner := NewRunner(limits)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	errc := make(chan error, 2)
	go func() {
		_, err := runner.ImportURL(ctx, examplePluginID("one", first), ImportJob{
			ID: "job-1", URL: "https://example.invalid/one", Library: library,
		})
		errc <- err
	}()
	waitForFile(t, filepath.Join(firstDir, "started"), 2*time.Second)
	go func() {
		_, err := runner.ImportURL(ctx, examplePluginID("two", second), ImportJob{
			ID: "job-2", URL: "https://example.invalid/two", Library: library,
		})
		errc <- err
	}()
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(secondDir, "started")); err == nil {
			t.Fatal("second plugin started before a slot was free")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(firstDir, "release"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, filepath.Join(secondDir, "started"), 2*time.Second)
	if err := os.WriteFile(filepath.Join(secondDir, "release"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case err := <-errc:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestImportURLLineLimit(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "plugin", runPlugin("example", `
sys.stdout.write("x" * (1024 * 1024 + 2) + "\n")
raise SystemExit(0)
`))
	limits := testLimits()
	limits.KillGrace = 500 * time.Millisecond
	_, err := NewRunner(limits).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: t.TempDir(),
	})
	if err == nil || err.Error() != "plugin protocol is invalid" {
		t.Fatalf("err = %v", err)
	}
}

func TestProgressHandlerPanicStopsPlugin(t *testing.T) {
	dir := t.TempDir()
	script := writeScript(t, dir, "plugin", runPlugin("example", `
emit({"type": "progress", "protocol": "1", "id": req["id"], "progress": {"phase": "download"}})
time.sleep(30)
`))
	limits := testLimits()
	limits.Timeout = 3 * time.Second
	limits.KillGrace = 300 * time.Millisecond
	_, err := NewRunner(limits).ImportURL(context.Background(), examplePlugin(script), ImportJob{
		ID: "job-1", URL: "https://example.invalid/track", Library: t.TempDir(),
		OnProgress: func(Progress) { panic("boom") },
	})
	if err == nil || err.Error() != "progress handler failed" {
		t.Fatalf("err = %v", err)
	}
}

func TestSearchKeepsTheQueryOutOfArgv(t *testing.T) {
	query := "musik-plugin-search-secret"
	dir := t.TempDir()
	script := writeScript(t, dir, "plugin", `
import json, os, sys, pathlib
ROOT = pathlib.Path(__file__).resolve().parent
def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()
(ROOT / "argv.json").write_text(json.dumps(sys.argv))
(ROOT / "env").write_text("\n".join("%s=%s" % item for item in os.environ.items()))
args = sys.argv[1:]
if args == ["describe"]:
    emit({"type": "describe", "protocol": "1", "id": "example", "capabilities": ["library.import_url", "library.search"]})
    raise SystemExit(0)
if args != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
emit({"type": "result", "protocol": "1", "id": req["id"], "result": {"items": [
    {"title": "Example Track", "artist": "Example Artist", "url": "https://example.invalid/track", "duration": 248}
]}})
`)
	items, err := NewRunner(testLimits()).Search(context.Background(), Plugin{
		ID: "example", Executable: script, Capabilities: []string{CapabilityImportURL, CapabilitySearch},
	}, SearchJob{ID: "job-1", Query: query, Limit: DefaultSearchLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "Example Track" || items[0].URL != "https://example.invalid/track" || items[0].Duration == nil || *items[0].Duration != 248 {
		t.Fatalf("items = %#v", items)
	}
	var argv []string
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(dir, "argv.json"))), &argv); err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[1] != "run" || strings.Contains(strings.Join(argv, "\n"), query) || strings.Contains(mustRead(t, filepath.Join(dir, "env")), query) {
		t.Fatal("query leaked into argv or the environment")
	}

	leaky := writeScript(t, dir, "leaky", `
import json, sys
req = json.loads(sys.stdin.readline())
sys.stderr.write(req["input"]["query"] + "\n")
raise SystemExit(1)
`)
	_, err = NewRunner(testLimits()).Search(context.Background(), Plugin{
		ID: "example", Executable: leaky, Capabilities: []string{CapabilitySearch},
	}, SearchJob{ID: "job-1", Query: query, Limit: 8})
	var failure *Failure
	if !errors.As(err, &failure) || strings.Contains(failure.Error(), query) || strings.Contains(failure.Stderr, query) {
		t.Fatalf("failure = %#v", err)
	}
}

func examplePlugin(executable string) Plugin {
	return examplePluginID("example", executable)
}

func examplePluginID(id, executable string) Plugin {
	return Plugin{ID: id, Executable: executable, Capabilities: []string{CapabilityImportURL}}
}

func runPlugin(id, body string) string {
	return `
import json, os, sys, pathlib, time
ROOT = pathlib.Path(__file__).resolve().parent
def emit(obj):
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()
(ROOT / "argv.json").write_text(json.dumps(sys.argv))
(ROOT / "cwd").write_text(os.getcwd())
(ROOT / "env").write_text("\n".join("%s=%s" % (key, value) for key, value in os.environ.items()))
args = sys.argv[1:]
if args == ["describe"]:
    emit({"type": "describe", "protocol": "1", "id": "` + id + `", "capabilities": ["library.import_url"]})
    raise SystemExit(0)
if args != ["run"]:
    sys.stderr.write("usage: plugin describe|run\n")
    raise SystemExit(2)
raw = sys.stdin.readline()
(ROOT / "raw-request.txt").write_text(raw)
req = json.loads(raw)
(ROOT / "request.json").write_text(json.dumps(req))
` + body
}

func assertNoShell(t *testing.T, dir, script string) {
	t.Helper()
	var argv []string
	if err := json.Unmarshal([]byte(mustRead(t, filepath.Join(dir, "argv.json"))), &argv); err != nil {
		t.Fatal(err)
	}
	if len(argv) != 2 || argv[1] != "run" {
		t.Fatalf("argv = %#v", argv)
	}
	cwd, err := filepath.EvalSymlinks(strings.TrimSpace(mustRead(t, filepath.Join(dir, "cwd"))))
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(filepath.Dir(script))
	if err != nil {
		t.Fatal(err)
	}
	if cwd != want {
		t.Fatalf("cwd = %s, want %s", cwd, want)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readPID(t *testing.T, path string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(mustRead(t, path)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}
