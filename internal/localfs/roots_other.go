//go:build !windows

package localfs

// Roots on POSIX systems is the single filesystem root.
func Roots() []Root {
	return []Root{{Path: "/", Label: "/"}}
}
