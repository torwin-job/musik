package api

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/settings"
)

// settingsAllowed: with auth enabled the gate already let only the owner
// through; in open mode (MUSIK_AUTH_DISABLED) anyone on the LAN would reach
// these endpoints, so they are limited to this computer.
func (s *Server) settingsAllowed(w http.ResponseWriter, r *http.Request) bool {
	if s.Cfg.AuthEnabled() || isLoopback(r) {
		return true
	}
	writeErr(w, 403, "local_only", "без пароля настройки доступны только с этого компьютера")
	return false
}

func (s *Server) savedEnv() map[string]string {
	saved, err := settings.ReadEnv(settings.EnvPath(s.Cfg.Root))
	if err != nil {
		return map[string]string{}
	}
	return saved
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	saved := s.savedEnv()
	savedOr := func(key, running string) string {
		if v, ok := saved[key]; ok {
			return v
		}
		return running
	}
	_, port, _ := settings.SplitAddr(s.Cfg.Addr)
	host, _, _ := settings.SplitAddr(s.Cfg.Addr)
	ips := settings.LocalIPs()
	// Addresses a phone can open. An external address (a domain or an IP
	// outside the home network) set in the settings is the address: phones
	// on mobile data cannot use LAN IPs, so only it is offered. Otherwise the
	// public URL (if any) comes first, then the LAN IPs the player listens on.
	publicURL := strings.TrimRight(savedOr(settings.KeyPublicURL, s.Cfg.PublicBaseURL), "/")
	var phoneURLs []string
	if publicURL != "" {
		phoneURLs = append(phoneURLs, publicURL)
	}
	switch {
	case publicURL != "" && !settings.IsLANURL(publicURL):
		// external address only
	case host == "":
		for _, ip := range ips {
			phoneURLs = append(phoneURLs, "http://"+ip+":"+strconv.Itoa(port))
		}
	case host != "127.0.0.1" && host != "localhost":
		phoneURLs = append(phoneURLs, "http://"+host+":"+strconv.Itoa(port))
	}
	_, envErr := os.Stat(settings.EnvPath(s.Cfg.Root))
	running := map[string]string{
		"addr": s.Cfg.Addr, "public_base_url": s.Cfg.PublicBaseURL, "library": s.Cfg.Library,
	}
	stored := map[string]string{
		"addr":            savedOr(settings.KeyAddr, s.Cfg.Addr),
		"public_base_url": savedOr(settings.KeyPublicURL, s.Cfg.PublicBaseURL),
		"library":         savedOr(settings.KeyLibrary, s.Cfg.Library),
	}
	pending := false
	for k, v := range running {
		if stored[k] != v {
			pending = true
		}
	}
	writeJSON(w, map[string]any{
		"running":           running,
		"saved":             stored,
		"restart_pending":   pending,
		"restart_supported": settings.RestartCommand(s.Cfg.Root) != nil,
		"env_path":          settings.EnvPath(s.Cfg.Root),
		"env_exists":        envErr == nil,
		"platform":          runtime.GOOS,
		"port":              port,
		"local_ips":         ips,
		"phone": map[string]any{
			"urls":         phoneURLs,
			"auth_enabled": s.Cfg.AuthEnabled(),
			"token":        s.Cfg.APIToken,
			"openapi":      "/api/openapi.json",
		},
	})
}

func (s *Server) handleSettingsPut(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	var req struct {
		Addr          *string `json:"addr"`
		PublicBaseURL *string `json:"public_base_url"`
		Library       *string `json:"library"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	values := map[string]string{}
	if req.Addr != nil {
		if err := settings.ValidateAddr(*req.Addr); err != nil {
			writeErr(w, 400, "bad_addr", err.Error())
			return
		}
		values[settings.KeyAddr] = *req.Addr
	}
	if req.PublicBaseURL != nil {
		if err := settings.ValidatePublicURL(*req.PublicBaseURL); err != nil {
			writeErr(w, 400, "bad_public_url", err.Error())
			return
		}
		values[settings.KeyPublicURL] = *req.PublicBaseURL
	}
	if req.Library != nil {
		if err := settings.ValidateLibrary(*req.Library); err != nil {
			writeErr(w, 400, "bad_library", err.Error())
			return
		}
		values[settings.KeyLibrary] = *req.Library
	}
	if len(values) == 0 {
		writeErr(w, 400, "nothing_to_save", "nothing to save")
		return
	}
	if err := settings.UpdateEnv(settings.EnvPath(s.Cfg.Root), values); err != nil {
		writeErr(w, 500, "env_write", err.Error())
		return
	}
	s.handleSettingsGet(w, r)
}

// handleSettingsExternalIP asks a public "what is my IP" service (cached for
// ten minutes; ?refresh=1 asks again) — the address the internet sees.
func (s *Server) handleSettingsExternalIP(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	ext, err := settings.LookupExternalIP(r.Context(), s.HTTP, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeErr(w, 502, "external_ip", "не удалось узнать внешний IP: "+err.Error())
		return
	}
	writeJSON(w, ext)
}

// handleSettingsQR draws the "add this server" QR for the phone apps:
// musik://connect?url=<?url>&token=<API token>. It carries the token, so it is
// only served to the owner (same gate as the rest of the settings).
func (s *Server) handleSettingsQR(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	base := r.URL.Query().Get("url")
	if base == "" {
		writeErr(w, 400, "url_required", "url required")
		return
	}
	if err := settings.ValidatePublicURL(base); err != nil {
		writeErr(w, 400, "bad_url", err.Error())
		return
	}
	token := ""
	if s.Cfg.AuthEnabled() {
		token = s.Cfg.APIToken
	}
	svg, err := settings.QRSVG(settings.ConnectPayload(base, token))
	if err != nil {
		writeErr(w, 500, "qr", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(svg))
}

func (s *Server) handleSettingsDirs(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	listing, err := settings.ListDirs(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, 400, "dir", err.Error())
		return
	}
	writeJSON(w, listing)
}

// handleSettingsRestart answers first, then starts the platform launcher
// (start-musik.ps1 -Restart, musik.sh restart or systemd), which stops this
// process and starts worker and player with the saved .env.
func (s *Server) handleSettingsRestart(w http.ResponseWriter, r *http.Request) {
	if !s.settingsAllowed(w, r) {
		return
	}
	cmd := settings.RestartCommand(s.Cfg.Root)
	if cmd == nil {
		writeErr(w, 501, "no_launcher", "перезапуск отсюда недоступен — перезапусти сервер вручную")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(500 * time.Millisecond)
		if out := settings.OpenRestartLog(s.Cfg.Root); out != nil {
			_, _ = out.WriteString("\n--- restart " + time.Now().Format(time.RFC3339) + "\n")
			cmd.Stdout, cmd.Stderr = out, out
			defer out.Close() // the child keeps its own handle
		}
		if err := cmd.Start(); err != nil {
			log.Printf("restart: %v", err)
			return
		}
		log.Printf("restart: launcher pid=%d", cmd.Process.Pid)
		_ = cmd.Process.Release()
	}()
}
