package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDirs(t *testing.T) {
	home := t.TempDir()
	dirs, err := DefaultDirs(map[string]string{}, home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "musik", "plugins")
	if len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("dirs = %#v, want %s", dirs, want)
	}

	xdg := filepath.Join(t.TempDir(), "xdg")
	dirs, err = DefaultDirs(map[string]string{"XDG_DATA_HOME": xdg}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != filepath.Join(xdg, "musik", "plugins") {
		t.Fatalf("xdg dirs = %#v", dirs)
	}

	a := filepath.Join(t.TempDir(), "a")
	b := filepath.Join(t.TempDir(), "b")
	env := map[string]string{
		"MUSIK_PLUGIN_DIRS": a + string(os.PathListSeparator) + b,
		"XDG_DATA_HOME":     xdg,
	}
	dirs, err = DefaultDirs(env, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 || dirs[0] != a || dirs[1] != b {
		t.Fatalf("override dirs = %#v", dirs)
	}

	dirs, err = DefaultDirs(map[string]string{"MUSIK_PLUGIN_DIRS": "~/plugins"}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != filepath.Join(home, "plugins") {
		t.Fatalf("tilde dirs = %#v", dirs)
	}

	dirs, err = DefaultDirs(map[string]string{"MUSIK_PLUGIN_DIRS": string(os.PathListSeparator)}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("empty override dirs = %#v, want default %s", dirs, want)
	}
}

func TestManifestFilesAreOneLevelDeep(t *testing.T) {
	root := t.TempDir()
	plugins := filepath.Join(root, "plugins")
	nested := filepath.Join(plugins, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	top := filepath.Join(plugins, "example.json")
	if err := os.WriteFile(top, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "hidden.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugins, "notes.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(plugins, "dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	found, rejected, err := ManifestFiles([]string{plugins, filepath.Join(root, "missing")})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 0 || len(found) != 1 || found[0] != top {
		t.Fatalf("found=%v rejected=%v", found, rejected)
	}
}

func TestConflictingIDs(t *testing.T) {
	manifests := []Manifest{{ID: "alpha"}, {ID: "beta"}, {ID: "alpha"}, {ID: "alpha"}}
	got := ConflictingIDs(manifests)
	if len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("conflicts = %#v", got)
	}
}

func TestParseManifest(t *testing.T) {
	payload, err := decodeObject(exampleBytes(t, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParseManifest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ID != "example" || manifest.Executable != "bin/example-plugin" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != CapabilityImportURL {
		t.Fatalf("capabilities = %#v", manifest.Capabilities)
	}

	raw, err := decodeObject([]byte(`{"id":"example","executable":"bin/plugin","protocol":1,"capabilities":["library.import_url"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(raw); err == nil || err.Error() != "unsupported protocol version" {
		t.Fatalf("integer protocol err = %v", err)
	}

	raw, err = decodeObject([]byte(`{"id":"example","executable":"bin/plugin","protocol":"1","capabilities":["library.import_url","library.future_tool"],"vendor":"acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Extra["vendor"] != "acme" || len(manifest.Capabilities) != 2 {
		t.Fatalf("extra manifest = %#v", manifest)
	}
}

func TestCompatibleCapabilities(t *testing.T) {
	manifest, err := ParseManifest(mustObject(t, `{"id":"example","executable":"bin/plugin","protocol":"1","capabilities":["library.import_url","library.future_tool"]}`))
	if err != nil {
		t.Fatal(err)
	}
	describe, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"example","capabilities":["library.future_tool","library.import_url"]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := CompatibleCapabilities(manifest, describe)
	if err != nil || len(got) != 1 || got[0] != CapabilityImportURL {
		t.Fatalf("usable = %#v, %v", got, err)
	}

	other, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"other","capabilities":["library.import_url"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompatibleCapabilities(manifest, other); err == nil || err.Error() != "plugin id does not match the manifest" {
		t.Fatalf("mismatch err = %v", err)
	}

	future, err := ParseManifest(mustObject(t, `{"id":"example","executable":"bin/plugin","protocol":"1","capabilities":["library.future_tool"]}`))
	if err != nil {
		t.Fatal(err)
	}
	described, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"example","capabilities":["library.future_tool"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompatibleCapabilities(future, described); err == nil || err.Error() != "plugin has no usable capabilities" {
		t.Fatalf("future err = %v", err)
	}

	both, err := ParseManifest(mustObject(t, `{"id":"example","executable":"bin/plugin","protocol":"1","capabilities":["library.import_url","library.search"]}`))
	if err != nil {
		t.Fatal(err)
	}
	describedBoth, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"example","capabilities":["library.search","library.import_url"]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, err = CompatibleCapabilities(both, describedBoth)
	if err != nil || len(got) != 2 || got[0] != CapabilityImportURL || got[1] != CapabilitySearch {
		t.Fatalf("search usable = %#v, %v", got, err)
	}
	searchOnly, err := ParseManifest(mustObject(t, `{"id":"example","executable":"bin/plugin","protocol":"1","capabilities":["library.search"]}`))
	if err != nil {
		t.Fatal(err)
	}
	describedSearch, err := ParseDescribe(mustObject(t, `{"type":"describe","protocol":"1","id":"example","capabilities":["library.search"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CompatibleCapabilities(searchOnly, describedSearch); err == nil || err.Error() != "plugin has no usable capabilities" {
		t.Fatalf("search-only err = %v", err)
	}
}

func TestLoadManifestResolvesExecutable(t *testing.T) {
	root := t.TempDir()
	script := writeScript(t, filepath.Join(root, "bin"), "plugin", "raise SystemExit(0)\n")
	path := writeManifest(t, root, "example.json", map[string]any{
		"id":           "example",
		"executable":   "bin/plugin",
		"protocol":     "1",
		"capabilities": []string{CapabilityImportURL},
	})
	loaded, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	resolvedScript, err := filepath.EvalSymlinks(script)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ResolvedExecutable != resolvedScript || loaded.Source != path {
		t.Fatalf("loaded = %#v", loaded)
	}

	if _, err := ResolveExecutable(path, "bin/missing"); err == nil || err.Error() != "executable is missing" {
		t.Fatalf("missing err = %v", err)
	}
	if err := os.Chmod(script, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveExecutable(path, "bin/plugin"); err == nil || err.Error() != "executable is not runnable" {
		t.Fatalf("mode err = %v", err)
	}
}

func TestUnreadablePluginDir(t *testing.T) {
	root := t.TempDir()
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if _, err := os.ReadDir(locked); err == nil {
		t.Skip("process can read a mode 0 directory")
	}
	_, rejected, err := ManifestFiles([]string{locked})
	if err != nil {
		t.Fatal(err)
	}
	if len(rejected) != 1 || rejected[0].Reason != "plugin directory is unreadable" {
		t.Fatalf("rejected = %#v", rejected)
	}
}

func exampleBytes(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("..", "..", "..", "sdk", "plugin", "v1", "examples", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	payload, err := decodeObject([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestExampleManifestJSON(t *testing.T) {
	var payload map[string]any
	if err := json.Unmarshal(exampleBytes(t, "manifest.json"), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["protocol"] != ProtocolVersion {
		t.Fatalf("protocol = %#v", payload["protocol"])
	}
}
