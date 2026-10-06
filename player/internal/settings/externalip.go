package settings

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ExternalIPSources answer with this host's public IP as plain text. The IP
// is only visible from outside, so one of them has to be asked.
var ExternalIPSources = []string{
	"https://api.ipify.org",
	"https://ifconfig.me/ip",
	"https://icanhazip.com",
}

const externalIPTTL = 10 * time.Minute

type ExternalIP struct {
	IP        string    `json:"ip"`
	Source    string    `json:"source"`
	CheckedAt time.Time `json:"checked_at"`
}

var (
	externalMu    sync.Mutex
	externalCache ExternalIP
)

// LookupExternalIP returns the public IP, cached for ten minutes so opening
// the settings page does not ask a third-party service every time.
func LookupExternalIP(ctx context.Context, client *http.Client, refresh bool) (ExternalIP, error) {
	externalMu.Lock()
	defer externalMu.Unlock()
	if !refresh && externalCache.IP != "" && time.Since(externalCache.CheckedAt) < externalIPTTL {
		return externalCache, nil
	}
	var lastErr error
	for _, src := range ExternalIPSources {
		ip, err := fetchIP(ctx, client, src)
		if err != nil {
			lastErr = err
			continue
		}
		externalCache = ExternalIP{IP: ip, Source: src, CheckedAt: time.Now().UTC()}
		return externalCache, nil
	}
	if lastErr == nil {
		lastErr = errors.New("no source")
	}
	return ExternalIP{}, lastErr
}

func fetchIP(ctx context.Context, client *http.Client, src string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "musik (self-hosted)")
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", errors.New(src + ": " + res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 64))
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(string(body))
	if net.ParseIP(ip) == nil {
		return "", errors.New(src + ": not an IP")
	}
	return ip, nil
}
