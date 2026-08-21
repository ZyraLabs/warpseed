//go:build !windows

package localfs

import "golang.org/x/sys/unix"

// DiskSpace reports the free/total bytes of the volume holding path.
func DiskSpace(path string) (Space, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Space{}, err
	}
	bs := int64(st.Bsize)
	return Space{Free: int64(st.Bavail) * bs, Total: int64(st.Blocks) * bs}, nil
}
