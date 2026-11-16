package api

import (
	"errors"
	"net/http"
	"sync/atomic"
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
	Cfg    config.Config
	Store  *db.Store
	Idx    *index.Index
	Taste  *taste.Profile
	Play   *playback.Engine
	App    *app.Service
	Static http.FileSystem
	HTTP   *http.Client
	Auth   *auth.Gate
	multi  *multiUserState

	listeners    *atomic.Int32
	loginLimiter *auth.LoginLimiter
	latency      *latencyRecorder
	backgroundIO chan func()

	Media *media.Service
}

// routeScope says who may call a route.
// public needs no session. user needs a session on this server.
// profile needs a session; the handler then sees that profile's store,
// taste index, and engine. admin needs an administrator on this server.
// GET /listen/{token} is public, and the share token picks the profile.
type routeScope string

const (
	scopePublic  routeScope = "public"
	scopeUser    routeScope = "user"
	scopeProfile routeScope = "profile"
	scopeAdmin   routeScope = "admin"
)

type routeDescriptor struct {
	method  string
	path    string
	scope   routeScope
	handler func(*Server, http.ResponseWriter, *http.Request)
}

var apiRoutes = []routeDescriptor{
	{"GET", "/api/health", scopePublic, (*Server).handleHealth},
	{"GET", "/api/themes", scopePublic, (*Server).handleThemes},
	{"GET", "/api/openapi.json", scopePublic, (*Server).handleOpenAPI},
	{"GET", "/api/auth/me", scopePublic, (*Server).handleAuthMe},
	{"POST", "/api/auth/login", scopePublic, (*Server).handleAuthLogin},
	{"POST", "/api/auth/register", scopePublic, (*Server).handleLocalRegister},
	{"POST", "/api/auth/token", scopePublic, (*Server).handleAuthToken},
	{"POST", "/api/account/password", scopeUser, (*Server).handlePasswordChange},
	{"POST", "/api/auth/logout", scopePublic, (*Server).handleAuthLogout},
	{"GET", "/api/auth/oidc/default/start", scopePublic, (*Server).handleOIDCStart},
	{"GET", "/api/auth/oidc/default/callback", scopePublic, (*Server).handleOIDCCallback},
	{"POST", "/api/auth/oidc/default/link", scopeUser, (*Server).handleOIDCLinkStart},
	{"GET", "/api/profiles", scopeUser, (*Server).handleProfiles},
	{"POST", "/api/profiles", scopeUser, (*Server).handleProfileCreate},
	{"PATCH", "/api/profiles/{profile_id}", scopeUser, (*Server).handleProfileRename},
	{"DELETE", "/api/profiles/{profile_id}", scopeUser, (*Server).handleProfileDelete},
	{"POST", "/api/profiles/{profile_id}/activate", scopeUser, (*Server).handleProfileActivate},
	{"GET", "/api/account/device-tokens", scopeUser, (*Server).handleDeviceTokens},
	{"POST", "/api/account/device-tokens", scopeUser, (*Server).handleDeviceTokenCreate},
	{"DELETE", "/api/account/device-tokens/{id}", scopeUser, (*Server).handleDeviceTokenRevoke},
	{"POST", "/api/account/device-tokens/{id}/replace", scopeUser, (*Server).handleDeviceTokenReplace},
	{"POST", "/api/admin/invitations", scopeAdmin, (*Server).handleInvitationCreate},
	{"GET", "/api/admin/invitations", scopeAdmin, (*Server).handleInvitations},
	{"GET", "/api/admin/invitations/{id}", scopeAdmin, (*Server).handleInvitations},
	{"DELETE", "/api/admin/invitations/{id}", scopeAdmin, (*Server).handleInvitationRevoke},
	{"GET", "/api/admin/users", scopeAdmin, (*Server).handleAccounts},
	{"PATCH", "/api/admin/users/{id}", scopeAdmin, (*Server).handleAccountUpdate},
	{"GET", "/api/status", scopeProfile, (*Server).handleStatus},
	{"GET", "/api/profile", scopeProfile, (*Server).handleProfile},
	{"PUT", "/api/profile/explore", scopeProfile, (*Server).handleExploreBounds},
	{"GET", "/api/library", scopeProfile, (*Server).handleLibrary},
	{"GET", "/api/library/map", scopeProfile, (*Server).handleLibraryMap},
	{"GET", "/api/artists", scopeProfile, (*Server).handleArtists},
	{"GET", "/api/albums", scopeProfile, (*Server).handleAlbums},
	{"GET", "/api/tracks/{id}", scopeProfile, (*Server).handleTrack},
	{"GET", "/api/stream/{id}", scopeProfile, (*Server).handleStream},
	{"GET", "/api/artwork/{id}", scopeProfile, (*Server).handleArtwork},
	{"GET", "/api/similar/{id}", scopeProfile, (*Server).handleSimilar},
	{"POST", "/api/reload", scopeAdmin, (*Server).handleReload},
	{"POST", "/api/session/start", scopeProfile, (*Server).handleSessionStart},
	{"POST", "/api/session/jump", scopeProfile, (*Server).handleSessionJump},
	{"POST", "/api/session/back", scopeProfile, (*Server).handleSessionBack},
	{"POST", "/api/radio/start", scopeProfile, (*Server).handleRadioStart},
	{"POST", "/api/share/radio", scopeProfile, (*Server).handleShareRadioCreate},
	{"GET", "/api/share/radio", scopeProfile, (*Server).handleShareRadioList},
	{"DELETE", "/api/share/radio/{token}", scopeProfile, (*Server).handleShareRadioRevoke},
	{"GET", "/listen/{token}", scopePublic, (*Server).handleListenShare},
	{"POST", "/api/play", scopeProfile, (*Server).handlePlay},
	{"POST", "/api/events", scopeProfile, (*Server).handleEvents},
	{"GET", "/api/now", scopeProfile, (*Server).handleNow},
	{"GET", "/api/playback/state", scopeProfile, (*Server).handlePlaybackStateGet},
	{"GET", "/api/artist-photos", scopeProfile, (*Server).handleArtistPhotos},
	{"GET", "/api/settings", scopeAdmin, (*Server).handleSettingsGet},
	{"PUT", "/api/settings", scopeAdmin, (*Server).handleSettingsPut},
	{"GET", "/api/settings/dirs", scopeAdmin, (*Server).handleSettingsDirs},
	{"GET", "/api/settings/external-ip", scopeAdmin, (*Server).handleSettingsExternalIP},
	{"GET", "/api/settings/qr", scopeAdmin, (*Server).handleSettingsQR},
	{"POST", "/api/settings/restart", scopeAdmin, (*Server).handleSettingsRestart},
	{"GET", "/api/artist-photos/{id}", scopeProfile, (*Server).handleArtistPhoto},
	{"PUT", "/api/playback/state", scopeProfile, (*Server).handlePlaybackStatePut},
	{"GET", "/api/tracks/{id}/lyrics", scopeProfile, (*Server).handleTrackLyrics},
	{"GET", "/api/mixes", scopeProfile, (*Server).handleMixes},
	{"POST", "/api/mixes/{kind}/play", scopeProfile, (*Server).handleMixPlay},
	{"GET", "/api/later", scopeProfile, (*Server).handleLaterList},
	{"POST", "/api/later", scopeProfile, (*Server).handleLaterAdd},
	{"DELETE", "/api/later", scopeProfile, (*Server).handleLaterRemove},
	{"GET", "/api/favorites", scopeProfile, (*Server).handleFavoritesList},
	{"POST", "/api/favorites", scopeProfile, (*Server).handleFavoritesAdd},
	{"DELETE", "/api/favorites", scopeProfile, (*Server).handleFavoritesRemove},
	{"POST", "/api/favorites/toggle", scopeProfile, (*Server).handleFavoritesToggle},
	{"GET", "/api/favorites/status", scopeProfile, (*Server).handleFavoritesStatus},
	{"GET", "/api/similar/artists", scopeProfile, (*Server).handleSimilarArtists},
	{"GET", "/api/similar/albums", scopeProfile, (*Server).handleSimilarAlbums},
	{"GET", "/api/recommend/favorites", scopeProfile, (*Server).handleRecommendFavorites},
	{"GET", "/api/recommend/seed", scopeProfile, (*Server).handleRecommendSeed},
	{"GET", "/api/discover/albums", scopeProfile, (*Server).handleDiscoverAlbums},
	{"GET", "/api/discover/resurfaced", scopeProfile, (*Server).handleDiscoverResurfaced},
	{"POST", "/api/library/upload", scopeAdmin, (*Server).handleLibraryUpload},
	{"POST", "/api/library/rescan", scopeAdmin, (*Server).handleLibraryRescan},
	{"POST", "/api/jobs/{kind}", scopeAdmin, (*Server).handleEnqueueJob},
	{"GET", "/api/jobs/{id}", scopeAdmin, (*Server).handleGetJob},
	{"GET", "/api/jobs", scopeAdmin, (*Server).handleListJobs},
	{"GET", "/api/plugins", scopeAdmin, (*Server).handlePlugins},
	{"POST", "/api/plugins/{id}/import", scopeAdmin, (*Server).handlePluginImport},
	{"POST", "/api/plugins/{id}/search", scopeAdmin, (*Server).handlePluginSearch},
	{"GET", "/api/plugin-jobs", scopeAdmin, (*Server).handleListPluginJobs},
	{"GET", "/api/plugin-jobs/{id}", scopeAdmin, (*Server).handleGetPluginJob},
	{"POST", "/api/plugin-jobs/{id}/cancel", scopeAdmin, (*Server).handleCancelPluginJob},
	{"GET", "/api/metrics/weekly", scopeProfile, (*Server).handleWeeklyMetrics},
	{"GET", "/api/metrics/listening", scopeProfile, (*Server).handleListeningMemory},
	{"GET", "/api/metrics/recommendations", scopeProfile, (*Server).handleRecommendationMetrics},
	{"GET", "/api/contexts", scopeProfile, (*Server).handleContextsList},
	{"POST", "/api/contexts", scopeProfile, (*Server).handleContextsCreate},
	{"GET", "/api/contexts/{id}", scopeProfile, (*Server).handleContextGet},
	{"PATCH", "/api/contexts/{id}", scopeProfile, (*Server).handleContextPatch},
	{"DELETE", "/api/contexts/{id}", scopeProfile, (*Server).handleContextDelete},
	{"POST", "/api/contexts/{id}/activate", scopeProfile, (*Server).handleContextActivate},
	{"POST", "/api/contexts/{id}/deactivate", scopeProfile, (*Server).handleContextDeactivate},
	{"POST", "/api/session/contexts", scopeProfile, (*Server).handleSessionContexts},
	{"GET", "/api/rules", scopeProfile, (*Server).handleRulesList},
	{"POST", "/api/rules", scopeProfile, (*Server).handleRulesCreate},
	{"PATCH", "/api/rules/{id}", scopeProfile, (*Server).handleRulePatch},
	{"DELETE", "/api/rules/{id}", scopeProfile, (*Server).handleRuleDelete},
	{"POST", "/api/rules/undo", scopeProfile, (*Server).handleRulesUndo},
	{"GET", "/api/playlists", scopeProfile, (*Server).handlePlaylistsList},
	{"POST", "/api/playlists", scopeProfile, (*Server).handlePlaylistsCreate},
	{"POST", "/api/playlists/import", scopeProfile, (*Server).handlePlaylistImport},
	{"POST", "/api/playlists/preview", scopeProfile, (*Server).handlePlaylistPreview},
	{"POST", "/api/playlists/from-favorites", scopeProfile, (*Server).handlePlaylistFromFavorites},
	{"POST", "/api/playlists/from-later", scopeProfile, (*Server).handlePlaylistFromLater},
	{"GET", "/api/playlists/{id}", scopeProfile, (*Server).handlePlaylistGet},
	{"PATCH", "/api/playlists/{id}", scopeProfile, (*Server).handlePlaylistPatch},
	{"DELETE", "/api/playlists/{id}", scopeProfile, (*Server).handlePlaylistDelete},
	{"POST", "/api/playlists/{id}/tracks", scopeProfile, (*Server).handlePlaylistAddTrack},
	{"DELETE", "/api/playlists/{id}/tracks/{item_id}", scopeProfile, (*Server).handlePlaylistRemoveTrack},
	{"POST", "/api/playlists/{id}/reorder", scopeProfile, (*Server).handlePlaylistReorder},
	{"POST", "/api/playlists/{id}/duplicate", scopeProfile, (*Server).handlePlaylistDuplicate},
	{"POST", "/api/playlists/{id}/from-queue", scopeProfile, (*Server).handlePlaylistFromQueue},
	{"POST", "/api/playlists/{id}/play", scopeProfile, (*Server).handlePlaylistPlay},
	{"POST", "/api/playlists/{id}/radio", scopeProfile, (*Server).handlePlaylistRadio},
	{"GET", "/api/playlists/{id}/export", scopeProfile, (*Server).handlePlaylistExport},
	{"GET", "/api/tags", scopeProfile, (*Server).handleTagsList},
	{"POST", "/api/tags", scopeProfile, (*Server).handleTagsCreate},
	{"POST", "/api/tags/{id}/tracks", scopeProfile, (*Server).handleTagAddTrack},
	{"DELETE", "/api/tags/{id}/tracks", scopeProfile, (*Server).handleTagRemoveTrack},
	{"GET", "/manifest.webmanifest", scopePublic, (*Server).handleManifest},
}

