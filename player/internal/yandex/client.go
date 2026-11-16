// Package yandex is the optional Yandex Music plugin: a small client for the
// mobile API (the one MarshalX/yandex-music-api documents), OAuth device
// login, and a downloader that saves tracks into the music library so the
// usual scan → embed pipeline picks them up.
package yandex

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIURL   = "https://api.music.yandex.net"
	DefaultOAuthURL = "https://oauth.yandex.ru"

	androidClient = "YandexMusicAndroid/24023621"
	// get-file-info is signed with the desktop app's key, and the server
	// checks the signature together with the matching client header. The key
	// is operator configuration and must not be embedded in source or binaries.
	desktopClient = "YandexMusicDesktopAppWindows/5.95.0"
)

// fileInfoCodecs lists every codec the server may pick from, best first.
var fileInfoCodecs = []string{"flac", "flac-mp4", "aac-mp4", "he-aac-mp4", "aac", "he-aac", "mp3"}

// ErrUnauthorized means the token is missing, expired or revoked.
var ErrUnauthorized = errors.New("yandex: unauthorized")

// APIError is a non-2xx answer from Yandex.
type APIError struct {
	Status  int
	Name    string
	Message string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("yandex: %d %s: %s", e.Status, e.Name, e.Message)
	}
	return fmt.Sprintf("yandex: http %d", e.Status)
}

// ID accepts both the numeric and the string ids the API mixes.
type ID string

func (id *ID) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*id = ID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*id = ID(n.String())
	return nil
}

type Artist struct {
	ID    ID     `json:"id"`
	Name  string `json:"name"`
	Cover *Cover `json:"cover,omitempty"`
}

type Cover struct {
	URI string `json:"uri"`
}

type Label struct {
	Name string `json:"name"`
}

type TrackPosition struct {
	Volume int `json:"volume"`
	Index  int `json:"index"`
}

type Album struct {
	ID            ID             `json:"id"`
	Title         string         `json:"title"`
	Version       string         `json:"version"`
	Year          int            `json:"year"`
	Genre         string         `json:"genre"`
	CoverURI      string         `json:"coverUri"`
	TrackCount    int            `json:"trackCount"`
	Artists       []Artist       `json:"artists"`
	Labels        []Label        `json:"-"`
	TrackPosition *TrackPosition `json:"trackPosition,omitempty"`
	Volumes       [][]Track      `json:"volumes,omitempty"`
	Available     *bool          `json:"available,omitempty"`
}

