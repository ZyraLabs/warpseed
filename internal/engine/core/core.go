// Package core holds the engine-neutral transfer model. Both engines
// (sftpfast, rcadapter) speak these types; nothing here imports an engine,
// Wails, or the queue.
package core

import "strings"

// Entry mirrors localfs.Entry's wire shape so both panes render one type.
type Entry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`    // -1 for directories
	ModTime string `json:"modTime"` // RFC3339 UTC
	Mode    string `json:"mode"`
}

// Listing is one remote directory read.
type Listing struct {
	Path    string  `json:"path"`
	Parent  string  `json:"parent"`
	Entries []Entry `json:"entries"`
}

// ErrClass buckets failures for the retry ladder (approved plan: two-level
// retry; transient errors requeue, permanent errors surface).
type ErrClass int

const (
	ClassTransient ErrClass = iota // network blips, timeouts — retry with backoff
	ClassCapacity                  // server connection caps — shrink pool + requeue
	ClassAuth                      // bad credentials — surface immediately
	ClassHostKey                   // pin mismatch — surface loudly, never retry
	ClassPermanent                 // everything else that retrying won't fix
)

// Classify maps an error to a retry class. String matching is deliberate:
// SSH/SFTP libraries return opaque errors and the wire messages are the only
// stable signal (same approach as rclone and FileZilla).
func Classify(err error) ErrClass {
	if err == nil {
		return ClassPermanent
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "host key changed") || strings.Contains(msg, "key mismatch"):
		return ClassHostKey
	case strings.Contains(msg, "administratively prohibited"),
		strings.Contains(msg, "connection refused"),
		strings.Contains(msg, "too many connections"):
		return ClassCapacity
	case strings.Contains(msg, "unable to authenticate"),
		strings.Contains(msg, "permission denied"),
		strings.Contains(msg, "auth"):
		return ClassAuth
	case strings.Contains(msg, "timeout"),
		strings.Contains(msg, "connection reset"),
		strings.Contains(msg, "broken pipe"),
		// pkg/sftp's word for a dead session (request-errors.go: "connection
		// lost"). It used to match nothing here and fall through to
		// Permanent, so the single most transient failure there is — a
		// dropped connection mid-transfer — failed the row outright instead
		// of retrying it.
		strings.Contains(msg, "connection lost"),
		strings.Contains(msg, "connection closed"),
		// What net/http and crypto/ssh say when we tear a connection down
		// underneath a copy that is still running.
		strings.Contains(msg, "use of closed network connection"),
		strings.Contains(msg, "eof"):
		return ClassTransient
	default:
		return ClassPermanent
	}
}