// routeMux answers "which table row is this request?" for middleware.
// Handlers close over their own row and pass it to serveTarget.
var (
	routesByPattern map[string]routeDescriptor
	routeMux        = http.NewServeMux()
)

func init() {
	routesByPattern = make(map[string]routeDescriptor, len(apiRoutes))
	for _, route := range apiRoutes {
		if route.scope == "" {
			panic("api route missing scope: " + route.method + " " + route.path)
		}
		pattern := route.method + " " + route.path
		if _, exists := routesByPattern[pattern]; exists {
			panic("duplicate api route: " + pattern)
		}
		routesByPattern[pattern] = route
		routeMux.HandleFunc(pattern, func(http.ResponseWriter, *http.Request) {})
	}
}

func matchRoute(r *http.Request) (routeDescriptor, bool) {
	_, pattern := routeMux.Handler(r)
	route, ok := routesByPattern[pattern]
	return route, ok
}

// singleUserOpen is the single-user gate. Public routes come from the table.
// Reload stays open here because the worker calls it; the handler still
// requires loopback or a bearer.
func singleUserOpen(r *http.Request) bool {
	if r.Method == http.MethodPost && r.URL.Path == "/api/reload" {
		return true
	}
	route, ok := matchRoute(r)
	return ok && route.scope == scopePublic
}

