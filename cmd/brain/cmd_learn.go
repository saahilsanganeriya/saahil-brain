package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
)

func init() {
	register("learn", "learn from one session now (Temporal): brain learn <session-id> — lessons and anti-patterns when it was corrected or failed", cmdLearn)
}

func cmdLearn(args []string) int {
	if len(args) != 1 {
		return fail("usage: brain learn <session-id>")
	}
	root := config.BrainRoot()
	short := strings.ReplaceAll(args[0], "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	notes, _ := filepath.Glob(filepath.Join(root, "sessions", "*", "*", "*", "*-"+short+"-*.md"))
	if len(notes) != 1 {
		return fail("%d session notes match %s under %s/sessions", len(notes), short, root)
	}
	rel, _ := filepath.Rel(root, notes[0])
	c, err := dialDream()
	if err != nil {
		return fail("temporal at %s: %v", dream.HostPort, err)
	}
	defer c.Close()
	id := dream.LearnID(rel)
	run, err := c.ExecuteWorkflow(context.Background(), client.StartWorkflowOptions{ID: id, TaskQueue: dream.TaskQueue,
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE},
		dream.LearnSessionWorkflow, dream.LearnInput{Session: rel, Apply: true})
	if err != nil {
		return fail("start %s: %v", id, err)
	}
	fmt.Fprintf(os.Stderr, "%s (run %s) started for %s; waiting…\n", id, run.GetRunID(), rel)
	var out dream.LearnResult
	err = run.Get(context.Background(), &out)
	out.Edits = nil // whole files; the applied count says what landed
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	if err != nil {
		return fail("%s: %v", id, err)
	}
	return 0
}
