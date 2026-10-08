package evals

import (
	"time"

	"golang.org/x/sys/unix"
)

// statTimes is the file's birth (ext4 keeps it) and modification time; zero when unknown or missing.
func statTimes(path string) (born, modified time.Time) {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME|unix.STATX_MTIME, &stx); err != nil || stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}, time.Time{}
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec)), time.Unix(stx.Mtime.Sec, int64(stx.Mtime.Nsec))
}
