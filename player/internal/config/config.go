package config

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	DBPath               string
	Library              string
	Addr                 string
	ExploreRatio         float64
	DiscoverExploreRatio float64
	ProfileReadyAt       int
	ProfileFormingAt     int
	NewTrackDays         int
	QueueSize            int
	TasteAlpha           float64
	NewBoostBeta         float64
	NewBoostTauDays      float64
	NewBoostGamma        float64
	WorkerURL            string
	WorkerAutostart      bool
	NewAlbumDays         int
	Password             string
	APIToken             string
	SessionSecret        string
	AuthDisabled         bool
	SecureCookie         bool
	PublicBaseURL        string
	FFmpegPath           string
	ShareBitrate         string
	ShareMaxListeners    int
	MobileBitrate        string // e.g. 160k — Android / LTE stream profile
	MobileFormat         string // aac | mp3
	CORSOrigins          []string
	CandidatePoolAt      int // N at which queue uses shortlist (default 8000)
}

func Load() Config {
	root := findRoot()
	db := env("MUSIK_DB_PATH", filepath.Join(root, "data", "db", "musik.db"))
	return Config{
		DBPath:               db,
		Library:              env("MUSIK_LIBRARY", filepath.Join(root, "data", "music")),
		Addr:                 env("MUSIK_PLAYER_ADDR", ":8787"),
		ExploreRatio:         envFloat("MUSIK_EXPLORE_RATIO", 0.15),
		DiscoverExploreRatio: envFloat("MUSIK_DISCOVER_EXPLORE", 0.35),
		ProfileReadyAt:       envInt("MUSIK_PROFILE_READY_AT", 8),
		ProfileFormingAt:     envInt("MUSIK_PROFILE_FORMING_AT", 3),
		NewTrackDays:         envInt("MUSIK_NEW_TRACK_DAYS", 14),
		QueueSize:            envInt("MUSIK_QUEUE_SIZE", 6),
		TasteAlpha:           envFloat("MUSIK_TASTE_ALPHA", 0.22),
		NewBoostBeta:         envFloat("MUSIK_NEW_BOOST_BETA", 0.25),
		NewBoostTauDays:      envFloat("MUSIK_NEW_BOOST_TAU", 14),
		NewBoostGamma:        envFloat("MUSIK_NEW_BOOST_GAMMA", 5),
		WorkerURL:            env("MUSIK_WORKER_URL", "http://127.0.0.1:8790"),
		WorkerAutostart:      envBool("MUSIK_WORKER_AUTOSTART", true),
		NewAlbumDays:         envInt("MUSIK_NEW_ALBUM_DAYS", 14),
		Password:             env("MUSIK_PASSWORD", ""),
		APIToken:             env("MUSIK_API_TOKEN", ""),
		SessionSecret:        env("MUSIK_SESSION_SECRET", ""),
		AuthDisabled:         envBool("MUSIK_AUTH_DISABLED", false),
		SecureCookie:         envBool("MUSIK_SECURE_COOKIE", false),
		PublicBaseURL:        stringsTrimRightSlash(env("MUSIK_PUBLIC_BASE_URL", "")),
		FFmpegPath:           env("MUSIK_FFMPEG", "ffmpeg"),
		ShareBitrate:         env("MUSIK_SHARE_BITRATE", "192k"),
		ShareMaxListeners:    envInt("MUSIK_SHARE_MAX_LISTENERS", 4),
		MobileBitrate:        env("MUSIK_MOBILE_BITRATE", "160k"),
		MobileFormat:         stringsToLower(env("MUSIK_MOBILE_FORMAT", "aac")),
		CORSOrigins:          splitCSV(env("MUSIK_CORS_ORIGINS", "")),
		CandidatePoolAt:      envInt("MUSIK_CANDIDATE_POOL_AT", 8000),
	}
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := s[start:i]
			for len(part) > 0 && (part[0] == ' ' || part[0] == '\t') {
				part = part[1:]
			}
			for len(part) > 0 && (part[len(part)-1] == ' ' || part[len(part)-1] == '\t') {
				part = part[:len(part)-1]
			}
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func stringsTrimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func (c Config) AuthEnabled() bool {
	if c.AuthDisabled {
		return false
	}
	return c.Password != "" || c.APIToken != ""
}

func findRoot() string {
	if r := os.Getenv("MUSIK_ROOT"); r != "" {
		return r
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := cwd
	for i := 0; i < 6; i++ {
		candidate := filepath.Join(dir, "data", "db", "musik.db")
		if _, err := os.Stat(candidate); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return cwd
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envFloat(k string, def float64) float64 {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	case "0", "false", "FALSE", "no", "off":
		return false
	default:
		return def
	}
}
