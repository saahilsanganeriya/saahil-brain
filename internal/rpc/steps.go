package rpc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"saahil.local/brain/internal/config"
)

// Step is one named brain command the other machine may start. The argv is fixed here; a caller only names it.
type Step struct {
	Args    []string
	Timeout time.Duration
	Only    config.Machine // empty: both machines
}

// AllowedSteps is the whole remote surface beyond status, sessions and notes.
var AllowedSteps = map[string]Step{
	"version": {Args: []string{"version"}, Timeout: time.Minute},
	"doctor":  {Args: []string{"doctor", "--json"}, Timeout: 5 * time.Minute},
	"install": {Args: []string{"install"}, Timeout: 10 * time.Minute},
	"index":   {Args: []string{"index"}, Timeout: 30 * time.Minute},
	"sync":    {Args: []string{"sync"}, Timeout: 30 * time.Minute, Only: config.Laptop},
	"update":  {Args: []string{"self-update"}, Timeout: 5 * time.Minute, Only: config.Laptop},
	// read-only views and the shadow-phase setup
	"agents-list":   {Args: []string{"agents", "list"}, Timeout: 2 * time.Minute},
	"init":          {Args: []string{"init"}, Timeout: time.Minute},
	"import-memory": {Args: []string{"import", "memory"}, Timeout: 10 * time.Minute},
	"sync-setup":    {Args: []string{"sync", "--setup"}, Timeout: 15 * time.Minute, Only: config.Laptop},
	"sync-dry-run":  {Args: []string{"sync", "--dry-run"}, Timeout: 30 * time.Minute, Only: config.Laptop},
	"sync-enable":   {Args: []string{"sync", "--enable"}, Timeout: 5 * time.Minute, Only: config.Laptop},
}

type Job struct {
	ID       string    `json:"id"`
	Step     string    `json:"step"`
	State    string    `json:"state"` // running | done | failed
	ExitCode int       `json:"exitCode"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
	LogTail  string    `json:"logTail,omitempty"`
}

type Steps struct {
	Dir     string
	Allowed map[string]Step
	// Exe is the binary a step runs; tests point it elsewhere.
	Exe string
}

func NewSteps() *Steps {
	exe, _ := os.Executable()
	return &Steps{Dir: filepath.Join(config.StateDir(), "jobs"), Allowed: AllowedSteps, Exe: exe}
}

// Start launches a step in its own session, so it outlives a one-shot `brain rpc` over ssh and a bridge restart.
func (s *Steps) Start(name string) (*Job, error) {
	step, found := s.Allowed[name]
	if !found {
		names := make([]string, 0, len(s.Allowed))
		for n := range s.Allowed {
			names = append(names, n)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("step %q is not allowed (allowed: %s)", name, strings.Join(names, ", "))
	}
	if step.Only != "" && step.Only != config.ThisMachine() {
		return nil, fmt.Errorf("step %q runs only on the %s", name, step.Only)
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return nil, err
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	job := &Job{ID: "j-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b), Step: name,
		State: "running", Started: time.Now().UTC()}
	if err := s.write(job); err != nil {
		return nil, err
	}
	cmd := exec.Command(s.Exe, "_step-run", job.ID, name)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		job.State, job.ExitCode, job.Ended = "failed", -1, time.Now().UTC()
		_ = s.write(job)
		return nil, err
	}
	_ = cmd.Process.Release()
	return job, nil
}

// RunJob is the body of `brain _step-run`: run the fixed argv, keep its output in the job log, record the exit.
func (s *Steps) RunJob(id, name string) int {
	job, err := s.Job(id)
	if err != nil {
		return 2
	}
	step := s.Allowed[name]
	logFile, err := os.OpenFile(filepath.Join(s.Dir, id+".log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 2
	}
	defer logFile.Close()
	cmd := exec.Command(s.Exe, step.Args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		job.State, job.ExitCode = "failed", -1
	} else {
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err = <-done:
		case <-time.After(step.Timeout):
			_ = cmd.Process.Kill()
			err = <-done
			fmt.Fprintf(logFile, "\nstep %s killed after %s\n", name, step.Timeout)
		}
		job.ExitCode = cmd.ProcessState.ExitCode()
		job.State = "done"
		if err != nil || job.ExitCode != 0 {
			job.State = "failed"
		}
	}
	job.Ended = time.Now().UTC()
	_ = s.write(job)
	return job.ExitCode
}

func (s *Steps) Job(id string) (*Job, error) {
	if !strings.HasPrefix(id, "j-") || strings.ContainsAny(id, "/\\") {
		return nil, fmt.Errorf("bad job id %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	if tail, err := os.ReadFile(filepath.Join(s.Dir, id+".log")); err == nil {
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		job.LogTail = string(tail)
	}
	return &job, nil
}

func (s *Steps) write(job *Job) error {
	saved := *job
	saved.LogTail = ""
	raw, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.Dir, "."+job.ID+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Dir, job.ID+".json"))
}
