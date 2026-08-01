//go:build windows

package creds

import (
	"errors"

	"github.com/danieljoos/wincred"
)

// targetPrefix namespaces warpseed entries in Windows Credential Manager.
const targetPrefix = "warpseed/"

// wincredStore persists secrets via Credential Manager (DPAPI-backed,
// per-user) — the approved plan's Windows credential layer.
type wincredStore struct{}

// Default returns the production store on Windows.
func Default() Store { return wincredStore{} }

func (wincredStore) Get(ref string) (string, error) {
	cred, err := wincred.GetGenericCredential(targetPrefix + ref)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return "", ErrNotFound
		}
		return "", err
	}
	return string(cred.CredentialBlob), nil
}

func (wincredStore) Set(ref, secret string) error {
	cred := wincred.NewGenericCredential(targetPrefix + ref)
	cred.CredentialBlob = []byte(secret)
	cred.Persist = wincred.PersistLocalMachine
	return cred.Write()
}

func (wincredStore) Delete(ref string) error {
	cred, err := wincred.GetGenericCredential(targetPrefix + ref)
	if err != nil {
		if errors.Is(err, wincred.ErrElementNotFound) {
			return nil
		}
		return err
	}
	return cred.Delete()
}
