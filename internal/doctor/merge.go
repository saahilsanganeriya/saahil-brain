package doctor

import (
	"context"
	"fmt"

	"saahil.local/brain/internal/config"
	"saahil.local/brain/internal/dream"
)

// The dream's curate.merge queues the rule pairs it cannot settle for the owner, and the coordinator relays them. The
// line is information only: questions that wait do not turn doctor red.
func init() {
	Register(Check{Name: "owner questions", Run: func(context.Context) (Status, string) {
		return ownerQuestions(config.BrainRoot())
	}})
}

func ownerQuestions(root string) (Status, string) {
	n, err := dream.WaitingOwnerQuestions(root)
	if err != nil {
		return Warn, err.Error()
	}
	if n == 0 {
		return OK, "none wait (.brain/owner-questions.jsonl)"
	}
	return OK, fmt.Sprintf("%d wait in .brain/owner-questions.jsonl for the coordinator to relay", n)
}
