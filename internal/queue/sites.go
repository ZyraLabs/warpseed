package queue

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Site is a saved connection profile. CredRef points into the credential
// store; no secret ever lands in this table.
type Site struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"` // sftp | ftp | ftps | s3 | webdav
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	CredRef     string `json:"credRef"`
	OptionsJSON string `json:"optionsJson"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

var ErrSiteNotFound = errors.New("site not found")

func nowUTC() string { return time.Now().UTC().Format(time.RFC3339) }

// SaveSite inserts (ID==0) or updates (ID>0) and returns the site ID.
func (s *Store) SaveSite(site Site) (int64, error) {
	if site.Name == "" || site.Host == "" || site.Protocol == "" {
		return 0, errors.New("site requires name, protocol and host")
	}
	if site.Port == 0 {
		site.Port = 22
	}
	if site.OptionsJSON == "" {
		site.OptionsJSON = "{}"
	}
	now := nowUTC()

	if site.ID == 0 {
		res, err := s.db.Exec(
			`INSERT INTO sites(name,protocol,host,port,username,cred_ref,options_json,created_at,updated_at)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			site.Name, site.Protocol, site.Host, site.Port, site.Username,
			site.CredRef, site.OptionsJSON, now, now)
		if err != nil {
			return 0, fmt.Errorf("insert site: %w", err)
		}
		return res.LastInsertId()
	}

	res, err := s.db.Exec(
		`UPDATE sites SET name=?, protocol=?, host=?, port=?, username=?, cred_ref=?, options_json=?, updated_at=?
		 WHERE id=?`,
		site.Name, site.Protocol, site.Host, site.Port, site.Username,
		site.CredRef, site.OptionsJSON, now, site.ID)
	if err != nil {
		return 0, fmt.Errorf("update site: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrSiteNotFound
	}
	return site.ID, nil
}

// SetSiteCredRef records the credential reference after the secret is stored.
func (s *Store) SetSiteCredRef(id int64, ref string) error {
	_, err := s.db.Exec(`UPDATE sites SET cred_ref=?, updated_at=? WHERE id=?`, ref, nowUTC(), id)
	if err != nil {
		return fmt.Errorf("set cred ref: %w", err)
	}
	return nil
}

// DeleteSite removes a site; transfers/hostkeys cascade via FK.
func (s *Store) DeleteSite(id int64) error {
	res, err := s.db.Exec(`DELETE FROM sites WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("delete site: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrSiteNotFound
	}
	return nil
}

// Sites lists all saved sites, most recently updated first.
func (s *Store) Sites() ([]Site, error) {
	rows, err := s.db.Query(
		`SELECT id,name,protocol,host,port,username,cred_ref,options_json,created_at,updated_at
		 FROM sites ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list sites: %w", err)
	}
	defer rows.Close()

	sites := make([]Site, 0, 8)
	for rows.Next() {
		var x Site
		if err := rows.Scan(&x.ID, &x.Name, &x.Protocol, &x.Host, &x.Port,
			&x.Username, &x.CredRef, &x.OptionsJSON, &x.CreatedAt, &x.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan site: %w", err)
		}
		sites = append(sites, x)
	}
	return sites, rows.Err()
}

// SiteByID fetches one site.
func (s *Store) SiteByID(id int64) (Site, error) {
	var x Site
	err := s.db.QueryRow(
		`SELECT id,name,protocol,host,port,username,cred_ref,options_json,created_at,updated_at
		 FROM sites WHERE id=?`, id).
		Scan(&x.ID, &x.Name, &x.Protocol, &x.Host, &x.Port,
			&x.Username, &x.CredRef, &x.OptionsJSON, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Site{}, ErrSiteNotFound
	}
	if err != nil {
		return Site{}, fmt.Errorf("get site: %w", err)
	}
	return x, nil
}
