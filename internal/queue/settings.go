package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Setting returns a settings value or def when absent.
func (s *Store) Setting(key, def string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) || err != nil {
		return def
	}
	return v
}

// SettingInt returns an integer setting, falling back to def on absence or
// malformed values.
func (s *Store) SettingInt(key string, def int) int {
	v, err := strconv.Atoi(s.Setting(key, strconv.Itoa(def)))
	if err != nil {
		return def
	}
	return v
}

// SetSetting upserts one settings key.
func (s *Store) SetSetting(key, value string) error {
	if key == "" {
		return errors.New("empty settings key")
	}
	_, err := s.db.Exec(
		`INSERT INTO settings(key,value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// AllSettings returns every settings row (small table, UI consumption).
func (s *Store) AllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string, 16)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}
