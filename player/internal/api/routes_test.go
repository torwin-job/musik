package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

func TestRouteScopesAreExplicit(t *testing.T) {
	nonProfile := map[string]routeScope{
		"GET /api/health":                              scopePublic,
		"GET /api/themes":                              scopePublic,
		"GET /api/openapi.json":                        scopePublic,
		"GET /api/auth/me":                             scopePublic,
		"POST /api/auth/login":                         scopePublic,
		"POST /api/auth/register":                      scopePublic,
		"POST /api/auth/token":                         scopePublic,
		"POST /api/auth/logout":                        scopePublic,
		"GET /api/auth/oidc/default/start":             scopePublic,
		"GET /api/auth/oidc/default/callback":          scopePublic,
		"GET /listen/{token}":                          scopePublic,
		"GET /manifest.webmanifest":                    scopePublic,
		"POST /api/account/password":                   scopeUser,
		"POST /api/auth/oidc/default/link":             scopeUser,
		"GET /api/profiles":                            scopeUser,
		"POST /api/profiles":                           scopeUser,
		"PATCH /api/profiles/{profile_id}":             scopeUser,
		"DELETE /api/profiles/{profile_id}":            scopeUser,
		"POST /api/profiles/{profile_id}/activate":     scopeUser,
		"GET /api/account/device-tokens":               scopeUser,
		"POST /api/account/device-tokens":              scopeUser,
		"DELETE /api/account/device-tokens/{id}":       scopeUser,
		"POST /api/account/device-tokens/{id}/replace": scopeUser,
		"POST /api/admin/invitations":                  scopeAdmin,
		"GET /api/admin/invitations":                   scopeAdmin,
		"GET /api/admin/invitations/{id}":              scopeAdmin,
		"DELETE /api/admin/invitations/{id}":           scopeAdmin,
		"GET /api/admin/users":                         scopeAdmin,
		"PATCH /api/admin/users/{id}":                  scopeAdmin,
		"POST /api/reload":                             scopeAdmin,
		"GET /api/settings":                            scopeAdmin,
		"PUT /api/settings":                            scopeAdmin,
		"GET /api/settings/dirs":                       scopeAdmin,
		"GET /api/settings/external-ip":                scopeAdmin,
		"GET /api/settings/qr":                         scopeAdmin,
		"POST /api/settings/restart":                   scopeAdmin,
		"POST /api/library/upload":                     scopeAdmin,
		"POST /api/library/rescan":                     scopeAdmin,
		"POST /api/jobs/{kind}":                        scopeAdmin,
		"GET /api/jobs/{id}":                           scopeAdmin,
		"GET /api/jobs":                                scopeAdmin,
		"GET /api/plugins":                             scopeAdmin,
		"POST /api/plugins/{id}/import":                scopeAdmin,
		"POST /api/plugins/{id}/search":                scopeAdmin,
		"GET /api/plugin-jobs":                         scopeAdmin,
		"GET /api/plugin-jobs/{id}":                    scopeAdmin,
		"POST /api/plugin-jobs/{id}/cancel":            scopeAdmin,
	}
	seen := map[string]struct{}{}
	for _, route := range apiRoutes {
		key := route.method + " " + route.path
		if _, dup := seen[key]; dup {
			t.Errorf("duplicate route %s", key)
		}
		seen[key] = struct{}{}
		if route.scope == "" {
			t.Errorf("%s has no scope", key)
		}
		want, special := nonProfile[key]
		if special {
			if route.scope != want {
				t.Errorf("%s scope %s, want %s", key, route.scope, want)
			}
			delete(nonProfile, key)
			continue
		}
		if route.scope != scopeProfile {
			t.Errorf("%s scope %s, want profile", key, route.scope)
		}
	}
	for key := range nonProfile {
		t.Errorf("scope table lists unknown route %s", key)
	}
}

func TestMatchRouteAgreesWithTable(t *testing.T) {
	for _, route := range apiRoutes {
		path := route.path
		for {
			start := strings.Index(path, "{")
			if start < 0 {
				break
			}
			end := strings.Index(path[start:], "}")
			if end < 0 {
				t.Fatalf("unclosed wildcard in %s", route.path)
			}
			path = path[:start] + "1" + path[start+end+1:]
		}
		req := httptest.NewRequest(route.method, path, nil)
		got, ok := matchRoute(req)
		if !ok || got.method != route.method || got.path != route.path || got.scope != route.scope {
			t.Fatalf("match %s %s -> ok=%v path=%s scope=%s", route.method, path, ok, got.path, got.scope)
		}
		wantOpen := route.scope == scopePublic || (route.method == http.MethodPost && route.path == "/api/reload")
		if singleUserOpen(req) != wantOpen {
			t.Errorf("%s %s single-user open=%v, want %v", route.method, path, singleUserOpen(req), wantOpen)
		}
	}
	if _, ok := matchRoute(httptest.NewRequest(http.MethodGet, "/app.js", nil)); ok {
		t.Fatal("static asset matched an api route")
	}
}

func TestServeTargetFollowsRouteScope(t *testing.T) {
	listening := &profileState{store: &db.Store{ProfileID: "p1"}}
	server := &Server{
		Cfg:   config.Config{MultiUser: true},
		Auth:  &auth.Gate{},
		multi: &multiUserState{profiles: map[string]*profileState{"p1": listening}},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	for _, route := range []routeDescriptor{
		{method: "GET", path: "/api/health", scope: scopePublic},
		{method: "GET", path: "/api/profiles", scope: scopeUser},
		{method: "GET", path: "/api/settings", scope: scopeAdmin},
	} {
		got, release, err := server.serveTarget(req, route)
		if err != nil || got != server || release != nil {
			t.Fatalf("%s %s left the installation server: %v %v", route.method, route.path, got == server, err)
		}
	}

	_, _, err := server.serveTarget(req, routeDescriptor{method: "GET", path: "/api/favorites", scope: scopeProfile})
	if !errors.Is(err, db.ErrProfileNotFound) {
		t.Fatalf("profile route without a session: %v", err)
	}

	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, &principal{Profile: db.Profile{ID: "p1"}}))
	got, release, err := server.serveTarget(req, routeDescriptor{method: "GET", path: "/api/favorites", scope: scopeProfile})
	if err != nil || got == server || got.Store != listening.store || got.Auth != server.Auth {
		t.Fatalf("profile view store=%v auth shared=%v err=%v", got != nil && got.Store == listening.store, got != nil && got.Auth == server.Auth, err)
	}
	release()
	if listening.refs.Load() != 0 {
		t.Fatalf("profile refs=%d", listening.refs.Load())
	}
}
