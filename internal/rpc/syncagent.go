package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"saahil.local/brain/internal/config"
)

// SyncAgentLabel is the laptop's hourly `brain sync` LaunchAgent.
const SyncAgentLabel = "com.saahil.brain-sync"

// SyncAgentLog is where the hourly agent's runs print.
func SyncAgentLog() string { return filepath.Join(config.Home(), "Library", "Logs", "brain-sync.log") }

// SyncAgentState says what launchd holds for the hourly sync agent (empty off macOS); tests replace it.
var SyncAgentState = func(ctx context.Context) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), SyncAgentLabel)).CombinedOutput()
	agent := parseLaunchctlPrint(string(out))
	var elapsed, children string
	if agent.pid > 0 {
		pid := strconv.Itoa(agent.pid)
		if b, err := exec.CommandContext(ctx, "ps", "-o", "etime=", "-p", pid).Output(); err == nil {
			elapsed = string(b)
		}
		if b, err := exec.CommandContext(ctx, "pgrep", "-l", "-P", pid).Output(); err == nil {
			children = string(b)
		}
	}
	return describeSyncAgent(agent, elapsed, children, lastLine(SyncAgentLog()))
}

// withSyncAgent adds the hourly agent's state to the laptop's sync record, so the devbox reads why a sync is stale.
func withSyncAgent(ctx context.Context, record []byte) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(record, &fields) != nil {
		return record
	}
	agent, _ := json.Marshal(SyncAgentState(ctx))
	fields["agent"] = agent
	out, err := json.Marshal(fields)
	if err != nil {
		return record
	}
	return out
}

type launchdAgent struct {
	loaded   bool
	state    string
	pid      int
	runs     string
	lastExit string
}

// parseLaunchctlPrint reads the agent's own top-level keys; nested blocks (event triggers, endpoints) have their own.
func parseLaunchctlPrint(out string) launchdAgent {
	var agent launchdAgent
	depth := 0
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasSuffix(t, "{") {
			depth++
			agent.loaded = true
			continue
		}
		if t == "}" {
			depth--
			continue
		}
		key, value, found := strings.Cut(t, " = ")
		if !found || depth != 1 {
			continue
		}
		switch key {
		case "state":
			agent.state = value
		case "pid":
			agent.pid, _ = strconv.Atoi(value)
		case "runs":
			agent.runs = value
		case "last exit code":
			agent.lastExit = value
		}
	}
	return agent
}

func describeSyncAgent(agent launchdAgent, elapsed, children, logTail string) string {
	head := "hourly agent " + SyncAgentLabel
	if !agent.loaded {
		return head + " not loaded (brain peer step sync-enable loads it)"
	}
	var s string
	if agent.pid > 0 {
		s = fmt.Sprintf("%s running pid %d for %s", head, agent.pid, strings.TrimSpace(elapsed))
		if kids := strings.Fields(children); len(kids) > 0 {
			s += " (children: " + strings.Join(kids, " ") + ")"
		}
		s += fmt.Sprintf(", runs %s: launchd starts no new run while this one lives (brain peer step sync-enable restarts it)", agent.runs)
	} else {
		s = fmt.Sprintf("%s waiting (%s), runs %s, last exit %s", head, agent.state, agent.runs, agent.lastExit)
	}
	if logTail != "" {
		s += "; log: " + logTail
	}
	return s
}

// lastLine is the file's last non-empty line, at most 200 bytes of it.
func lastLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 64<<10 {
		_, _ = f.Seek(info.Size()-64<<10, 0)
	}
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if t := strings.TrimSpace(sc.Text()); t != "" {
			last = t
		}
	}
	if len(last) > 200 {
		last = last[len(last)-200:]
	}
	return last
}
