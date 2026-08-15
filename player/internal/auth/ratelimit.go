package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// LoginLimiter caps password attempts per client IP.
type LoginLimiter struct {
	mu      sync.Mutex
	hits    map[string][]time.Time
	limit   int
	window  time.Duration
}

func NewLoginLimiter(limit int, window time.Duration) *LoginLimiter {
	if limit < 1 {
		limit = 5
	}
	if window <= 0 {
		window = time.Minute
	}
	return &LoginLimiter{
		hits:   map[string][]time.Time{},
		limit:  limit,
		window: window,
	}
}

func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// first hop only
		if i := len(xff); i > 0 {
			for j := 0; j < len(xff); j++ {
				if xff[j] == ',' {
					return trimSpace(xff[:j])
				}
			}
			return trimSpace(xff)
		}
	}
	return host
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

// Allow returns false if the IP is over the limit.
func (l *LoginLimiter) Allow(r *http.Request) bool {
	if l == nil {
		return true
	}
	ip := clientIP(r)
	now := time.Now()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	arr := l.hits[ip]
	kept := arr[:0]
	for _, t := range arr {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[ip] = kept
		return false
	}
	kept = append(kept, now)
	l.hits[ip] = kept
	return true
}
