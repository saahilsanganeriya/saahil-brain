package agents

import (
	"bytes"
	"os"
	"strconv"
	"strings"
)

// processIsSame compares the registry's procStart with field 22 of /proc/<pid>/stat (start time in clock ticks),
// which is what Claude Code records on Linux.
func processIsSame(pid int, procStart string) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// the command name can hold spaces and parentheses; fields resume after the LAST ')'
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return false
	}
	fields := strings.Fields(string(raw[end+1:]))
	const startTimeIndex = 22 - 3 // stat field 22, counted after pid (1) and comm (2), so index 19 of the rest
	if len(fields) <= startTimeIndex {
		return false
	}
	if procStart == "" {
		return true
	}
	return fields[startTimeIndex] == procStart
}

func orcaHandleOf(pid int) string {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return ""
	}
	for _, kv := range bytes.Split(raw, []byte{0}) {
		if v, ok := bytes.CutPrefix(kv, []byte("ORCA_TERMINAL_HANDLE=")); ok {
			return string(v)
		}
	}
	return ""
}
