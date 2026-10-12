package harness

import (
	"context"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// On hitting MaxTurns with a Settlement configured, the loop injects the wrap-up
// prompt, hides DisabledTools during wrap-up, and finishes with the ORIGINAL
// budget reason (not ReasonCompleted).
func TestSettlementOnBudgetHit(t *testing.T) {
	var toolsSeen [][]string
	turns := [][]llm.StreamEvent{
		toolTurn("a", "Bash", `{"command":"echo 1"}`), // turn 1 → hits MaxTurns=1
		textTurn("facts written back and summarized"), // settlement turn → natural stop
	}
	call := func(_ context.Context, req llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
		names := make([]string, 0, len(req.Tools))
		for _, ts := range req.Tools {
			names = append(names, ts.Name)
		}
		i := len(toolsSeen)
		toolsSeen = append(toolsSeen, names)
		evs := turns[i]
		return func(yield func(llm.StreamEvent, error) bool) {
			for _, ev := range evs {
				if !yield(ev, nil) {
					return
				}
			}
		}
	}
	in := QueryInput{
		Messages:       []llm.Message{llm.UserText("go")},
		Tools:          tool.NewRegistry(tool.NewBash()),
		PermissionMode: permission.ModeBypass,
		WorkingDir:     t.TempDir(),
		MaxTurns:       1,
		Settlement:     &Settlement{Prompt: "SETTLE_NOW please write back the facts", DisabledTools: []string{"Bash"}},
	}
	term := drain(t, in, QueryDeps{CallModel: call})

	if term.Reason != ReasonMaxTurns {
		t.Fatalf("reason=%v, want ReasonMaxTurns (after settlement still reports the original budget reason)", term.Reason)
	}
	found := false
	for _, m := range term.Messages {
		for _, c := range m.Content {
			if strings.Contains(c.Text, "SETTLE_NOW") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("settlement prompt was not injected into the conversation")
	}
	if len(toolsSeen) < 2 {
		t.Fatalf("expected at least 2 model calls (main+settlement), got %d", len(toolsSeen))
	}
	if !contains(toolsSeen[0], "Bash") {
		t.Fatalf("main phase should see Bash: %v", toolsSeen[0])
	}
	if contains(toolsSeen[1], "Bash") {
		t.Fatalf("settlement phase should hide Bash: %v", toolsSeen[1])
	}
}

// With no Settlement, hitting the budget finishes immediately (unchanged behavior).
func TestNoSettlementFinishesImmediately(t *testing.T) {
	m := &scriptedModel{turns: [][]llm.StreamEvent{
		toolTurn("a", "Bash", `{"command":"echo 1"}`),
		toolTurn("b", "Bash", `{"command":"echo 2"}`),
	}}
	in := QueryInput{
		Tools:          tool.NewRegistry(tool.NewBash()),
		PermissionMode: permission.ModeBypass,
		WorkingDir:     t.TempDir(),
		MaxTurns:       1,
	}
	term := drain(t, in, QueryDeps{CallModel: m.call})
	if term.Reason != ReasonMaxTurns {
		t.Fatalf("reason=%v", term.Reason)
	}
	if m.calls != 1 {
		t.Fatalf("without settlement the model should be called only once, got %d", m.calls)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
