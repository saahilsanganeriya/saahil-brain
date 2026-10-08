//go:build !linux

package evals

import "time"

// statTimes is unknown off Linux; the evals then treat every file as older than every session.
func statTimes(string) (born, modified time.Time) { return time.Time{}, time.Time{} }
