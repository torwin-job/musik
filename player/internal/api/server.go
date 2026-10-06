package api

import (
	"net/http"
	"time"

	"github.com/torwin-job/musik/player/internal/app"
	"github.com/torwin-job/musik/player/internal/auth"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/media"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/queue"
	"github.com/torwin-job/musik/player/internal/taste"
)

const (
	Version    = "1.0.0"
	APIVersion = "v1"
)

type Server struct {
	Cfg     config.Config
	Store   *db.Store
	Idx     *index.Index
	Taste   *taste.Profile
	Builder *queue.Builder
	Play    *playback.Engine
	App     *app.Service
	Static  http.FileSystem
	HTTP    *http.Client
	Auth    *auth.Gate

	shareListeners int32
	loginLimiter   *auth.LoginLimiter
	latency        *latencyRecorder
	backgroundIO   chan func()

	Media *media.Service
}

type routeDescriptor struct {
	method  string
	path    string
	handler func(*Server, http.ResponseWriter, *http.Request)
}

var apiRoutes = []routeDescriptor{
	{"GET", "/api/health", (*Server).handleHealth},
	{"GET", "/api/themes", (*Server).handleThemes},
	{"GET", "/api/openapi.json", (*Server).handleOpenAPI},
	{"GET", "/api/auth/me", (*Server).handleAuthMe},
	{"POST", "/api/auth/login", (*Server).handleAuthLogin},
	{"POST", "/api/auth/logout", (*Server).handleAuthLogout},
	{"GET", "/api/status", (*Server).handleStatus},
	{"GET", "/api/profile", (*Server).handleProfile},
	{"PUT", "/api/profile/explore", (*Server).handleExploreBounds},
	{"GET", "/api/library", (*Server).handleLibrary},
	{"GET", "/api/artists", (*Server).handleArtists},
	{"GET", "/api/albums", (*Server).handleAlbums},
	{"GET", "/api/tracks/{id}", (*Server).handleTrack},
	{"GET", "/api/stream/{id}", (*Server).handleStream},
	{"GET", "/api/artwork/{id}", (*Server).handleArtwork},
	{"GET", "/api/similar/{id}", (*Server).handleSimilar},
	{"POST", "/api/reload", (*Server).handleReload},
	{"POST", "/api/session/start", (*Server).handleSessionStart},
	{"POST", "/api/session/jump", (*Server).handleSessionJump},
	{"POST", "/api/session/back", (*Server).handleSessionBack},
	{"POST", "/api/radio/start", (*Server).handleRadioStart},
	{"POST", "/api/share/radio", (*Server).handleShareRadioCreate},
	{"GET", "/api/share/radio", (*Server).handleShareRadioList},
	{"DELETE", "/api/share/radio/{token}", (*Server).handleShareRadioRevoke},
	{"GET", "/listen/{token}", (*Server).handleListenShare},
	{"POST", "/api/play", (*Server).handlePlay},
	{"POST", "/api/events", (*Server).handleEvents},
	{"GET", "/api/now", (*Server).handleNow},
	{"GET", "/api/playback/state", (*Server).handlePlaybackStateGet},
	{"PUT", "/api/playback/state", (*Server).handlePlaybackStatePut},
	{"GET", "/api/tracks/{id}/lyrics", (*Server).handleTrackLyrics},
	{"GET", "/api/mixes", (*Server).handleMixes},
	{"POST", "/api/mixes/{kind}/play", (*Server).handleMixPlay},
	{"GET", "/api/later", (*Server).handleLaterList},
	{"POST", "/api/later", (*Server).handleLaterAdd},
	{"DELETE", "/api/later", (*Server).handleLaterRemove},
	{"GET", "/api/favorites", (*Server).handleFavoritesList},
	{"POST", "/api/favorites", (*Server).handleFavoritesAdd},
	{"DELETE", "/api/favorites", (*Server).handleFavoritesRemove},
	{"POST", "/api/favorites/toggle", (*Server).handleFavoritesToggle},
	{"GET", "/api/favorites/status", (*Server).handleFavoritesStatus},
	{"GET", "/api/similar/artists", (*Server).handleSimilarArtists},
	{"GET", "/api/similar/albums", (*Server).handleSimilarAlbums},
	{"GET", "/api/recommend/favorites", (*Server).handleRecommendFavorites},
	{"GET", "/api/recommend/seed", (*Server).handleRecommendSeed},
	{"GET", "/api/discover/albums", (*Server).handleDiscoverAlbums},
	{"GET", "/api/discover/resurfaced", (*Server).handleDiscoverResurfaced},
	{"POST", "/api/library/upload", (*Server).handleLibraryUpload},
	{"POST", "/api/library/rescan", (*Server).handleLibraryRescan},
	{"POST", "/api/jobs/{kind}", (*Server).handleEnqueueJob},
	{"GET", "/api/jobs/{id}", (*Server).handleGetJob},
	{"GET", "/api/jobs", (*Server).handleListJobs},
	{"GET", "/api/metrics/weekly", (*Server).handleWeeklyMetrics},
	{"GET", "/api/metrics/recommendations", (*Server).handleRecommendationMetrics},
	{"GET", "/api/contexts", (*Server).handleContextsList},
	{"POST", "/api/contexts", (*Server).handleContextsCreate},
	{"GET", "/api/contexts/{id}", (*Server).handleContextGet},
	{"PATCH", "/api/contexts/{id}", (*Server).handleContextPatch},
	{"DELETE", "/api/contexts/{id}", (*Server).handleContextDelete},
	{"POST", "/api/contexts/{id}/activate", (*Server).handleContextActivate},
	{"POST", "/api/contexts/{id}/deactivate", (*Server).handleContextDeactivate},
	{"POST", "/api/session/contexts", (*Server).handleSessionContexts},
	{"GET", "/api/rules", (*Server).handleRulesList},
	{"POST", "/api/rules", (*Server).handleRulesCreate},
	{"PATCH", "/api/rules/{id}", (*Server).handleRulePatch},
	{"DELETE", "/api/rules/{id}", (*Server).handleRuleDelete},
	{"POST", "/api/rules/undo", (*Server).handleRulesUndo},
	{"GET", "/api/playlists", (*Server).handlePlaylistsList},
	{"POST", "/api/playlists", (*Server).handlePlaylistsCreate},
	{"POST", "/api/playlists/import", (*Server).handlePlaylistImport},
	{"POST", "/api/playlists/preview", (*Server).handlePlaylistPreview},
	{"POST", "/api/playlists/from-favorites", (*Server).handlePlaylistFromFavorites},
	{"POST", "/api/playlists/from-later", (*Server).handlePlaylistFromLater},
	{"GET", "/api/playlists/{id}", (*Server).handlePlaylistGet},
	{"PATCH", "/api/playlists/{id}", (*Server).handlePlaylistPatch},
	{"DELETE", "/api/playlists/{id}", (*Server).handlePlaylistDelete},
	{"POST", "/api/playlists/{id}/tracks", (*Server).handlePlaylistAddTrack},
	{"DELETE", "/api/playlists/{id}/tracks/{item_id}", (*Server).handlePlaylistRemoveTrack},
	{"POST", "/api/playlists/{id}/reorder", (*Server).handlePlaylistReorder},
	{"POST", "/api/playlists/{id}/duplicate", (*Server).handlePlaylistDuplicate},
	{"POST", "/api/playlists/{id}/from-queue", (*Server).handlePlaylistFromQueue},
	{"POST", "/api/playlists/{id}/play", (*Server).handlePlaylistPlay},
	{"POST", "/api/playlists/{id}/radio", (*Server).handlePlaylistRadio},
	{"GET", "/api/playlists/{id}/export", (*Server).handlePlaylistExport},
	{"GET", "/api/tags", (*Server).handleTagsList},
	{"POST", "/api/tags", (*Server).handleTagsCreate},
	{"POST", "/api/tags/{id}/tracks", (*Server).handleTagAddTrack},
	{"DELETE", "/api/tags/{id}/tracks", (*Server).handleTagRemoveTrack},
	{"GET", "/manifest.webmanifest", (*Server).handleManifest},
}

