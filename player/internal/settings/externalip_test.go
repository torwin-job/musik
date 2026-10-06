package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLookupExternalIPFallsBackAndCaches(t *testing.T) {
	calls := 0
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("<html>not an ip</html>"))
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		_, _ = w.Write([]byte("203.0.113.7\n"))
	}))
	defer good.Close()

	saved := ExternalIPSources
	ExternalIPSources = []string{bad.URL, good.URL}
	externalCache = ExternalIP{}
	t.Cleanup(func() { ExternalIPSources = saved; externalCache = ExternalIP{} })

	got, err := LookupExternalIP(context.Background(), http.DefaultClient, false)
	if err != nil || got.IP != "203.0.113.7" || got.Source != good.URL {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, err := LookupExternalIP(context.Background(), http.DefaultClient, false); err != nil || calls != 2 {
		t.Fatalf("cached lookup asked again: calls=%d err=%v", calls, err)
	}
	if _, err := LookupExternalIP(context.Background(), http.DefaultClient, true); err != nil || calls != 4 {
		t.Fatalf("refresh did not ask again: calls=%d", calls)
	}
}
