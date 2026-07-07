//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd

package app

func imageDiskUsage(path string) (totalBytes int64, freeBytes int64) {
	return 0, 0
}