func New(cfg config.Config, store *db.Store, idx *index.Index, tp *taste.Profile, staticFS http.FileSystem) *Server {
	var secret []byte
	if cfg.SessionSecret != "" {
		secret = []byte(cfg.SessionSecret)
	}
	gate := auth.New(auth.Config{
		Password:      cfg.Password,
		APIToken:      cfg.APIToken,
		SessionSecret: secret,
		Disabled:      cfg.AuthDisabled,
		SecureCookie:  cfg.SecureCookie,
	})
	builder := queue.NewBuilder(idx, cfg)
	s := &Server{
		Cfg: cfg, Store: store, Idx: idx, Taste: tp,
		Builder:      builder,
		Play:         playback.New(cfg, store, idx, tp, builder),
		Static:       staticFS,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		Auth:         gate,
		loginLimiter: auth.NewLoginLimiter(5, time.Minute),
		latency:      newLatencyRecorder(),
		backgroundIO: make(chan func(), 256),
	}
	s.App = app.New(cfg, store, idx, tp, s.Play, s.HTTP)
	s.Media = media.New(cfg, idx)
	s.Play.Enqueue = s.enqueueBackgroundIO
	s.Play.Flush = s.flushBackgroundIO
	s.Play.Observe = func(op string, d time.Duration) { s.latency.Observe(op, d) }
	s.Play.Warm = s.Media.Warm
	go s.runBackgroundIO()
	return s
}

func (s *Server) runBackgroundIO() {
	for work := range s.backgroundIO {
		work()
	}
}

func (s *Server) enqueueBackgroundIO(work func()) {
	if work == nil {
		return
	}
	s.backgroundIO <- work
}

func (s *Server) flushBackgroundIO() {
	done := make(chan struct{})
	s.enqueueBackgroundIO(func() { close(done) })
	<-done
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, route := range apiRoutes {
		mux.HandleFunc(route.method+" "+route.path, func(w http.ResponseWriter, r *http.Request) {
			route.handler(s, w, r)
		})
	}
	mux.Handle("/", s.staticHandler())
	var h http.Handler = mux
	if s.Auth != nil {
		h = s.Auth.Middleware(h)
	}
	h = withGzip(h)
	return withCORS(h, s.Cfg.CORSOrigins)
}
