package queue

import (
	"errors"
	"fmt"
)

// Bookmark is a saved folder. SiteID 0 means the local filesystem.
type Bookmark struct {
	ID        int64  `json:"id"`
	SiteID    int64  `json:"siteId"`
	Path      string `json:"path"`
	Label     string `json:"label"`
	CreatedAt string `json:"createdAt"`
}

// AddBookmark saves a folder, replacing any bookmark already on that exact
// path so re-bookmarking updates the label instead of failing.
func (s *Store) AddBookmark(siteID int64, path, label string) (int64, error) {
	if path == "" {
		return 0, errors.New("bookmark requires a path")
	}
	res, err := s.db.Exec(
		`INSERT INTO bookmarks(site_id,path,label,created_at) VALUES (?,?,?,?)
		 ON CONFLICT(site_id,path) DO UPDATE SET label=excluded.label`,
		siteID, path, label, nowUTC())
	if err != nil {
		return 0, fmt.Errorf("add bookmark: %w", err)
	}
	return res.LastInsertId()
}

// Bookmarks lists saved folders for one source, newest first.
func (s *Store) Bookmarks(siteID int64) ([]Bookmark, error) {
	rows, err := s.db.Query(
		`SELECT id,site_id,path,label,created_at FROM bookmarks
		 WHERE site_id=? ORDER BY created_at DESC, id DESC`, siteID)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()

	out := make([]Bookmark, 0, 8)
	for rows.Next() {
		var b Bookmark
		if err := rows.Scan(&b.ID, &b.SiteID, &b.Path, &b.Label, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan bookmark: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBookmark removes one saved folder.
func (s *Store) DeleteBookmark(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM bookmarks WHERE id=?`, id); err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	return nil
}
