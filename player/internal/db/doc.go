// Package db is the player's store for SQLite and PostgreSQL. One Store is a
// profile-scoped view of a shared pool. Files are split by table:
//
//	store.go            Open, schema check, Store, TrackRow
//	sqlx.go             pool, placeholders, transactions
//	models.go           sqlx row types for users, jobs, tokens
//	identity.go         users, profiles, installation profile
//	accounts.go         passwords, roles, account admin
//	invitations.go      invite codes
//	device_tokens.go    device credentials
//	tracks.go           catalog rows, embeddings, per-profile stats, paths
//	passport.go         encoding and audio features of one track
//	artist_photos.go    artist image rows
//	history.go          listens, transitions, rec_stats, impressions
//	profile.go          taste snapshots and top artists/clusters
//	playlists.go        generated mixes
//	user_playlists.go   user and smart playlists
//	smart.go            smart-playlist rule SQL
//	contexts.go         taste contexts and session activation
//	rules.go            radio block, downrank, cooldown
//	entities.go         artist and album vectors
//	tags.go             custom tags
//	transition_stats.go outcome-aware A→B weights
//	jobs.go             worker job queue
//	plugin_jobs.go      plugin import tasks, separate from the worker queue
//	metrics.go          weekly recommendation metrics
//	monitor.go          recommendation policy and training rows
//	explore.go          bandit arms and explore bounds
//	radio.go            public radio share tokens
//	sessions.go         persisted play sessions
//	playback_state.go   resume position
//	lyrics.go           lyrics rows
package db
