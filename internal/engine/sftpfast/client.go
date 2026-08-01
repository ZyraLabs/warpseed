// Package sftpfast is warpseed's native SFTP engine: pipelined requests,
// large in-flight windows, byte-level resume — the things rclone's SFTP
// backend cannot do (approved plan, Part 2).
package sftpfast

import (
	"context"
	"fmt"
	"net"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"warpseed/internal/engine/core"
)

// Config describes one SFTP endpoint plus tuning knobs.
type Config struct {
	Host     string
	Port     int
	User     string
	Password string // agent/key auth arrives with the Windows agent bridge
	// Concurrency is outstanding requests per file (research: 64 default,
	// up to 256 saturates 1 Gbps at high RTT).
	Concurrency int
	Timeout     time.Duration
}

func (c Config) withDefaults() Config {
	if c.Port == 0 {
		c.Port = 22
	}
	if c.Concurrency == 0 {
		c.Concurrency = 64
	}
	if c.Timeout == 0 {
		c.Timeout = 15 * time.Second
	}
	return c
}

// preferredCiphers orders AEAD ciphers first: AES-GCM rides AES-NI at
// multi-Gbps per core; chacha20 wins on machines without AES-NI.
var preferredCiphers = []string{
	"aes128-gcm@openssh.com",
	"aes256-gcm@openssh.com",
	"chacha20-poly1305@openssh.com",
	"aes128-ctr",
	"aes256-ctr",
}

// Client is one SSH connection carrying one SFTP session. The connection
// manager (next increment) pools these; a browse Client never carries
// file data.
type Client struct {
	ssh  *ssh.Client // nil for in-process test clients
	sftp *sftp.Client
}

// Dial connects, authenticates, and opens an SFTP session. hostKey is
// mandatory — there is no insecure-ignore path in this codebase.
func Dial(ctx context.Context, cfg Config, hostKey ssh.HostKeyCallback) (*Client, error) {
	cfg = cfg.withDefaults()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))

	sshCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            []ssh.AuthMethod{ssh.Password(cfg.Password)},
		HostKeyCallback: hostKey,
		Timeout:         cfg.Timeout,
		Config:          ssh.Config{Ciphers: preferredCiphers},
	}

	d := net.Dialer{Timeout: cfg.Timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, sshCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	sshClient := ssh.NewClient(sc, chans, reqs)

	sftpClient, err := sftp.NewClient(sshClient,
		sftp.UseConcurrentReads(true),
		sftp.UseConcurrentWrites(true),
		sftp.MaxConcurrentRequestsPerFile(cfg.Concurrency),
	)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("open sftp session: %w", err)
	}
	return &Client{ssh: sshClient, sftp: sftpClient}, nil
}

func (c *Client) Close() error {
	var first error
	if c.sftp != nil {
		first = c.sftp.Close()
	}
	if c.ssh != nil {
		if err := c.ssh.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// List reads a remote directory, sorted dirs-first then case-insensitive —
// identical presentation contract to localfs.List.
func (c *Client) List(remotePath string) (core.Listing, error) {
	clean := path.Clean(remotePath)
	if clean == "" || clean == "." {
		clean = "/"
	}
	infos, err := c.sftp.ReadDir(clean)
	if err != nil {
		return core.Listing{}, fmt.Errorf("read remote dir %q: %w", clean, err)
	}

	entries := make([]core.Entry, 0, len(infos))
	for _, fi := range infos {
		e := core.Entry{
			Name:    fi.Name(),
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			ModTime: fi.ModTime().UTC().Format("2006-01-02T15:04:05Z"),
			Mode:    fi.Mode().String(),
		}
		if e.IsDir {
			e.Size = -1
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	parent := path.Dir(clean)
	if parent == clean {
		parent = ""
	}
	return core.Listing{Path: clean, Parent: parent, Entries: entries}, nil
}

// newFromSFTP lets tests drive the engine over an in-process pipe pair
// without SSH.
func newFromSFTP(sc *sftp.Client) *Client {
	return &Client{sftp: sc}
}