// profileRuntime reports routes whose handler must see that listener's store,
// taste index, and playback engine. Share links are public, but the token names a profile.
func (route routeDescriptor) profileRuntime() bool {
	return route.scope == scopeProfile || route.path == "/listen/{token}"
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
	s := &Server{
		Cfg:          cfg,
		Store:        store,
		Static:       staticFS,
		HTTP:         &http.Client{Timeout: 30 * time.Second},
		Auth:         gate,
		listeners:    &atomic.Int32{},
		loginLimiter: auth.NewLoginLimiter(5, time.Minute),
		latency:      newLatencyRecorder(),
		backgroundIO: make(chan func(), 256),
	}
	// Multi-user keeps one server for routes, auth, and media. Taste indexes
	// and engines belong to profiles and are opened on demand.
	if cfg.MultiUser {
		s.App = app.New(cfg, store, nil, nil, nil, s.HTTP)
		s.bindPluginRescan()
		s.Media = media.NewCatalog(cfg, s.App.Catalog)
		go s.runBackgroundIO()
		return s
	}
	builder := queue.NewBuilder(idx, cfg)
	s.Idx = idx
	s.Taste = tp
	s.Play = playback.New(cfg, store, idx, tp, builder)
	s.App = app.New(cfg, store, idx, tp, s.Play, s.HTTP)
	s.bindPluginRescan()
	s.Media = media.New(cfg, idx)
	s.wire(s.Play)
	go s.runBackgroundIO()
	return s
}

