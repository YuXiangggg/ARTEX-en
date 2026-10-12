package agent

import (
	"strings"

	"github.com/Autumn-27/norma/harness"
)

// Wrap-up / settlement prompt: when an agent is terminated because its [step budget is
// exhausted (MaxTurns)] or [it times out (run_seconds/MaxDuration)], the SDK settlement
// phase injects this prompt so the agent first persists identified-but-unwritten content
// and then outputs a one-sentence summary, avoiding an abrupt ending.
//
// Each agent's wrap-up prompt can be overridden in the backend as needed (stored in
// agents.wrapup_prompt); if empty, the built-in default here is used. Only the [prompt
// body] is editable; which tools are disabled and how many turns the wrap-up itself gets
// are code-fixed policy.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts, indexed by agent key. worker reuses the historically
// hardcoded settleWrapUpPrompt (defined in worker.go); planner/mainagent each have their
// own; a miss (custom agent) falls back to the generic default.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: the built-in default turn budget for each agent's [own] wrap-up
// phase (can be overridden by a backend value >0). All get 10 turns, ensuring the wrap-up
// phase has enough steps to persist. A miss falls back to genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your planning steps for this round are about to run out -- note this is only the end of [this round]; the system will wake you again later as the situation changes to keep planning. This is not task termination, and you need not wrap up the entire plan here. Land the conclusions you have already thought through this round so the round is not wasted, but [do not force intents just to wrap up] (0 intents this round is still a perfectly normal result): (1) if you have identified exploration directions that [should be dispatched right now], submit them in one add_intent batch (do not hold back what you have thought out); (2) for goals already proven met by some finding/fact, call prove_goal to mark them met (do not miss any); (3) if you have identified a serial exploitation chain that needs to be done step by step, record it with TodoWrite so the next wake-up can continue dispatching. When done, end the round directly, with no summary text needed."

const mainAgentWrapUpDefault = "Your steps are about to run out and this interaction is about to end. Do not start any new exploration/operation. Please **use a single plain-text sentence on its own** to summarize the current progress, key conclusions, and the suggested next step for the user."

const genericWrapUpDefault = "You are about to be terminated due to budget exhaustion. First write back any completed-but-unpersisted results, then **use a single plain-text sentence on its own** to summarize what you did and the key conclusions you reached (this sentence is shown as the result of this run)."

// WrapupDefault returns the built-in default wrap-up prompt for an agent key —
// used by the admin UI as the "restore default" value and empty-field placeholder.
func WrapupDefault(agentKey string) string {
	if d, ok := wrapupDefaults[agentKey]; ok {
		return d
	}
	return genericWrapUpDefault
}

// WrapupTurnsDefault returns the built-in wrap-up turn budget for an agent key —
// used by the admin UI as the "0 = default N" hint.
func WrapupTurnsDefault(agentKey string) int {
	if n, ok := wrapupTurnDefaults[agentKey]; ok {
		return n
	}
	return genericWrapupTurns
}

// resolveWrapup returns the effective wrap-up prompt: the DB override (if set and
// non-empty) over the built-in default.
func resolveWrapup(agentKey string) string {
	if WrapupOverride != nil {
		if t, ok := WrapupOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return WrapupDefault(agentKey)
}

// resolveWrapupTurns returns the effective wrap-up turn budget: a positive DB
// override over the built-in per-agent default.
func resolveWrapupTurns(agentKey string) int {
	if WrapupMaxTurnsOverride != nil {
		if v, ok := WrapupMaxTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return WrapupTurnsDefault(agentKey)
}

// wrapupSettlement builds the settlement config for an agent's run. Prompt and the
// turn budget are admin-editable per agent; disabled tools are code-owned policy so
// a user can't edit away the "stop probing" guardrail. Resolved fresh each run
// (reads DB live), so edits apply on the next run without a restart.
func wrapupSettlement(agentKey string, disabledTools []string) *harness.Settlement {
	return &harness.Settlement{
		Prompt:        resolveWrapup(agentKey),
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
}

// ---------- Task-level timeout wrap-up prompts (see docs/task-level-timeout-and-wrapup-design.md) ----------
//
// These are a [separate set] from the per-run wrap-up prompts: per-run means "the budget
// for this one run is used up"; task timeout means "the whole task has hit its deadline
// and is about to end". The semantics are often opposite (especially for the planner:
// per-run says "do not stop, keep planning", task timeout says "stop planning at the
// deadline and make the final judgment"). Configured only for worker/planner.

// WrapupTaskTimeoutOverride / ...TurnsOverride: DB overrides for the task-timeout wrap-up
// prompt and turn count (wired to agents.task_timeout_wrapup_prompt / _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not your per-run budget -- the entire exploration has hit its deadline). This is the last chance: (1) persist [all] content you have identified but not yet written back -- new assets via insert_assets, exploration conclusions/facts via record_fact, confirmed findings via report_finding; (2) do not start any new command/probe; (3) **finally, in a single plain-text sentence on its own**, summarize your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The whole task has reached its timeout limit and is about to end** (not this round -- the entire task is terminating). Based on [all] current facts and findings, make a final goal judgment: for goals already proven met by evidence, call prove_goal to mark them met (do not miss any). **Do not generate any new intents** (dispatched intents will no longer be executed at this point). Once judged, wrap up; no summary text is needed."

// TaskTimeoutWrapupDefault returns an agent's built-in default task-timeout wrap-up prompt (for backend placeholder/restore-default).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // Unconfigured (mainagent/chat) returns an empty string
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty string
// means the agent has no task-timeout prompt (non worker/planner), in which case the
// caller should fall back to the per-run prompt.
func resolveTaskTimeoutWrapup(agentKey string) string {
	if WrapupTaskTimeoutOverride != nil {
		if t, ok := WrapupTaskTimeoutOverride(agentKey); ok && strings.TrimSpace(t) != "" {
			return t
		}
	}
	return TaskTimeoutWrapupDefault(agentKey)
}

func resolveTaskTimeoutTurns(agentKey string) int {
	if WrapupTaskTimeoutTurnsOverride != nil {
		if v, ok := WrapupTaskTimeoutTurnsOverride(agentKey); ok && v > 0 {
			return v
		}
	}
	return resolveWrapupTurns(agentKey) // By default reuse the per-run turn count
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  -> this run is squeezed by the task deadline: wrap-up due to Timeout =
//     task deadline reached -> task-timeout prompt; wrap-up due to MaxTurns = steps ran out
//     within the squeeze window while minutes remain on the task -> fall back to per-run prompt.
//   - clamped=false -> the task is still far off: both reasons use the per-run prompt (i.e. degrades to wrapupSettlement).
//
// The PromptByReason handed to the harness picks by the [actual] reason at wrap-up time, avoiding a build-time mismatch.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // Fallback (also the value for both reasons when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // Task deadline reached
				harness.ReasonMaxTurns: perRun, // Steps ran out first while task time remains
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
