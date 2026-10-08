package agents

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// processIsSame checks the pid is alive and, when procStart is a number, that it matches the process start time in
// one of the units Claude Code may record (seconds, milliseconds, microseconds). An unknown format falls back to
// "alive and a node/claude process", so a live laptop session is never hidden by a format change.
func processIsSame(pid int, procStart string) bool {
	if err := syscall.Kill(pid, 0); err != nil && err != syscall.EPERM {
		return false
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Proc.P_pid != int32(pid) {
		return false
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(procStart), 10, 64); err == nil {
		sec, usec := int64(kp.Proc.P_starttime.Sec), int64(kp.Proc.P_starttime.Usec)
		for _, candidate := range []int64{sec, sec*1000 + usec/1000, sec*1_000_000 + usec} {
			if n == candidate || n == candidate+1 || n == candidate-1 {
				return true
			}
		}
		return false
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	name := strings.ToLower(string(out))
	return err == nil && (strings.Contains(name, "claude") || strings.Contains(name, "node"))
}

// orcaHandleOf is empty on the laptop: another process's environment is not readable on macOS; the note sender
// falls back to the folder match against the local Orca terminal list.
func orcaHandleOf(int) string { return "" }
