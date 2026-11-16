package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/torwin-job/musik/player/internal/yandex"
)

const (
	pluginID     = "yandex"
	protocolV1   = "1"
	maxRequest   = 1 << 20
	maxItems     = 100
	defaultLimit = 8
)

type request struct {
	Type     string `json:"type"`
	Protocol string `json:"protocol"`
	ID       string `json:"id"`
	Action   string `json:"action"`
	Input    struct {
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		URL     string `json:"url"`
		Library string `json:"library"`
		DestDir string `json:"dest_dir"`
	} `json:"input"`
}

type failure struct {
	code    string
	message string
}

func (e failure) Error() string { return e.message }

type mediaItem struct {
	track yandex.Track
	place yandex.Placement
}

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	if len(args) != 1 {
		return 2
	}
	switch args[0] {
	case "describe":
		return emit(map[string]any{
			"type": "describe", "protocol": protocolV1, "id": pluginID,
			"name": "Яндекс Музыка", "version": "1.0.0",
			"description":  "Поиск и импорт музыки из Яндекса",
			"capabilities": []string{"library.search", "library.import_url"},
		})
	case "run":
		return runRequest()
	default:
		return 2
	}
}

func runRequest() int {
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxRequest+1))
	if err != nil || len(data) > maxRequest {
		emitError("0", "invalid_request", "request is invalid")
		return 1
	}
	var req request
	if err := json.Unmarshal(data, &req); err != nil || req.Type != "request" || req.Protocol != protocolV1 {
		emitError("0", "invalid_request", "request is invalid")
		return 1
	}
	if req.ID == "" {
		req.ID = "0"
	}
	if !validRequestID(req.ID) {
		emitError("0", "invalid_request", "request is invalid")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var result any
	switch req.Action {
	case "library.search":
		result, err = search(ctx, req)
	case "library.import_url":
		result, err = importURL(ctx, req)
	default:
		err = failure{code: "unsupported_action", message: "action is not supported"}
	}
	if err != nil {
		var f failure
		if errors.As(err, &f) {
			emitError(req.ID, f.code, f.message)
		} else if errors.Is(ctx.Err(), context.Canceled) {
			emitError(req.ID, "cancelled", "operation cancelled")
		} else {
			emitError(req.ID, "internal", "plugin failed")
		}
		return 1
	}
	if emit(map[string]any{
		"type": "result", "protocol": protocolV1, "id": req.ID, "result": result,
	}) != 0 {
		return 1
	}
	return 0
}

func search(ctx context.Context, req request) (any, error) {
	query := strings.TrimSpace(req.Input.Query)
	if query == "" {
		return nil, failure{code: "invalid_request", message: "query is required"}
	}
	limit := req.Input.Limit
	if limit == 0 {
		limit = defaultLimit
	}
	if limit < 1 || limit > 20 {
		return nil, failure{code: "invalid_request", message: "limit is invalid"}
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	found, err := client.Search(ctx, query, "track", 0)
	if err != nil {
		return nil, yandexFailure(err, "search")
	}
	items := make([]map[string]any, 0, min(limit, len(found.Tracks)))
	for _, track := range found.Tracks {
		if len(items) >= limit {
			break
		}
		title := protocolText(track.Title)
		if strings.TrimSpace(string(track.ID)) == "" || title == "" {
			continue
		}
		row := map[string]any{
			"title": title,
			"url":   "https://music.yandex.ru/track/" + url.PathEscape(string(track.ID)),
		}
		if artist := artistNames(track.Artists); artist != "" {
			row["artist"] = artist
		}
		if track.DurationMs > 0 {
			seconds := int(track.DurationMs / 1000)
			if seconds <= 864000 {
				row["duration"] = seconds
			}
		}
		items = append(items, row)
	}
	return map[string]any{"items": items}, nil
}

func importURL(ctx context.Context, req request) (any, error) {
	media, err := parseMediaURL(req.Input.URL)
	if err != nil {
		return nil, failure{code: "unsupported_url", message: "URL is not a supported Yandex Music link"}
	}
	client, err := newClient()
	if err != nil {
		return nil, err
	}
	items, err := resolve(ctx, client, media)
	if err != nil {
		return nil, yandexFailure(err, "import")
	}
	if len(items) == 0 {
		return nil, failure{code: "download_failed", message: "no available tracks were found"}
	}
	if len(items) > maxItems {
		items = items[:maxItems]
	}

	root, destDir, err := destination(req.Input.Library, req.Input.DestDir)
	if err != nil {
		return nil, failure{code: "write_failed", message: "library destination is unavailable"}
	}
	downloader := &yandex.Downloader{
		HTTP:   client.HTTP,
		FFmpeg: firstNonEmpty(os.Getenv("MUSIK_FFMPEG"), "ffmpeg"),
	}
	paths := make([]string, 0, len(items))
	var lastErr error
	for i, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, failure{code: "cancelled", message: "operation cancelled"}
		}
		progress(req.ID, i, len(items), "download")
		if item.track.Available != nil && !*item.track.Available {
			continue
		}
		rel, saveErr := downloader.Save(ctx, client, root, item.track, item.place, quality())
		if saveErr != nil && !errors.Is(saveErr, yandex.ErrExists) {
			lastErr = saveErr
			continue
		}
		paths = append(paths, path.Join(destDir, filepath.ToSlash(rel)))
	}
	if len(paths) == 0 {
		if ctx.Err() != nil {
			return nil, failure{code: "cancelled", message: "operation cancelled"}
		}
		if lastErr != nil {
			return nil, failure{code: "download_failed", message: "Yandex could not provide the selected tracks"}
		}
		return nil, failure{code: "download_failed", message: "no tracks were imported"}
	}
	progress(req.ID, len(items), len(items), "write")
	return map[string]any{"paths": paths}, nil
}

