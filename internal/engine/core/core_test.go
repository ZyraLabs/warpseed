package core

import (
	"errors"
	"fmt"
	"testing"
)

// TestClassifyRetriesADroppedConnection — a dead session is the most transient
// failure a transfer can hit, and it must come back through the retry ladder
// rather than failing the row. pkg/sftp words it "connection lost", which
// matched none of the transient patterns and fell through to Permanent: an
// overnight upload that lost its connection stayed failed until someone
// noticed and retried it by hand.
func TestClassifyRetriesADroppedConnection(t *testing.T) {
	transient := []string{
		// Exactly as it reaches us, wrapped by the chunk that failed.
		"sync chunk 1: connection lost",
		"connection lost",
		"write /remote/big.mkv: connection closed",
		"read tcp 10.0.0.2:22: use of closed network connection",
		"i/o timeout",
		"connection reset by peer",
		"broken pipe",
		"unexpected EOF",
	}
	for _, msg := range transient {
		t.Run(msg, func(t *testing.T) {
			if got := Classify(errors.New(msg)); got != ClassTransient {
				t.Fatalf("Classify(%q) = %v, want ClassTransient — the transfer "+
					"would fail outright instead of retrying", msg, got)
			}
		})
	}

	// Wrapping must not change the answer: every real failure arrives inside
	// at least one fmt.Errorf.
	wrapped := fmt.Errorf("upload %q: %w", "/remote/big.mkv",
		fmt.Errorf("sync chunk 3: %w", errors.New("connection lost")))
	if got := Classify(wrapped); got != ClassTransient {
		t.Fatalf("wrapped connection loss classified %v, want ClassTransient", got)
	}

	// And the classes that must NOT be softened into a retry.
	if got := Classify(errors.New("host key changed")); got != ClassHostKey {
		t.Fatalf("host key change classified %v", got)
	}
	if got := Classify(errors.New("unable to authenticate")); got != ClassAuth {
		t.Fatalf("auth failure classified %v", got)
	}
}
