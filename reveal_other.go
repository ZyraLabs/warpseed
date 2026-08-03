//go:build !windows

package main

import "os/exec"

// openInFileManager opens a folder in the desktop's file manager. Used by
// the Linux dev loop; the shipping target is Windows.
func openInFileManager(dir string) error {
	cmd := exec.Command("xdg-open", dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
