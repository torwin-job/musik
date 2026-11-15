package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	if err := os.MkdirAll(filepath.Join(plugins, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "nested", "hidden.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	dupA := writeScript(t, filepath.Join(plugins, "bin"), "dup-a", "import pathlib, sys\npathlib.Path(__file__).resolve().with_name('ran-a').write_text('1')\nraise SystemExit(2)\n")
	dupB := writeScript(t, filepath.Join(plugins, "bin"), "dup-b", "import pathlib, sys\npathlib.Path(__file__).resolve().with_name('ran-b').write_text('1')\nraise SystemExit(2)\n")
	writeManifest(t, plugins, "a-dup-1.json", manifestDoc("dup", "bin/dup-a"))
	writeManifest(t, plugins, "a-dup-2.json", manifestDoc("dup", "bin/dup-b"))

	writeScript(t, filepath.Join(plugins, "bin"), "future", describeSource("future", `["library.future_tool"]`, ""))
	writeManifest(t, plugins, "c-future.json", manifestDoc("future", "bin/future", "library.future_tool"))

	writeScript(t, filepath.Join(plugins, "bin"), "mismatch", describeSource("other", `["library.import_url"]`, ""))
	writeManifest(t, plugins, "d-mismatch.json", manifestDoc("mismatch", "bin/mismatch"))

	slowDir := filepath.Join(plugins, "bin")
	writeScript(t, slowDir, "slow", "import os, time, pathlib\npathlib.Path(__file__).resolve().with_name('slow.pid').write_text(str(os.getpid()))\ntime.sleep(30)\n")
	writeManifest(t, plugins, "e-slow.json", manifestDoc("slow", "bin/slow"))

	quiet := writeScript(t, filepath.Join(plugins, "bin"), "quiet", "raise SystemExit(0)\n")
	if err := os.Chmod(quiet, 0o644); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, plugins, "f-quiet.json", manifestDoc("quiet", "bin/quiet"))

	writeScript(t, filepath.Join(plugins, "bin"), "gamma", describeSource("gamma", `["library.future_tool", "library.import_url"]`, `doc["name"] = "Gamma"`+"\n"+`doc["vendor"] = "acme"`))
	writeManifest(t, plugins, "z-gamma.json", map[string]any{
		"id":           "gamma",
		"executable":   "bin/gamma",
		"protocol":     "1",
		"capabilities": []string{"library.import_url", "library.future_tool"},
		"name":         "From manifest",
	})

	limits := testLimits()
	limits.DescribeTimeout = 300 * time.Millisecond
	limits.KillGrace = 300 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	registry, err := NewRunner(limits).Discover(ctx, []string{plugins, filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get("dup"); ok {
		t.Fatal("duplicate id was activated")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dupA), "ran-a")); err == nil {
		t.Fatal("duplicate executable was started")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dupB), "ran-b")); err == nil {
		t.Fatal("duplicate executable was started")
	}
	plugin, ok := registry.Get("gamma")
	if !ok || len(registry.List()) != 1 {
		t.Fatalf("active = %#v", registry.List())
	}
	if len(plugin.Capabilities) != 1 || plugin.Capabilities[0] != CapabilityImportURL {
		t.Fatalf("usable = %#v", plugin.Capabilities)
	}
	if plugin.Name != "Gamma" || plugin.Describe.Extra["vendor"] != "acme" {
		t.Fatalf("describe metadata = %#v extra %#v", plugin, plugin.Describe.Extra)
	}
	if len(registry.ByCapability(CapabilityImportURL)) != 1 || len(registry.ByCapability("library.future_tool")) != 0 {
		t.Fatalf("by capability = %#v", registry.ByCapability("library.future_tool"))
	}
	reasons := map[string]string{}
	for _, rejection := range registry.Rejections() {
		reasons[rejection.ID+" "+rejection.Source] = rejection.Reason
		if rejection.ID != "" {
			reasons[rejection.ID] = rejection.Reason
		}
	}
	if reasons["dup"] != "duplicate plugin id" {
		t.Fatalf("dup reason = %q rejections=%#v", reasons["dup"], registry.Rejections())
	}
	if reasons["future"] != "plugin has no usable capabilities" {
		t.Fatalf("future reason = %q", reasons["future"])
	}
	if reasons["mismatch"] != "plugin id does not match the manifest" {
		t.Fatalf("mismatch reason = %q", reasons["mismatch"])
	}
	if reasons["slow"] != "describe timed out" {
		t.Fatalf("slow reason = %q", reasons["slow"])
	}
	if reasons["quiet"] != "executable is not runnable" {
		t.Fatalf("quiet reason = %q", reasons["quiet"])
	}
	pidText, err := os.ReadFile(filepath.Join(slowDir, "slow.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(trimSpace(pidText)))
	if err != nil {
		t.Fatal(err)
	}
	if processAlive(pid) {
		t.Fatalf("timed out describe pid %d still running", pid)
	}
	foundBad := false
	for _, rejection := range registry.Rejections() {
		if filepath.Base(rejection.Source) == "bad.json" {
			foundBad = true
		}
	}
	if !foundBad {
		t.Fatalf("bad manifest missing from %#v", registry.Rejections())
	}
}

func manifestDoc(id, executable string, capabilities ...string) map[string]any {
	if len(capabilities) == 0 {
		capabilities = []string{CapabilityImportURL}
	}
	return map[string]any{
		"id":           id,
		"executable":   executable,
		"protocol":     "1",
		"capabilities": capabilities,
	}
}

func describeSource(id, capabilities, mutate string) string {
	return `
import json, sys
doc = {"type": "describe", "protocol": "1", "id": "` + id + `", "capabilities": ` + capabilities + `}
` + mutate + `
sys.stdout.write(json.dumps(doc, indent=2) + "\n")
raise SystemExit(0)
`
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
