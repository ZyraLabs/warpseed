//go:build windows

package main

import "os/exec"

// openInFileManager opens a folder in Explorer. explorer.exe returns a
// non-zero exit code even on success, so its status is deliberately ignored;
// a genuine failure surfaces as a start error.
func openInFileManager(dir string) error {
	cmd := exec.Command("explorer.exe", dir)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
