//go:build !linux

package evals

import "time"

// birthTime is unknown off Linux; the target filter then keeps every file.
func birthTime(string) time.Time { return time.Time{} }
