package hostkeys

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"

	"warpseed/internal/queue"
)

func testDB(t *testing.T) *queue.Store {
	t.Helper()
	s, err := queue.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	now := "2026-08-01T00:00:00Z"
	if _, err := s.DB().Exec(
		`INSERT INTO sites(id,name,protocol,host,created_at,updated_at) VALUES (1,'t','sftp','example.test',?,?)`,
		now, now); err != nil {
		t.Fatal(err)
	}
	return s
}

func testKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub
}

func TestTOFUAcceptPinsKey(t *testing.T) {
	// Arrange
	db := testDB(t)
	st := New(db.DB())
	key := testKey(t)
	prompts := 0
	cb := st.Callback(1, func(_, _ string) bool { prompts++; return true })

	// Act — first sight prompts and pins; second sight is silent
	if err := cb("h", nil, key); err != nil {
		t.Fatalf("first connect: %v", err)
	}
	if err := cb("h", nil, key); err != nil {
		t.Fatalf("second connect: %v", err)
	}

	// Assert
	if prompts != 1 {
		t.Fatalf("prompted %d times, want 1 (TOFU)", prompts)
	}
}

func TestTOFURejectDenies(t *testing.T) {
	// Arrange
	db := testDB(t)
	st := New(db.DB())
	cb := st.Callback(1, func(_, _ string) bool { return false })

	// Act
	err := cb("h", nil, testKey(t))

	// Assert
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("err = %v, want ErrRejected", err)
	}
}

func TestChangedKeyAlarms(t *testing.T) {
	// Arrange — pin one key, present another of the same algo
	db := testDB(t)
	st := New(db.DB())
	accept := st.Callback(1, func(_, _ string) bool { return true })
	if err := accept("h", nil, testKey(t)); err != nil {
		t.Fatal(err)
	}
	neverPrompt := st.Callback(1, func(_, _ string) bool {
		t.Fatal("changed key must never re-prompt TOFU")
		return true
	})

	// Act
	err := neverPrompt("h", nil, testKey(t))

	// Assert
	var changed *KeyChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("err = %v, want KeyChangedError", err)
	}
}
