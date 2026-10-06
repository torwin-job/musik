// Package settings edits the owner-facing server settings stored in .env:
// listen address, public base URL and the music folder. The running process
// keeps its values until restart; the UI shows saved vs. running values.
package settings

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

const (
	KeyAddr      = "MUSIK_PLAYER_ADDR"
	KeyPublicURL = "MUSIK_PUBLIC_BASE_URL"
	KeyLibrary   = "MUSIK_LIBRARY"
)

// EnvPath is the .env every launcher (start-musik.ps1, musik.sh, systemd
// units, the Python worker) reads.
func EnvPath(root string) string { return filepath.Join(root, ".env") }

// ReadEnv returns KEY=VALUE pairs; comments, blanks and malformed lines are
// skipped and matching surrounding quotes are removed.
func ReadEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseLine(sc.Text())
		if ok {
			out[key] = val
		}
	}
	return out, sc.Err()
}

func parseLine(line string) (key, val string, ok bool) {
	t := strings.TrimSpace(strings.TrimPrefix(line, string(rune(0xFEFF))))
	if t == "" || strings.HasPrefix(t, "#") {
		return "", "", false
	}
	i := strings.IndexByte(t, '=')
	if i < 1 {
		return "", "", false
	}
	key = strings.TrimSpace(t[:i])
	val = strings.TrimSpace(t[i+1:])
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
		val = val[1 : len(val)-1]
	}
	return key, val, true
}

// quote writes a value every reader parses back unchanged. Single quotes:
// python-dotenv and systemd treat them literally, so a Windows path keeps its
// backslashes; plain values stay unquoted like the rest of the file.
func quote(v string) string {
	if !strings.ContainsAny(v, " \t#\"'") {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", "") + "'"
}

// UpdateEnv sets keys in place: an existing "KEY=" line (or a commented
// "# KEY=" template line) is rewritten, other lines and comments are kept,
// missing keys are appended. An empty value comments the key out.
func UpdateEnv(path string, values map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	text := string(raw)
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	render := func(k, v string) string {
		if v == "" {
			return "# " + k + "="
		}
		return k + "=" + quote(v)
	}
	done := map[string]bool{}
	// First pass: live lines win over commented templates.
	for i, line := range lines {
		if key, _, ok := parseLine(line); ok {
			if v, want := values[key]; want && !done[key] {
				lines[i] = render(key, v)
				done[key] = true
			}
		}
	}
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "#") {
			continue
		}
		key, _, ok := parseLine(strings.TrimSpace(strings.TrimPrefix(t, "#")))
		if !ok {
			continue
		}
		if v, want := values[key]; want && !done[key] && v != "" {
			lines[i] = render(key, v)
			done[key] = true
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !done[k] && values[k] != "" {
			lines = append(lines, render(k, values[k]))
		}
	}
	out := strings.Join(lines, newline) + newline
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// SplitAddr turns ":8787" / "0.0.0.0:8787" / "192.168.1.5:8787" into host
// ("" = all interfaces) and port.
func SplitAddr(addr string) (host string, port int, err error) {
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	port, err = strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("bad port %q", p)
	}
	if h == "0.0.0.0" || h == "::" {
		h = ""
	}
	return h, port, nil
}

// ValidateAddr accepts all interfaces, loopback or one of this host's IPs.
func ValidateAddr(addr string) error {
	host, _, err := SplitAddr(addr)
	if err != nil {
		return fmt.Errorf("адрес должен быть вида IP:порт: %v", err)
	}
	if host == "" || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%q — не IP-адрес", host)
	}
	if ip.IsLoopback() {
		return nil
	}
	for _, own := range LocalIPs() {
		if own == host {
			return nil
		}
	}
	return fmt.Errorf("%s не принадлежит этому компьютеру", host)
}

func ValidatePublicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("адрес должен начинаться с http:// или https://")
	}
	return nil
}

// IsLANURL reports whether raw points into the home network (or this
// computer): localhost, *.local, loopback, private and link-local IPs. Any
// other host — a domain, a public IP, a Tailscale 100.x address — is an
// external address that phones use from anywhere. Same rule as isLanUrl in
// app.js.
func IsLANURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func ValidateLibrary(dir string) error {
	if dir == "" {
		return errors.New("папка не выбрана")
	}
	if !filepath.IsAbs(dir) {
		return errors.New("нужен полный путь к папке")
	}
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("папка недоступна: %v", err)
	}
	if !st.IsDir() {
		return errors.New("это не папка")
	}
	return nil
}

// LocalIPs lists this host's IPv4 addresses (no loopback), private LAN
// ranges first — those are what a phone on the same Wi-Fi uses.
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var private, other []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipnet.IP.To4()
			if ip == nil || ip.IsLinkLocalUnicast() {
				continue
			}
			if ip.IsPrivate() {
				private = append(private, ip.String())
			} else {
				other = append(other, ip.String())
			}
		}
	}
	return append(private, other...)
}

type Dir struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type Listing struct {
	Path   string `json:"path"`
	Parent string `json:"parent"`
	Dirs   []Dir  `json:"dirs"`
	Roots  []Dir  `json:"roots"`
}

// ListDirs lists the subfolders of path for the folder picker; an empty path
// lists the roots (drives on Windows).
func ListDirs(path string) (Listing, error) {
	out := Listing{Roots: roots()}
	if path == "" {
		out.Dirs = out.Roots
		return out, nil
	}
	path = filepath.Clean(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		return out, err
	}
	out.Path = path
	if parent := filepath.Dir(path); parent != path {
		out.Parent = parent
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "$") {
			continue
		}
		isDir := e.IsDir()
		if !isDir && e.Type()&os.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(path, name)); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			out.Dirs = append(out.Dirs, Dir{Name: name, Path: filepath.Join(path, name)})
		}
	}
	sort.Slice(out.Dirs, func(i, j int) bool {
		return strings.ToLower(out.Dirs[i].Name) < strings.ToLower(out.Dirs[j].Name)
	})
	return out, nil
}

func roots() []Dir {
	if runtime.GOOS == "windows" {
		var out []Dir
		for c := 'A'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				out = append(out, Dir{Name: string(c) + ":", Path: p})
			}
		}
		return out
	}
	out := []Dir{{Name: "/", Path: "/"}}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, Dir{Name: "~", Path: home})
	}
	for _, p := range []string{"/mnt", "/media", "/srv"} {
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			out = append(out, Dir{Name: p, Path: p})
		}
	}
	return out
}
