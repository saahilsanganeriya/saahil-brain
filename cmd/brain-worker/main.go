// Command brain-worker runs the brain's Temporal workflows on the devbox's local dev server (systemd user unit).
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	tlog "go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
	"saahil.local/brain/internal/llm"
)

func main() {
	llmKind := flag.String("llm", "claude", "claude | fake (fake: no model, empty edits — tests and shadow checks)")
	claudeBin := flag.String("claude-bin", filepath.Join(config.BinDir(), "claude"), "the claude CLI")
	skills := flag.String("skills", "", "skills folder (default: the skills embedded in this binary)")
	brainBin := flag.String("brain-bin", filepath.Join(config.BinDir(), "brain"), "the brain CLI")
	shadowDocs := flag.String("shadow-docs", filepath.Join(config.Home(), "Documents", "work-repo", "saahil-docs"),
		"read-only doc root presented as work/ (until the cut-over)")
	address := flag.String("address", dream.HostPort, "local Temporal frontend (loopback only)")
	flag.Parse()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger, *llmKind, *claudeBin, *skills, *brainBin, *shadowDocs, *address); err != nil {
		logger.Error("brain-worker stopped", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, llmKind, claudeBin, skills, brainBin, shadowDocs, address string) error {
	if err := requireLoopback(address); err != nil {
		return err
	}
	var runner llm.Runner
	switch llmKind {
	case "claude":
		runner = llm.DefaultClaude()
		if claudeBin != filepath.Join(config.BinDir(), "claude") {
			runner = llm.Claude{Bin: claudeBin}
		}
	case "fake":
		runner = &llm.Fake{}
	default:
		return fmt.Errorf("--llm must be claude or fake, not %q", llmKind)
	}
	root := config.BrainRoot()
	ledger, err := llm.OpenLedger(filepath.Join(root, ".index", "brain.db"), filepath.Join(root, ".data", "llm"))
	if err != nil {
		return err
	}
	defer ledger.Close()
	shadow, forbidden := []string{shadowDocs}, []string{shadowDocs, config.ClaudeDir()}
	if !separateFrom(shadowDocs, root) {
		shadow, forbidden = nil, []string{config.ClaudeDir()}
	}
	acts := &dream.Activities{LLM: runner, Ledger: ledger, Cfg: dream.Config{
		BrainRoot: root, ShadowDocs: shadow, SkillsDir: skills, BrainBin: brainBin, Forbidden: forbidden,
	}}

	c, err := dial(logger, address)
	if err != nil {
		return err
	}
	defer c.Close()

	dreamWorker := worker.New(c, dream.TaskQueue, worker.Options{})
	dreamWorker.RegisterWorkflow(dream.DreamWorkflow)
	dreamWorker.RegisterWorkflow(dream.DreamTickWorkflow)
	dreamWorker.RegisterWorkflow(dream.EvalWorkflow)
	dreamWorker.RegisterWorkflow(dream.BackfillWorkflow)
	dreamWorker.RegisterWorkflow(dream.BackfillBatchWorkflow)
	dreamWorker.RegisterWorkflow(dream.LearnSessionWorkflow)
	dreamWorker.RegisterActivity(acts)
	// One Opus call at a time and two Haiku calls bound spend rate and memory; the queues share nothing else.
	opus := worker.New(c, dream.QueueOpus, worker.Options{MaxConcurrentActivityExecutionSize: 1})
	haiku := worker.New(c, dream.QueueHaiku, worker.Options{MaxConcurrentActivityExecutionSize: 2})
	for _, w := range []worker.Worker{opus, haiku} {
		w.RegisterActivityWithOptions(acts.SkillStep, activity.RegisterOptions{Name: "LLMStep"})
	}
	opus.RegisterActivityWithOptions(acts.BackfillCampaign, activity.RegisterOptions{Name: "BackfillCampaign"})
	opus.RegisterActivityWithOptions(acts.LearnReflect, activity.RegisterOptions{Name: "LearnReflect"})
	opus.RegisterActivityWithOptions(acts.MergeStep, activity.RegisterOptions{Name: "MergeStep"})
	haiku.RegisterActivityWithOptions(acts.BackfillCard, activity.RegisterOptions{Name: "BackfillCard"})
	haiku.RegisterActivityWithOptions(acts.LearnJudge, activity.RegisterOptions{Name: "LearnJudge"})
	for _, w := range []worker.Worker{dreamWorker, opus, haiku} {
		if err := w.Start(); err != nil {
			return err
		}
		defer w.Stop()
	}
	logger.Info("brain-worker running", "address", address, "namespace", dream.Namespace, "llm", llmKind,
		"brain", root, "skills", skills)
	<-worker.InterruptCh()
	return nil
}

// requireLoopback refuses any address that is not this machine: the brain's Temporal is the local dev server, never
// a company or cloud cluster.
func requireLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("refusing non-loopback Temporal address %q", address)
	}
	return nil
}

// dial waits up to a minute for the dev server, which systemd starts just before the worker.
func dial(logger *slog.Logger, address string) (client.Client, error) {
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		c, err := client.Dial(client.Options{HostPort: address, Namespace: dream.Namespace,
			Logger: tlog.NewStructuredLogger(logger)})
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("temporal at %s not reachable: %w", address, lastErr)
}

// separateFrom is true while the shadow docs are their own tree; after the cut-over the old path links into (or no
// longer exists beside) the brain, and keeping it read-only would refuse every write to the brain's own work/.
func separateFrom(shadowDocs, root string) bool {
	realShadow, err := filepath.EvalSymlinks(shadowDocs)
	if err != nil {
		return false
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return true
	}
	return realShadow != realRoot && !strings.HasPrefix(realShadow, realRoot+string(filepath.Separator))
}
