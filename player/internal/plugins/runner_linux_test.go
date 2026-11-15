//go:build linux

package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestImportURLCancelKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	library := t.TempDir()
	url := "https://example.invalid/musik-plugin-secret"
	script := writeScript(t, dir, "plugin", `
import json, os, signal, sys, time, pathlib
ROOT = pathlib.Path(__file__).resolve().parent
REQUEST_ID = "0"

def on_term(signum, frame):
    sys.stdout.write(json.dumps({
        "type": "error",
        "protocol": "1",
        "id": REQUEST_ID,
        "error": {"code": "cancelled", "message": "cancelled"},
    }) + "\n")
    sys.stdout.flush()
    raise SystemExit(1)

signal.signal(signal.SIGTERM, on_term)
if sys.argv[1:] != ["run"]:
    sys.stderr.write("usage: plugin describe|run\n")
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
REQUEST_ID = req["id"]
(ROOT / "parent.pid").write_text(str(os.getpid()))
child = os.fork()
if child == 0:
    signal.signal(signal.SIGTERM, signal.SIG_DFL)
    (ROOT / "child.pid").write_text(str(os.getpid()))
    while True:
        time.sleep(0.2)
deadline = time.time() + 5
while not (ROOT / "child.pid").exists():
    if time.time() > deadline:
        raise SystemExit(2)
    time.sleep(0.01)
(ROOT / "ready").write_text("1")
time.sleep(60)
`)
	limits := testLimits()
	limits.Timeout = 5 * time.Second
	limits.KillGrace = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := NewRunner(limits).ImportURL(ctx, examplePlugin(script), ImportJob{
			ID: "job-1", URL: url, Library: library,
		})
		errc <- err
	}()
	waitForFile(t, filepath.Join(dir, "ready"), 2*time.Second)
	parent := readPID(t, filepath.Join(dir, "parent.pid"))
	child := readPID(t, filepath.Join(dir, "child.pid"))
	cancel()
	var err error
	select {
	case err = <-errc:
	case <-time.After(4 * time.Second):
		t.Fatal("cancel did not stop the plugin")
	}
	if !IsCode(err, CodeCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err.Error() != "cancelled" && err.Error() != "plugin cancelled" {
		t.Fatalf("message = %q", err.Error())
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(parent) && !processAlive(child) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("parent alive=%v child alive=%v", processAlive(parent), processAlive(child))
}