// UnmarshalJSON reads labels, which come as objects or as plain names.
func (a *Album) UnmarshalJSON(b []byte) error {
	type plain Album
	var raw struct {
		plain
		Labels []json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*a = Album(raw.plain)
	for _, l := range raw.Labels {
		var name string
		if json.Unmarshal(l, &name) == nil {
			a.Labels = append(a.Labels, Label{Name: name})
			continue
		}
		var label Label
		if json.Unmarshal(l, &label) == nil && label.Name != "" {
			a.Labels = append(a.Labels, label)
		}
	}
	return nil
}

type Track struct {
	ID         ID       `json:"id"`
	Title      string   `json:"title"`
	Version    string   `json:"version"`
	DurationMs int64    `json:"durationMs"`
	Artists    []Artist `json:"artists"`
	Albums     []Album  `json:"albums"`
	CoverURI   string   `json:"coverUri"`
	Available  *bool    `json:"available,omitempty"`
}

type Account struct {
	UID         ID     `json:"uid"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
	HasPlus     bool   `json:"-"`
}

type SearchResult struct {
	Artists []Artist `json:"artists"`
	Albums  []Album  `json:"albums"`
	Tracks  []Track  `json:"tracks"`
}

// FileInfo says where to fetch one track file.
type FileInfo struct {
	TrackID   string   `json:"trackId"`
	Quality   string   `json:"quality"`
	Codec     string   `json:"codec"`
	Bitrate   int      `json:"bitrate"`
	Transport string   `json:"transport"`
	URL       string   `json:"url"`
	URLs      []string `json:"urls"`
	Key       string   `json:"key"`
}

// Client talks to the Yandex Music API with one user's OAuth token.
type Client struct {
	HTTP     *http.Client
	APIURL   string
	OAuthURL string
	Token    string
	SignKey  string
	// Now is the clock used to sign requests; tests pin it.
	Now func() time.Time
}

func (c *Client) api() string {
	if c.APIURL != "" {
		return strings.TrimRight(c.APIURL, "/")
	}
	return DefaultAPIURL
}

func (c *Client) oauth() string {
	if c.OAuthURL != "" {
		return strings.TrimRight(c.OAuthURL, "/")
	}
	return DefaultOAuthURL
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// call sends one API request and decodes its "result" into out.
func (c *Client) call(ctx context.Context, method, path string, query url.Values, form url.Values, header http.Header, out any) error {
	u := c.api() + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Yandex-Music-Client", androidClient)
	req.Header.Set("Accept-Language", "ru")
	if c.Token != "" {
		req.Header.Set("Authorization", "OAuth "+c.Token)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return err
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	if res.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if res.StatusCode >= 300 {
		return apiError(res.StatusCode, env.Error)
	}
	if out == nil {
		return nil
	}
	if len(env.Result) == 0 || bytes.Equal(env.Result, []byte("null")) {
		return &APIError{Status: res.StatusCode, Message: "empty result"}
	}
	return json.Unmarshal(env.Result, out)
}

func apiError(status int, raw json.RawMessage) error {
	e := &APIError{Status: status}
	var obj struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		e.Name, e.Message = obj.Name, obj.Message
	} else {
		_ = json.Unmarshal(raw, &e.Message)
	}
	return e
}

// Account returns the signed-in account and whether it has Plus.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var res struct {
		Account Account `json:"account"`
		Plus    struct {
			HasPlus bool `json:"hasPlus"`
		} `json:"plus"`
	}
	if err := c.call(ctx, "GET", "/account/status", nil, nil, nil, &res); err != nil {
		return Account{}, err
	}
	res.Account.HasPlus = res.Plus.HasPlus
	return res.Account, nil
}

// Search finds artists, albums and tracks. kind is all, artist, album or track.
func (c *Client) Search(ctx context.Context, text, kind string, page int) (SearchResult, error) {
	if kind == "" {
		kind = "all"
	}
	q := url.Values{
		"text": {text}, "type": {kind}, "page": {strconv.Itoa(page)},
		"nocorrect": {"False"}, "playlist-in-best": {"True"},
	}
	var res struct {
		Artists *struct {
			Results []Artist `json:"results"`
		} `json:"artists"`
		Albums *struct {
			Results []Album `json:"results"`
		} `json:"albums"`
		Tracks *struct {
			Results []Track `json:"results"`
		} `json:"tracks"`
	}
	if err := c.call(ctx, "GET", "/search", q, nil, nil, &res); err != nil {
		return SearchResult{}, err
	}
	var out SearchResult
	if res.Artists != nil {
		out.Artists = res.Artists.Results
	}
	if res.Albums != nil {
		out.Albums = res.Albums.Results
	}
	if res.Tracks != nil {
		out.Tracks = res.Tracks.Results
	}
	return out, nil
}

// Tracks returns full track objects for ids.
func (c *Client) Tracks(ctx context.Context, ids []string) ([]Track, error) {
	var out []Track
	form := url.Values{"track-ids": {strings.Join(ids, ",")}, "with-positions": {"True"}}
	err := c.call(ctx, "POST", "/tracks", nil, form, nil, &out)
	return out, err
}

// AlbumWithTracks returns an album and its tracks grouped by disc.
func (c *Client) AlbumWithTracks(ctx context.Context, id string) (Album, error) {
	var out Album
	err := c.call(ctx, "GET", "/albums/"+url.PathEscape(id)+"/with-tracks", nil, nil, nil, &out)
	return out, err
}

// ArtistAlbums returns every album the artist released, newest first.
func (c *Client) ArtistAlbums(ctx context.Context, id string) ([]Album, error) {
	var all []Album
	for page := 0; page < 50; page++ {
		var res struct {
			Albums []Album `json:"albums"`
			Pager  struct {
				Total   int `json:"total"`
				PerPage int `json:"perPage"`
			} `json:"pager"`
		}
		q := url.Values{"page": {strconv.Itoa(page)}, "page-size": {"100"}, "sort-by": {"year"}}
		if err := c.call(ctx, "GET", "/artists/"+url.PathEscape(id)+"/direct-albums", q, nil, nil, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Albums...)
		if len(res.Albums) == 0 || len(all) >= res.Pager.Total {
			break
		}
	}
	return all, nil
}

// ArtistTracks returns the artist's popular tracks (first page).
func (c *Client) ArtistTracks(ctx context.Context, id string, pageSize int) ([]Track, error) {
	var res struct {
		Tracks []Track `json:"tracks"`
	}
	q := url.Values{"page": {"0"}, "page-size": {strconv.Itoa(pageSize)}}
	err := c.call(ctx, "GET", "/artists/"+url.PathEscape(id)+"/tracks", q, nil, nil, &res)
	return res.Tracks, err
}

// Artist returns the artist card.
func (c *Client) Artist(ctx context.Context, id string) (Artist, error) {
	var res struct {
		Artist Artist `json:"artist"`
	}
	err := c.call(ctx, "GET", "/artists/"+url.PathEscape(id)+"/brief-info", nil, nil, nil, &res)
	return res.Artist, err
}

// FileInfoSign signs a get-file-info request the way the desktop app does.
func FileInfoSign(key string, ts int64, trackID, quality string, codecs []string, transport string) string {
	msg := strconv.FormatInt(ts, 10) + trackID + quality + strings.Join(codecs, "") + transport
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return sign[:len(sign)-1]
}

// FileInfo asks for the best file of a track. The server picks among the
// listed codecs and falls back to a lower quality when lossless is missing.
func (c *Client) FileInfo(ctx context.Context, trackID, quality string) (FileInfo, error) {
	if c.SignKey == "" {
		return FileInfo{}, errors.New("yandex: download signing key is not configured")
	}
	if quality == "" {
		quality = "lossless"
	}
	const transport = "raw"
	ts := c.now().Unix()
	q := url.Values{
		"ts":         {strconv.FormatInt(ts, 10)},
		"trackId":    {trackID},
		"quality":    {quality},
		"codecs":     {strings.Join(fileInfoCodecs, ",")},
		"transports": {transport},
		"sign":       {FileInfoSign(c.SignKey, ts, trackID, quality, fileInfoCodecs, transport)},
	}
	var res struct {
		DownloadInfo *FileInfo `json:"downloadInfo"`
	}
	h := http.Header{"X-Yandex-Music-Client": {desktopClient}}
	if err := c.call(ctx, "GET", "/get-file-info", q, nil, h, &res); err != nil {
		return FileInfo{}, err
	}
	if res.DownloadInfo == nil || (res.DownloadInfo.URL == "" && len(res.DownloadInfo.URLs) == 0) {
		return FileInfo{}, errors.New("yandex: no file for this track")
	}
	return *res.DownloadInfo, nil
}

// CoverURL turns a coverUri template ("avatars.yandex.net/.../%%") into a link.
func CoverURL(uri, size string) string {
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return ""
	}
	uri = strings.Replace(uri, "%%", size, 1)
	if !strings.HasPrefix(uri, "http") {
		uri = "https://" + strings.TrimPrefix(uri, "//")
	}
	return uri
}
