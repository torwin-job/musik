package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openAPIJSON []byte

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	_, _ = w.Write(openAPIJSON)
}
