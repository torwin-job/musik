// Package api is the HTTP layer of musik-player: routes, JSON, cookies, and
// request/response mapping. Listening, catalog, and storage live next door:
//
//	internal/app        reload and worker process
//	internal/media      artwork, transcoding, stream warming
//	internal/library    upload, archives, artist/album grouping
//	internal/playback   sessions, radio and fixed queues, mix shelf contract
//	internal/recommend  similar tracks, artists, and albums
//	internal/index      shared catalog embeddings and per-profile counters
//	internal/queue      candidate scoring and queue assembly
//	internal/taste      profile vector, dayparts, centroids, maturity
//	internal/playlist   import/export and smart-playlist rule JSON
//	internal/rules      radio block, downrank, and cooldown
//	internal/db         SQLite and PostgreSQL, one Store per profile
//	internal/auth       password, Bearer, cookie, OIDC
//	internal/settings   install paths, external address, restart
//	internal/themes     UI theme catalog
//
// Files here are named after the HTTP surface:
//
//	server.go        Server, New, route table (public, user, profile, admin)
//	http.go          CORS, JSON helpers, static, health, PWA manifest
//	auth.go          login, logout, register, OIDC
//	accounts.go      users, invitations, password
//	multiuser.go     per-profile store, taste index, and engine
//	device_tokens.go phone and tablet tokens
//	profile.go       taste snapshot
//	explore.go       explore bounds
//	library.go       catalog list and reload
//	catalog.go       artists, albums, track JSON
//	upload.go        library upload
//	stream.go        original and mobile stream
//	artwork.go       covers
//	artist_photos.go artist images
//	play.go          POST /api/play
//	session.go       session start, now, jump, back
//	events.go        listen events
//	radio.go         personal radio start
//	share.go         share-radio tokens
//	listen.go        public MP3 radio stream
//	playback_state.go resume position
//	mixes.go         mix shelf and play
//	playlists.go     user and smart playlists
//	later.go         listen-later list
//	favorites.go     hearts
//	recommend.go     similar, seed, favorites mix
//	discover.go      album and resurfaced tips
//	contexts.go      taste contexts
//	rules.go         radio rules
//	blocks.go        hard-block filtering for generated lists
//	jobs.go          worker job proxy
//	plugins.go       plugin discovery, import tasks, progress, and cancel
//	worker.go        worker autostart
//	metrics.go       weekly stats, on-this-day, year in music, recommendation stats
//	lyrics.go        track lyrics
//	settings.go      install settings
//	dto.go           track JSON shared by handlers
//	openapi.go       embedded API document
//	latency.go       in-process endpoint timings
//	gzip.go          response compression
//
// HTTP tests live in internal/apitest, not here.
package api
