package applog

import (
	"log"
	"sync/atomic"
)

// verbose gates engine-level detail: per-lane ranges, first-write latency,
// cancel requested vs honoured. Off by default so warpseed.log stays a
// readable timeline; a user chasing a specific problem turns it on in
// Settings → About and sends the result.
var verbose atomic.Bool

// SetVerbose switches debug logging on or off. Safe from any goroutine.
func SetVerbose(on bool) { verbose.Store(on) }

// Verbose reports whether debug lines are currently being written.
func Verbose() bool { return verbose.Load() }

// Debugf writes a line only while verbose logging is on. It goes through the
// standard logger, so it lands in the same rotating file as everything else
// and never on stderr in a GUI build.
func Debugf(format string, args ...any) {
	if verbose.Load() {
		log.Printf("debug: "+format, args...)
	}
}
