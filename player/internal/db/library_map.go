package db

import "database/sql"

// LibraryMapKey is the scan_state row the worker writes the library map to
// (musik.index.library_map).
const LibraryMapKey = "library_map"

// LibraryMapJSON returns the raw map document, or "" before the first build.
func (s *Store) LibraryMapJSON() (string, error) {
	var value string
	err := s.DB.QueryRow(`SELECT value FROM scan_state WHERE key = ?`, LibraryMapKey).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

// MapTransition is one listened A→B step of this profile: Count is how many
// times B was played at least in part right after A.
type MapTransition struct {
	From, To int64
	Count    int
}

// ListMapTransitions returns the profile's most frequent listened transitions.
func (s *Store) ListMapTransitions(limit int) ([]MapTransition, error) {
	return QueryAll(s.DB, func(rows *sql.Rows, t *MapTransition) error {
		return rows.Scan(&t.From, &t.To, &t.Count)
	}, `
SELECT from_id, to_id, finished_count + partial_count AS n
FROM transition_stats
WHERE profile_id = ? AND finished_count + partial_count > 0 AND from_id <> to_id
ORDER BY n DESC, from_id, to_id
LIMIT ?`, s.ProfileID, limit)
}
