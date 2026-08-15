package main

import (
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/static"
	"github.com/torwin-job/musik/player/internal/taste"
)

func main() {
	cfg := config.Load()
	if v := os.Getenv("MUSIK_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if !cfg.AuthEnabled() && !cfg.AuthDisabled {
		log.Fatal("auth required: set MUSIK_PASSWORD and/or MUSIK_API_TOKEN (or MUSIK_AUTH_DISABLED=1 for local open mode)")
	}
	log.Printf("musik-player db=%s addr=%s", cfg.DBPath, cfg.Addr)

	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer store.Close()

	idx := index.New(cfg)
	tp := taste.New()

	staticFS, err := fs.Sub(static.FS, ".")
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	srv := api.New(cfg, store, idx, tp, http.FS(staticFS))
	if err := srv.Reload(); err != nil {
		log.Fatalf("reload: %v", err)
	}

	srv.EnsureWorker()
	srv.WatchJobs()

	if cfg.AuthEnabled() {
		log.Printf("auth enabled (password=%v token=%v)", cfg.Password != "", cfg.APIToken != "")
	} else {
		log.Printf("auth explicitly disabled (MUSIK_AUTH_DISABLED=1)")
	}
	addr := cfg.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "0.0.0.0" + addr
	}
	log.Printf("listening on http://%s  tracks=%d  sessions=multi", addr, idx.Size())
	if err := http.ListenAndServe(cfg.Addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
