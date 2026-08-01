//go:build windows

package localfs

import "os"

// Roots enumerates available drive letters. A plain Stat probe avoids any
// syscall dependency and keeps the build CGO-free and pure-Go.
func Roots() []Root {
	roots := make([]Root, 0, 4)
	for letter := 'A'; letter <= 'Z'; letter++ {
		drive := string(letter) + `:\`
		if _, err := os.Stat(drive); err == nil {
			roots = append(roots, Root{Path: drive, Label: string(letter) + ":"})
		}
	}
	return roots
}
