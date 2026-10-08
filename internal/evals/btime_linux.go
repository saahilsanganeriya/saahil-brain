package evals

import (
	"time"

	"golang.org/x/sys/unix"
)

// birthTime is the file's creation time (ext4 keeps it); zero when the filesystem does not record it.
func birthTime(path string) time.Time {
	var stx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &stx); err != nil || stx.Mask&unix.STATX_BTIME == 0 {
		return time.Time{}
	}
	return time.Unix(stx.Btime.Sec, int64(stx.Btime.Nsec))
}
