//go:build windows

package localfs

import "golang.org/x/sys/windows"

// DiskSpace reports the free/total bytes of the volume holding path.
func DiskSpace(path string) (Space, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Space{}, err
	}
	var avail, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &totalFree); err != nil {
		return Space{}, err
	}
	return Space{Free: int64(avail), Total: int64(total)}, nil
}
