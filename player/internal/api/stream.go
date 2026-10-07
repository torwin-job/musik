package api

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	row, ok := s.Idx.RowOf(id)
	path := ""
	if ok {
		path = s.Idx.MetaAt(row).Path
	} else if p, err := s.Store.TrackPath(id); err == nil && p != "" {
		path = p
	} else {
		writeErr(w, 404, "not_found", "not found")
		return
	}
	q := strings.ToLower(r.URL.Query().Get("q"))
	if q == "" {
		q = strings.ToLower(r.URL.Query().Get("fmt"))
	}
	if q == "mobile" || q == "aac" || q == "mp3" {
		s.Media.ServeMobile(w, r, id, path)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, 404, "file_missing", "file missing")
		return
	}
	defer f.Close()
	stat, _ := f.Stat()
	w.Header().Set("Content-Type", contentType(path))
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeContent(w, r, filepath.Base(path), stat.ModTime(), flacBody(f, path, stat.Size()))
}

// flacBody skips an ID3v2 tag in front of a FLAC stream. Some rips carry one;
// ffmpeg reads past it, but browsers report "no supported streams" and the
// track never starts, which stalled the radio on it. The file is left alone.
func flacBody(f *os.File, path string, size int64) io.ReadSeeker {
	if !strings.EqualFold(filepath.Ext(path), ".flac") {
		return f
	}
	off := id3v2Len(f)
	if off == 0 || off >= size {
		return f
	}
	magic := make([]byte, 4)
	if _, err := f.ReadAt(magic, off); err != nil || string(magic) != "fLaC" {
		return f
	}
	return io.NewSectionReader(f, off, size-off)
}

// id3v2Len is the length of an ID3v2 tag at the start of r, 0 without one.
func id3v2Len(r io.ReaderAt) int64 {
	h := make([]byte, 10)
	if _, err := r.ReadAt(h, 0); err != nil || string(h[:3]) != "ID3" {
		return 0
	}
	n := int64(h[6]&0x7f)<<21 | int64(h[7]&0x7f)<<14 | int64(h[8]&0x7f)<<7 | int64(h[9]&0x7f)
	n += 10
	if h[5]&0x10 != 0 { // footer
		n += 10
	}
	return n
}
