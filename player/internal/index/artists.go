package index

import "strings"

// trackArtistNames returns the artist names a track is grouped under. The
// collaborator split is parsed once at scan time and stored in the database
// (row.artist_segments); the read path only trims those segments and falls
// back to the raw label when none were stored. There is no separator guessing
// here — "Король и Шут" and "Simon & Garfunkel" stay whole because the scanner
// never split them.
func trackArtistNames(artist string, segments []string) []string {
	out := make([]string, 0, len(segments)+1)
	seen := make(map[string]bool, len(segments)+1)
	add := func(name string) {
		name = strings.TrimSpace(name)
		key := normName(name)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(segments) == 0 {
		add(artist)
		return out
	}
	for _, name := range segments {
		add(name)
	}
	return out
}
