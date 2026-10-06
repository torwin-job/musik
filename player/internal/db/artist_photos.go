package db

import (
	"database/sql"
	"errors"
)

// ArtistPhoto is a photo the Python side found online for an artist.
// NameKey is the normalized name: spaces collapsed, lower case.
type ArtistPhoto struct {
	ID      int64
	NameKey string
	Path    string
}

func (s *Store) ListArtistPhotos() ([]ArtistPhoto, error) {
	rows, err := s.DB.Query(`
SELECT id, name_key, path FROM artist_photos
WHERE status = 'found' AND COALESCE(path, '') != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistPhoto
	for rows.Next() {
		var p ArtistPhoto
		if err := rows.Scan(&p.ID, &p.NameKey, &p.Path); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) ArtistPhotoPath(id int64) (string, bool, error) {
	var path string
	err := s.DB.QueryRow(`
SELECT path FROM artist_photos
WHERE id = ? AND status = 'found' AND COALESCE(path, '') != ''`, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return path, true, nil
}
