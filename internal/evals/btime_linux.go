package evals

import (
	"time"

	"golang.org/x/sys/unix"
)

// statTimes is the file's birth (ext4 keeps it) and modification time: both zero when there is no file, born zero
// when the filesystem keeps no birth time.
func statTimes(path string) (born, modified time.Time) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME|unix.STATX_MTIME, &stx); err != nil {
		return time.Time{}, time.Time{}
	}
	modified = time.Unix(stx.Mtime.Sec, int64(stx.Mtime.Nsec))
	if stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, modified
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec)), modified
}
