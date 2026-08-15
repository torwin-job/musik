package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const CookieName = "musik_session"

type Config struct {
	Password      string
	APIToken      string
	SessionSecret []byte
	TTL           time.Duration
	SecureCookie  bool
	Disabled      bool
}

func (c Config) Enabled() bool {
	if c.Disabled {
		return false
	}
	return c.Password != "" || c.APIToken != ""
}

type Gate struct {
	Cfg Config
}

func New(cfg Config) *Gate {
	if len(cfg.SessionSecret) < 16 {
		cfg.SessionSecret = deriveSecret(cfg.Password, cfg.APIToken)
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 14 * 24 * time.Hour
	}
	return &Gate{Cfg: cfg}
}

func deriveSecret(password, token string) []byte {
	h := sha256.Sum256([]byte("musik-session|" + password + "|" + token))
	return h[:]
}

type sessionPayload struct {
	Exp int64  `json:"exp"`
	Nbf int64  `json:"nbf"`
	Jti string `json:"jti"`
}

func (g *Gate) IssueCookie(w http.ResponseWriter) error {
	jti := make([]byte, 8)
	_, _ = rand.Read(jti)
	now := time.Now()
	p := sessionPayload{
		Exp: now.Add(g.Cfg.TTL).Unix(),
		Nbf: now.Add(-time.Minute).Unix(),
		Jti: hex.EncodeToString(jti),
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	sig := g.sign(raw)
	val := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig)
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    val,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   g.Cfg.SecureCookie,
		MaxAge:   int(g.Cfg.TTL.Seconds()),
	})
	return nil
}

func (g *Gate) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func (g *Gate) sign(raw []byte) []byte {
	m := hmac.New(sha256.New, g.Cfg.SessionSecret)
	_, _ = m.Write(raw)
	return m.Sum(nil)
}

func (g *Gate) validCookie(r *http.Request) bool {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return false
	}
	parts := strings.Split(c.Value, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	if !hmac.Equal(sig, g.sign(raw)) {
		return false
	}
	var p sessionPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return false
	}
	now := time.Now().Unix()
	return now >= p.Nbf && now <= p.Exp
}

func (g *Gate) validBearer(r *http.Request) bool {
	if g.Cfg.APIToken == "" {
		return false
	}
	h := r.Header.Get("Authorization")
	if h == "" {
		return false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h, prefix))
	return hmac.Equal([]byte(tok), []byte(g.Cfg.APIToken))
}

func (g *Gate) CheckPassword(pw string) bool {
	if g.Cfg.Password == "" {
		return false
	}
	return hmac.Equal([]byte(pw), []byte(g.Cfg.Password))
}

func (g *Gate) Authorized(r *http.Request) bool {
	if !g.Cfg.Enabled() {
		return true
	}
	return g.validCookie(r) || g.validBearer(r)
}

func (g *Gate) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if !g.Cfg.Enabled() || publicPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		// static assets + login page HTML
		if isStaticAsset(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if g.Authorized(r) {
			next.ServeHTTP(w, r)
			return
		}
		// HTML navigations → still serve index (JS shows login); API/stream → 401
		if wantsJSON(r) || isProtectedMedia(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized","code":"auth_required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func publicPath(r *http.Request) bool {
	p := r.URL.Path
	if r.Method == http.MethodPost && p == "/api/auth/login" {
		return true
	}
	// Worker reload callback: handler still enforces loopback or Bearer.
	if r.Method == http.MethodPost && p == "/api/reload" {
		return true
	}
	if r.Method == http.MethodGet {
		switch p {
		case "/api/health", "/api/openapi.json", "/api/auth/me", "/manifest.webmanifest":
			return true
		}
		// Share radio stream: token in path is the credential.
		if strings.HasPrefix(p, "/listen/") {
			return true
		}
	}
	return false
}

func isStaticAsset(p string) bool {
	return p == "/app.js" || p == "/style.css" || p == "/favicon.ico" ||
		strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".css") ||
		strings.HasSuffix(p, ".webmanifest") || strings.HasSuffix(p, ".map")
}

func isProtectedMedia(p string) bool {
	return strings.HasPrefix(p, "/api/stream/") || strings.HasPrefix(p, "/api/artwork/") ||
		strings.HasPrefix(p, "/api/")
}

func wantsJSON(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "application/json")
}
