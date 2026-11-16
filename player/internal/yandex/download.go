package yandex

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/torwin-job/musik/player/internal/library"
)

// LibraryFolder is the top folder Yandex downloads go into, under the shared
// library or under a profile's private folder.
const LibraryFolder = "Yandex Music"

const maxTrackBytes = library.MaxFileBytes

// Downloader saves Yandex tracks as tagged files.
type Downloader struct {
	HTTP   *http.Client
	FFmpeg string
	// WorkDir holds partial downloads; empty uses the system temp folder.
	WorkDir string
}

// ErrExists means the track is already in the library.
var ErrExists = errors.New("already in the library")

// Placement says where a track sits: album folder and file name.
type Placement struct {
	AlbumArtist string
	Album       string
	Year        int
	Disc        int
	Discs       int
	Number      int
}

// RelPath is the library-relative file name for a track with extension ext.
func (p Placement) RelPath(t Track, ext string) string {
	artist := cleanName(p.AlbumArtist, "Unknown Artist")
	album := cleanName(p.Album, "Singles")
	if p.Year > 0 && !strings.Contains(album, "("+strconv.Itoa(p.Year)+")") {
		album = cleanName(fmt.Sprintf("%s (%d)", album, p.Year), album)
	}
	name := cleanName(TrackTitle(t), "Unknown Track")
	if p.Number > 0 {
		prefix := fmt.Sprintf("%02d. ", p.Number)
		if p.Discs > 1 && p.Disc > 0 {
			prefix = fmt.Sprintf("%d-%02d. ", p.Disc, p.Number)
		}
		name = prefix + name
	}
	return filepath.Join(LibraryFolder, artist, album, name+ext)
}

// PlacementFor places a track on its first album, or on album when given.
func PlacementFor(t Track, album *Album) Placement {
	var a Album
	if album != nil {
		a = *album
	} else if len(t.Albums) > 0 {
		a = t.Albums[0]
	}
	p := Placement{AlbumArtist: joinArtists(a.Artists), Album: AlbumTitle(a), Year: a.Year, Discs: len(a.Volumes)}
	if p.AlbumArtist == "" {
		p.AlbumArtist = joinArtists(t.Artists)
	}
	pos := a.TrackPosition
	if pos == nil {
		for _, ta := range t.Albums {
			if ta.ID == a.ID && ta.TrackPosition != nil {
				pos = ta.TrackPosition
				break
			}
		}
	}
	if pos != nil {
		p.Disc, p.Number = pos.Volume, pos.Index
	}
	return p
}

// TrackTitle is the title with its version ("Remix", "Live") appended.
func TrackTitle(t Track) string {
	return withVersion(t.Title, t.Version)
}

func AlbumTitle(a Album) string {
	return withVersion(a.Title, a.Version)
}

func withVersion(title, version string) string {
	title, version = strings.TrimSpace(title), strings.TrimSpace(version)
	if version == "" || strings.Contains(strings.ToLower(title), strings.ToLower(version)) {
		return title
	}
	return title + " (" + version + ")"
}

func joinArtists(as []Artist) string {
	names := make([]string, 0, len(as))
	for _, a := range as {
		if n := strings.TrimSpace(a.Name); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, ", ")
}

