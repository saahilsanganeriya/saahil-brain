//go:build !linux

package evals

import (
	"os"
	"time"
)

// statTimes has no birth time off Linux, so the evals keep every file they find as older than every session.
func statTimes(path string) (born, modified time.Time) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, time.Time{}
	}
	return time.Time{}, fi.ModTime()
}
