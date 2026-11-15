package plugins

import (
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Manifest is one discovered plugin document. Extra holds unknown properties.
type Manifest struct {
	ID                 string
	Executable         string
	Capabilities       []string
	Name               string
	Version            string
	Description        string
	Extra              map[string]any
	Source             string
	ResolvedExecutable string
}

// ParseManifest validates a manifest object. It does not open the executable.
func ParseManifest(payload map[string]any) (Manifest, error) {
	if payload == nil {
		return Manifest{}, manifestError("manifest is invalid")
	}
	if err := requireProtocol(payload["protocol"]); err != nil {
		return Manifest{}, manifestError("unsupported protocol version")
	}
	id, ok := stringField(payload, "id")
	if !ok || !validPluginID(id) {
		return Manifest{}, manifestError("plugin id is invalid")
	}
	executable, ok := stringField(payload, "executable")
	if !ok || !validText(executable, maxExecutableChars) {
		return Manifest{}, manifestError("executable path is invalid")
	}
	capabilities, ok := capabilityList(payload["capabilities"])
	if !ok {
		return Manifest{}, manifestError("capabilities are invalid")
	}
	name, ok := optionalText(payload, "name", maxNameChars)
	if !ok {
		return Manifest{}, manifestError("manifest field is invalid")
	}
	version, ok := optionalText(payload, "version", maxVersionChars)
	if !ok {
		return Manifest{}, manifestError("manifest field is invalid")
	}
	description, ok := optionalText(payload, "description", maxDescriptionChars)
	if !ok {
		return Manifest{}, manifestError("manifest field is invalid")
	}
	return Manifest{
		ID:           id,
		Executable:   executable,
		Capabilities: capabilities,
		Name:         name,
		Version:      version,
		Description:  description,
		Extra: extraFields(payload,
			"id", "executable", "protocol", "capabilities", "name", "version", "description"),
	}, nil
}

// LoadManifest reads a manifest file and resolves its executable.
func LoadManifest(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, manifestError("manifest is unreadable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil || len(data) > maxManifestBytes {
		return Manifest{}, manifestError("manifest is unreadable")
	}
	payload, err := decodeObject(data)
	if err != nil {
		return Manifest{}, manifestError("manifest is unreadable")
	}
	manifest, err := ParseManifest(payload)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Source = path
	resolved, err := ResolveExecutable(path, manifest.Executable)
	if err != nil {
		return manifest, err
	}
	manifest.ResolvedExecutable = resolved
	return manifest, nil
}

// ResolveExecutable resolves executable relative to the manifest directory.
// The returned path is absolute, symlink-resolved, and runnable.
func ResolveExecutable(manifestFile, executable string) (string, error) {
	if !validText(executable, maxExecutableChars) {
		return "", manifestError("executable path is invalid")
	}
	candidate := executable
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(filepath.Dir(manifestFile), candidate)
	}
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return "", manifestError("executable path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", manifestError("executable is missing")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", manifestError("executable is missing")
	}
	if info.Mode()&0o111 == 0 {
		return "", manifestError("executable is not runnable")
	}
	return resolved, nil
}

// CompatibleCapabilities returns capabilities both sides advertise and
// protocol v1 can run. The manifest order is preserved. Other names stay on
// the documents and are omitted here.
func CompatibleCapabilities(manifest Manifest, describe Describe) ([]string, error) {
	if manifest.ID == "" || describe.ID == "" {
		return nil, manifestError("plugin id is invalid")
	}
	if manifest.ID != describe.ID {
		return nil, manifestError("plugin id does not match the manifest")
	}
	advertised := make(map[string]struct{}, len(describe.Capabilities))
	for _, capability := range describe.Capabilities {
		advertised[capability] = struct{}{}
	}
	var usable []string
	for _, capability := range manifest.Capabilities {
		if _, ok := advertised[capability]; !ok {
			continue
		}
		if !runnableCapability(capability) {
			continue
		}
		usable = append(usable, capability)
	}
	if !hasString(usable, CapabilityImportURL) {
		return nil, manifestError("plugin has no usable capabilities")
	}
	return usable, nil
}

func runnableCapability(name string) bool {
	return name == CapabilityImportURL || name == CapabilitySearch
}

func hasString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ConflictingIDs returns ids that appear on more than one manifest, in
// first-seen order.
func ConflictingIDs(manifests []Manifest) []string {
	seen := make(map[string]struct{}, len(manifests))
	var conflicts []string
	conflictSet := map[string]struct{}{}
	for _, manifest := range manifests {
		if _, ok := seen[manifest.ID]; ok {
			if _, already := conflictSet[manifest.ID]; !already {
				conflicts = append(conflicts, manifest.ID)
				conflictSet[manifest.ID] = struct{}{}
			}
			continue
		}
		seen[manifest.ID] = struct{}{}
	}
	return conflicts
}

// DefaultDirs returns the manifest directories a host scans.
// MUSIK_PLUGIN_DIRS, when it contains a path, replaces the default. The
// default is $XDG_DATA_HOME/musik/plugins, or ~/.local/share/musik/plugins
// when XDG_DATA_HOME is empty. home is used for ~ expansion and the default.
func DefaultDirs(env map[string]string, home string) ([]string, error) {
	if env == nil {
		env = map[string]string{}
	}
	override := strings.TrimSpace(env["MUSIK_PLUGIN_DIRS"])
	if override != "" {
		var dirs []string
		for _, part := range strings.Split(override, string(os.PathListSeparator)) {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			expanded, err := expandHome(part, home)
			if err != nil {
				return nil, err
			}
			dirs = append(dirs, expanded)
		}
		if len(dirs) > 0 {
			return dirs, nil
		}
	}
	xdg := strings.TrimSpace(env["XDG_DATA_HOME"])
	var base string
	if xdg != "" {
		expanded, err := expandHome(xdg, home)
		if err != nil {
			return nil, err
		}
		base = expanded
	} else {
		if home == "" {
			return nil, manifestError("home directory is unknown")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return []string{filepath.Join(base, "musik", "plugins")}, nil
}

// DefaultDirsFromEnv reads the process environment and the user home directory.
func DefaultDirsFromEnv() ([]string, error) {
	env := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			env[key] = value
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return DefaultDirs(env, home)
}

func expandHome(path, home string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	if home == "" {
		return "", manifestError("home directory is unknown")
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// ManifestFiles returns *.json files placed directly in each existing directory.
// Missing directories are skipped. Nested JSON files are not returned.
func ManifestFiles(dirs []string) ([]string, []Rejection, error) {
	var found []string
	var rejected []Rejection
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			rejected = append(rejected, Rejection{Source: dir, Reason: "plugin directory is unreadable"})
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			found = append(found, path)
		}
	}
	return found, rejected, nil
}

func requireProtocol(value any) error {
	text, ok := value.(string)
	if !ok || text != ProtocolVersion {
		return failure(CodeProtocolMismatch, "unsupported protocol version")
	}
	return nil
}
