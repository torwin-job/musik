package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticEditionCookieSelectsPage(t *testing.T) {
	server := &Server{Static: http.FS(fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("original interface")},
		"mozgolika.html": &fstest.MapFile{Data: []byte("mozgolika interface")},
		"rainedev.html":  &fstest.MapFile{Data: []byte("rainedev interface")},
	})}
	for _, tc := range []struct{ cookie, want string }{
		{"", "original interface"},
		{"mozgolika", "mozgolika interface"},
		{"rainedev", "rainedev interface"},
		{"original", "original interface"},
		{"../../etc/passwd", "original interface"},
	} {
		t.Run(tc.cookie, func(t *testing.T) {
			request := httptest.NewRequest("GET", "/", nil)
			if tc.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "musik_ui", Value: tc.cookie})
			}
			response := httptest.NewRecorder()
			server.staticHandler().ServeHTTP(response, request)
			if response.Code != 200 || response.Body.String() != tc.want {
				t.Fatalf("status=%d body=%q want=%q", response.Code, response.Body.String(), tc.want)
			}
			if !strings.Contains(response.Header().Get("Vary"), "Cookie") {
				t.Fatal("edition responses must vary on Cookie")
			}
			if response.Header().Get("Cache-Control") != "no-cache" {
				t.Fatal("edition must revalidate")
			}
		})
	}
}

func TestStaticEditionEnvironmentDefault(t *testing.T) {
	server := &Server{Static: http.FS(fstest.MapFS{
		"index.html":     &fstest.MapFile{Data: []byte("original")},
		"mozgolika.html": &fstest.MapFile{Data: []byte("mozgolika")},
	})}
	server.Cfg.UI = "mozgolika"
	response := httptest.NewRecorder()
	server.staticHandler().ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Body.String() != "mozgolika" {
		t.Fatalf("default body=%q", response.Body.String())
	}
}

func TestRaineDevEnvironmentDefaultAndCookieOverride(t *testing.T) {
	server := &Server{Static: http.FS(fstest.MapFS{
		"index.html":    &fstest.MapFile{Data: []byte("original")},
		"rainedev.html": &fstest.MapFile{Data: []byte("RaineDev")},
	})}
	server.Cfg.UI = "rainedev"
	for _, tc := range []struct{ cookie, want string }{{"", "RaineDev"}, {"original", "original"}} {
		req := httptest.NewRequest("GET", "/", nil)
		if tc.cookie != "" {
			req.AddCookie(&http.Cookie{Name: "musik_ui", Value: tc.cookie})
		}
		w := httptest.NewRecorder()
		server.staticHandler().ServeHTTP(w, req)
		if w.Code != 200 || w.Body.String() != tc.want {
			t.Fatalf("cookie=%q code=%d body=%q", tc.cookie, w.Code, w.Body.String())
		}
	}
}
