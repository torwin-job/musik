package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil || !s.Auth.Cfg.Enabled() {
		writeJSON(w, map[string]any{"ok": true, "auth": false, "hint": "auth disabled"})
		return
	}
	if s.loginLimiter != nil && !s.loginLimiter.Allow(r) {
		writeErr(w, 429, "rate_limited", "too many login attempts")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	if !s.Auth.CheckPassword(req.Password) {
		writeErr(w, 401, "bad_password", "неверный пароль")
		return
	}
	if err := s.Auth.IssueCookie(w); err != nil {
		writeErr(w, 500, "session", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthLogout(w http.ResponseWriter, _ *http.Request) {
	if s.Auth != nil {
		s.Auth.ClearCookie(w)
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	enabled := s.Auth != nil && s.Auth.Cfg.Enabled()
	ok := !enabled || (s.Auth != nil && s.Auth.Authorized(r))
	writeJSON(w, map[string]any{
		"ok": ok, "auth_enabled": enabled,
	})
}
