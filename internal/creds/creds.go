// Package creds abstracts secret storage. Windows uses Credential Manager
// (DPAPI); other platforms get an in-memory store until libsecret/Keychain
// arrive in Phase 3. Site rows hold only references, never secrets.
package creds

import (
	"errors"
	"sync"
)

var ErrNotFound = errors.New("credential not found")

// Store is implemented per platform; all engine and app code depends on
// this interface so the Linux test loop runs without Windows.
type Store interface {
	Get(ref string) (string, error)
	Set(ref, secret string) error
	Delete(ref string) error
}

// Memory is the test/dev fake and the non-Windows fallback.
type Memory struct {
	mu sync.Mutex
	m  map[string]string
}

func NewMemory() *Memory {
	return &Memory{m: make(map[string]string)}
}

func (s *Memory) Get(ref string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[ref]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (s *Memory) Set(ref, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[ref] = secret
	return nil
}

func (s *Memory) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, ref)
	return nil
}
