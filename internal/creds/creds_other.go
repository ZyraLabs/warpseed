//go:build !windows

package creds

// Default on non-Windows returns the in-memory store: the Linux dev loop can
// connect (passwords held for the session) but nothing persists. libsecret /
// Keychain implementations land in Phase 3.
func Default() Store { return NewMemory() }