type mediaRef struct {
	kind    string
	id      string
	albumID string
}

func parseMediaURL(raw string) (mediaRef, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return mediaRef{}, errors.New("invalid URL")
	}
	host := strings.ToLower(u.Hostname())
	switch host {
	case "music.yandex.ru", "music.yandex.com", "music.yandex.by", "music.yandex.kz", "yandex.ru":
	default:
		return mediaRef{}, errors.New("unsupported host")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 2 {
		switch parts[0] {
		case "track":
			if numericID(parts[1]) {
				return mediaRef{kind: "track", id: parts[1]}, nil
			}
		case "album":
			if !numericID(parts[1]) {
				break
			}
			if len(parts) >= 4 && parts[2] == "track" && numericID(parts[3]) {
				return mediaRef{kind: "track", id: parts[3], albumID: parts[1]}, nil
			}
			return mediaRef{kind: "album", id: parts[1]}, nil
		case "artist":
			if numericID(parts[1]) {
				return mediaRef{kind: "artist", id: parts[1]}, nil
			}
		}
	}
	return mediaRef{}, errors.New("unsupported Yandex Music URL")
}

func resolve(ctx context.Context, client *yandex.Client, ref mediaRef) ([]mediaItem, error) {
	switch ref.kind {
	case "track":
		tracks, err := client.Tracks(ctx, []string{ref.id})
		if err != nil {
			return nil, err
		}
		if len(tracks) == 0 {
			return nil, errors.New("track not found")
		}
		track := tracks[0]
		place := yandex.PlacementFor(track, nil)
		if ref.albumID != "" {
			if album, err := client.AlbumWithTracks(ctx, ref.albumID); err == nil {
				if album.ID != "" {
					place = placementInAlbum(track, album)
				}
			}
		}
		return []mediaItem{{track: track, place: place}}, nil
	case "album":
		album, err := client.AlbumWithTracks(ctx, ref.id)
		if err != nil {
			return nil, err
		}
		return albumItems(album), nil
	case "artist":
		albums, err := client.ArtistAlbums(ctx, ref.id)
		if err != nil {
			return nil, err
		}
		var out []mediaItem
		for _, album := range albums {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			full, err := client.AlbumWithTracks(ctx, string(album.ID))
			if err != nil {
				continue
			}
			out = append(out, albumItems(full)...)
			if len(out) >= maxItems {
				break
			}
		}
		return out, nil
	default:
		return nil, errors.New("unsupported item")
	}
}

func albumItems(album yandex.Album) []mediaItem {
	var out []mediaItem
	for disc, volume := range album.Volumes {
		for index, track := range volume {
			place := yandex.PlacementFor(track, &album)
			place.Disc = disc + 1
			place.Number = index + 1
			out = append(out, mediaItem{track: track, place: place})
		}
	}
	return out
}

