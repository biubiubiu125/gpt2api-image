//go:build linux || darwin || freebsd || netbsd || openbsd

package app

import "golang.org/x/sys/unix"

func imageDiskUsage(path string) (totalBytes int64, freeBytes int64) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, 0
	}
	blockSize := int64(stat.Bsize)
	return int64(stat.Blocks) * blockSize, int64(stat.Bavail) * blockSize
}
