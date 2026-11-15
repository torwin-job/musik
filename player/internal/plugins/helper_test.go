package plugins

import (
	"os"
	"path/filepath"
	"time"
)

func writeScript(t testingT, dir, name, body string) string {
	t.Helper()
	if len(body) < 2 || body[0] != '#' || body[1] != '!' {
		body = "#!/usr/bin/env python3\n" + body
	}
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeManifest(t testingT, dir, name string, document map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	payload, err := encodeLine(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitForFile(t testingT, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear", path)
}

func testLimits() Limits {
	return Limits{
		Concurrency:     2,
		Timeout:         5 * time.Second,
		DescribeTimeout: 2 * time.Second,
		KillGrace:       500 * time.Millisecond,
		MaxStderr:       4096,
	}
}

// testingT is the subset of *testing.T used by helpers so they stay in one file.
type testingT interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}