func placementInAlbum(track yandex.Track, album yandex.Album) yandex.Placement {
	for disc, volume := range album.Volumes {
		for index, candidate := range volume {
			if candidate.ID == track.ID {
				place := yandex.PlacementFor(track, &album)
				place.Disc = disc + 1
				place.Number = index + 1
				return place
			}
		}
	}
	return yandex.PlacementFor(track, &album)
}

func destination(libraryRoot, rawDest string) (string, string, error) {
	if !filepath.IsAbs(libraryRoot) {
		return "", "", errors.New("library path must be absolute")
	}
	base, err := filepath.EvalSymlinks(libraryRoot)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("library is unavailable")
	}
	destDir, err := safeDestDir(rawDest)
	if err != nil {
		return "", "", err
	}
	resolved := base
	for _, segment := range strings.Split(destDir, "/") {
		resolved = filepath.Join(resolved, segment)
		info, err := os.Lstat(resolved)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(resolved, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return "", "", err
			}
			info, err = os.Lstat(resolved)
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "", errors.New("destination is not a real directory")
		}
	}
	return resolved, destDir, nil
}

func safeDestDir(raw string) (string, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if raw == "" {
		raw = "imports/yandex"
	}
	if strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, 0) {
		return "", errors.New("invalid destination")
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid destination")
	}
	for _, segment := range strings.Split(clean, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.HasPrefix(segment, ".") {
			return "", errors.New("invalid destination")
		}
	}
	return clean, nil
}

func newClient() (*yandex.Client, error) {
	tokenPath := strings.TrimSpace(os.Getenv("MUSIK_YANDEX_TOKEN_FILE"))
	if tokenPath == "" {
		executable, err := os.Executable()
		if err != nil {
			return nil, failure{code: "internal", message: "Yandex token is not configured"}
		}
		tokenPath = filepath.Join(filepath.Dir(executable), "token")
	}
	data, err := os.ReadFile(tokenPath)
	if err != nil || len(data) > 4096 {
		return nil, failure{code: "internal", message: "Yandex token is not configured"}
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, failure{code: "internal", message: "Yandex token is not configured"}
	}
	return &yandex.Client{
		HTTP:    &http.Client{Timeout: 10 * time.Minute},
		APIURL:  strings.TrimSpace(os.Getenv("MUSIK_YANDEX_API_URL")),
		Token:   token,
		SignKey: strings.TrimSpace(os.Getenv("MUSIK_YANDEX_DESKTOP_SIGN_KEY")),
	}, nil
}

func quality() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MUSIK_YANDEX_QUALITY"))) {
	case "hq", "nq":
		return strings.ToLower(strings.TrimSpace(os.Getenv("MUSIK_YANDEX_QUALITY")))
	default:
		return "lossless"
	}
}

func yandexFailure(err error, action string) error {
	if errors.Is(err, yandex.ErrUnauthorized) {
		return failure{code: "internal", message: "Yandex token was rejected"}
	}
	if action == "search" {
		return failure{code: "internal", message: "Yandex search failed"}
	}
	return failure{code: "download_failed", message: "Yandex could not provide the selected tracks"}
}

func progress(id string, done, total int, phase string) {
	pct := 0.0
	if total > 0 {
		pct = float64(done) * 100 / float64(total)
	}
	_ = emit(map[string]any{
		"type": "progress", "protocol": protocolV1, "id": id,
		"progress": map[string]any{
			"pct": pct, "phase": phase,
			"message": fmt.Sprintf("Загрузка %d из %d", min(done+1, total), total),
		},
	})
}

func artistNames(artists []yandex.Artist) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if name := protocolText(artist.Name); name != "" {
			names = append(names, name)
		}
	}
	return protocolText(strings.Join(names, ", "))
}

func protocolText(value string) string {
	var b strings.Builder
	count := 0
	for _, r := range strings.TrimSpace(value) {
		if r == '\n' || r == '\r' || r == '\t' {
			r = ' '
		}
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
		count++
		if count >= 500 {
			break
		}
	}
	return strings.TrimSpace(strings.ToValidUTF8(b.String(), ""))
}

func numericID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for i, r := range value {
		valid := r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)
		if !valid || (i == 0 && !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func emitError(id, code, message string) {
	if id == "" {
		id = "0"
	}
	_ = emit(map[string]any{
		"type": "error", "protocol": protocolV1, "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
}

func emit(value any) int {
	if err := json.NewEncoder(os.Stdout).Encode(value); err != nil {
		return 1
	}
	return 0
}