func (s *Server) bindPluginRescan() {
	if s.App == nil || s.App.Plugins == nil {
		return
	}
	s.App.Plugins.SetCatalogRescan(s.enqueueFullRescan)
}

func (s *Server) wire(play *playback.Engine) {
	if play == nil {
		return
	}
	play.Enqueue = s.enqueueBackgroundIO
	play.Flush = s.flushBackgroundIO
	play.Observe = func(op string, d time.Duration) { s.latency.Observe(op, d) }
	if s.Media != nil {
		play.Warm = s.Media.Warm
	}
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
			// A hot standby rejects every write; fail clearly instead of
			// surfacing PostgreSQL's read-only transaction error as a 500.
			if s.Store.ReadOnly && route.method != http.MethodGet {
				w.Header().Set("Retry-After", "30")
				writeErr(w, 503, "read_only_replica", "this server is a read-only replica")
				return
			}
			server, release, err := s.serveTarget(r, route)
			if err != nil {
				if errors.Is(err, db.ErrProfileNotFound) {
					writeErr(w, 404, "profile_not_found", "profile not found")
				} else {
					writeErr(w, 500, "profile", "profile unavailable")
				}
				return
			}
			if release != nil {
				defer release()
			}
			route.handler(server, w, r)
		})
	}
	mux.Handle("/", s.staticHandler())
	var h http.Handler = mux
	if s.Cfg.MultiUser {
		h = s.multiUserMiddleware(h)
	} else if s.Auth != nil {
		h = s.Auth.Middleware(h, singleUserOpen)
	}
	h = withGzip(h)
	return withCORS(h, s.Cfg.CORSOrigins)
}