// cleanName makes one safe path segment: no separators or reserved
// characters, no leading dot (the scanner skips hidden files), bounded length.
func cleanName(s, fallback string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r < 0x20 || r == 0x7f:
			continue
		case strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('_')
		case r >= 0x200B && r <= 0x200D || r == 0xFEFF:
			continue
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(strings.TrimSpace(b.String()), ".")
	for utf8.RuneCountInString(out) > 120 {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	out = strings.TrimRight(strings.TrimSpace(out), ". ")
	if out == "" {
		return fallback
	}
	return out
}

// extFor maps a Yandex codec to the file the library stores.
func extFor(codec string) string {
	switch codec {
	case "flac", "flac-mp4":
		return ".flac"
	case "mp3":
		return ".mp3"
	default:
		return ".m4a"
	}
}

// Save downloads track t into root and returns the library-relative path.
// An existing file with the same name is left alone and reported as ErrExists.
func (d *Downloader) Save(ctx context.Context, c *Client, root string, t Track, place Placement, quality string) (string, error) {
	// Skip before asking for a link: a track saved earlier in any format stays.
	for _, ext := range []string{".flac", ".m4a", ".mp3"} {
		rel := place.RelPath(t, ext)
		if dest, err := library.ResolveDest(root, rel); err == nil {
			if _, err := os.Stat(dest); err == nil {
				return rel, ErrExists
			}
		}
	}
	info, err := c.FileInfo(ctx, string(t.ID), quality)
	if err != nil {
		return "", err
	}
	ext := extFor(info.Codec)
	rel := place.RelPath(t, ext)
	if _, err := library.SanitizeRelPath(rel); err != nil {
		return "", err
	}

	work, err := os.MkdirTemp(d.WorkDir, "ym-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	src := filepath.Join(work, "source")
	if err := d.fetchAudio(ctx, info, src); err != nil {
		return "", err
	}
	cover := ""
	if uri := firstNonEmpty(albumCover(t, place), t.CoverURI); uri != "" {
		path := filepath.Join(work, "cover.jpg")
		if d.fetch(ctx, CoverURL(uri, "1000x1000"), path, 20<<20) == nil {
			cover = path
		}
	}
	out := filepath.Join(work, "out"+ext)
	if err := d.tag(ctx, src, cover, out, ext, tagsFor(t, place)); err != nil {
		// Without ffmpeg a file whose container already matches is still
		// usable, just untagged; MP4-packed FLAC is not.
		if info.Codec == "flac-mp4" {
			return "", err
		}
		out = src
	}
	f, err := os.Open(out)
	if err != nil {
		return "", err
	}
	defer f.Close()
	// A profile's private folder does not exist before its first download.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	if _, err := library.SaveFile(root, rel, f); err != nil {
		return "", err
	}
	return rel, nil
}

func albumCover(t Track, place Placement) string {
	for _, a := range t.Albums {
		if AlbumTitle(a) == place.Album && a.CoverURI != "" {
			return a.CoverURI
		}
	}
	if len(t.Albums) > 0 {
		return t.Albums[0].CoverURI
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// fetchAudio downloads the file, trying each mirror, and decrypts encraw.
func (d *Downloader) fetchAudio(ctx context.Context, info FileInfo, dest string) error {
	urls := append([]string{}, info.URLs...)
	if info.URL != "" && !contains(urls, info.URL) {
		urls = append([]string{info.URL}, urls...)
	}
	var last error = errors.New("no download url")
	for _, u := range urls {
		if last = d.fetch(ctx, u, dest, maxTrackBytes); last == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if last != nil {
		return last
	}
	if info.Transport != "encraw" || info.Key == "" {
		return nil
	}
	return decryptFile(dest, info.Key)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (d *Downloader) fetch(ctx context.Context, u, dest string, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return err
	}
	client := d.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download: http %d", res.StatusCode)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, limit+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = errors.New("download: file too large")
	}
	return err
}

// decryptFile undoes the encraw transport: AES-128-CTR with a zero IV.
func decryptFile(path, hexKey string) error {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return fmt.Errorf("decrypt: bad key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	plain := path + ".dec"
	out, err := os.Create(plain)
	if err != nil {
		return err
	}
	stream := cipher.StreamReader{S: cipher.NewCTR(block, make([]byte, aes.BlockSize)), R: in}
	if _, err := io.Copy(out, stream); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(plain, path)
}

type tag struct{ key, value string }

func tagsFor(t Track, p Placement) []tag {
	tags := []tag{
		{"title", TrackTitle(t)},
		{"artist", joinArtists(t.Artists)},
		{"album_artist", p.AlbumArtist},
		{"album", p.Album},
	}
	if p.Year > 0 {
		tags = append(tags, tag{"date", strconv.Itoa(p.Year)})
	}
	var genre, label string
	for _, a := range t.Albums {
		if genre == "" {
			genre = a.Genre
		}
		if label == "" && len(a.Labels) > 0 {
			label = a.Labels[0].Name
		}
	}
	if genre != "" {
		tags = append(tags, tag{"genre", genre})
	}
	if label != "" {
		tags = append(tags, tag{"publisher", label})
	}
	if p.Number > 0 {
		tags = append(tags, tag{"track", strconv.Itoa(p.Number)})
	}
	if p.Disc > 0 {
		tags = append(tags, tag{"disc", strconv.Itoa(p.Disc)})
	}
	return tags
}

// tag remuxes src into the library container without re-encoding and writes
// tags and the cover. A failed cover attempt is retried without the cover.
func (d *Downloader) tag(ctx context.Context, src, cover, out, ext string, tags []tag) error {
	ffmpeg := d.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	run := func(withCover bool) error {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", src}
		if withCover {
			args = append(args, "-i", cover, "-map", "0:a:0", "-map", "1:v:0", "-c:v", "copy", "-disposition:v:0", "attached_pic")
		} else {
			args = append(args, "-map", "0:a:0")
		}
		args = append(args, "-c:a", "copy", "-map_metadata", "-1")
		for _, t := range tags {
			if t.value != "" {
				args = append(args, "-metadata", t.key+"="+t.value)
			}
		}
		switch ext {
		case ".flac":
			args = append(args, "-f", "flac")
		case ".mp3":
			args = append(args, "-id3v2_version", "3", "-f", "mp3")
		default:
			args = append(args, "-f", "ipod")
		}
		args = append(args, out)
		cmd := exec.CommandContext(ctx, ffmpeg, args...)
		msg, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(msg)))
		}
		return nil
	}
	if cover != "" {
		if err := run(true); err == nil {
			return nil
		}
	}
	return run(false)
}
