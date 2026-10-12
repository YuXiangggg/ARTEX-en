package agent

import (
	"context"
	"errors"
	"fmt"
)

// AbortCause names why an agent run's context was cancelled. Every cancellation
// site should attach one so the activity trace can report the real initiator.
type AbortCause struct {
	Code  string
	Short string
	Text  string
}

func (c *AbortCause) Error() string { return c.Text }

func cause(code, short, text string) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: text}
}

// Causef builds a cause that includes runtime-specific detail.
func Causef(code, short, format string, args ...any) *AbortCause {
	return &AbortCause{Code: code, Short: short, Text: fmt.Sprintf(format, args...)}
}

var (
	// Task-level execution context.
	AbortPausedByUser = cause("paused_by_user", "user paused the task",
		"The user paused the task via the task control interface (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was actively cancelled; the running intent returns to frontier(open) and, after the task resumes, is re-claimed and executed from the start")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "orchestrator Agent paused the task",
		"The orchestrator Agent called the pause_task tool to pause this task. This Planner/Worker run was actively cancelled; the running intent returns to frontier(open) and is re-executed after resume")
	AbortTaskDeleted = cause("task_deleted", "task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}); the deletion barrier cancelled the task's running Planner, Worker, and main Agent; this run's results will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "backend restored the task's paused state",
		"On startup the backend restored the task's paused state from the state persisted in the database. This run was cancelled; normally there is no running Agent during the restore phase")
	AbortGoalMet = cause("goal_met", "planner judged the task goal met",
		"The planner judged the task goal met and set the task to done, then cancelled the still-running Workers; these intents are marked stopped rather than failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "task-timeout wrap-up wait is used up",
		"After the task hit timeout it waited for the running Worker to wrap up gracefully, but the 90-second drain grace was still not enough, so a hard cancel was performed; the intent is marked exhausted, and facts and assets already written during the wrap-up phase are kept")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "planner terminated this intent",
		"The planner called kill_work to actively terminate this intent, usually meaning the direction went off track or had no further value; the intent is marked stopped and is not auto re-claimed")
	AbortWorkPausedByUser = cause("work_paused_by_user", "user paused this Worker intent",
		"The user paused the running Worker. This call was cancelled and the intent becomes paused; already-registered intents, facts, findings, and activity records are all kept, and after resume it is re-executed from the start")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "user deleted this Worker intent",
		"The user deleted the running Worker. This call was cancelled; after the Worker exits the write region, the server handles the intent per the deletion mode the user chose -- a soft delete only marks it deleted and keeps all output, while a hard delete cascades to remove the intent and the downstream nodes supported only by it")
	AbortWorkFinished = cause("work_finished", "Worker finished, context released",
		"The Worker finished normally and the engine released its context resources in detachWork. This is not a run interruption; if it appears in an interruption message, it means the cancel and the teardown event raced")
	AbortPausedRaceGuard = cause("paused_race_guard", "refused to start a new run while paused",
		"While the task is paused, the engine refuses to issue a new execution context, to prevent a race between claim and pause from letting a Worker keep starting; an already-claimed intent returns to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "user stopped this conversation turn",
		"The user clicked stop, actively aborting this run of the main Agent or conversation Agent. Activity records already produced are kept, and the next message can still be sent")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "task pause aborted main Agent chat",
		"When the user paused the task, the running main Agent conversation was cancelled in sync. Activity records already produced are kept; after the task resumes, this turn's message is not auto replayed")
	AbortChatTurnFinished = cause("chat_turn_finished", "chat turn finished, context released",
		"This conversation turn finished normally and the server is releasing the turn's context resources. This is not a run interruption; if it appears in an interruption message, it means the cancel and the teardown event raced")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "backend process is shutting down",
		"The backend process received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running Agents are cancelled; after restart, leftover running intents are reset to open and re-executed")
	AbortRunHardTimeout = cause("run_hard_timeout", "per-run hard-timeout backstop fired",
		"A single run exceeded the soft wall-clock budget plus extra grace, meaning the model request or some tool did not return for a long time, so the normal turn-boundary wrap-up could not run. Focus on checking the last tool call before the interruption that did not return")
)

// AbortReason resolves the named cause attached to a cancelled run context.
func AbortReason(ctx context.Context) (code, short, text string, ok bool) {
	c := context.Cause(ctx)
	if c == nil {
		return "", "", "", false
	}
	var ac *AbortCause
	if errors.As(c, &ac) {
		return ac.Code, ac.Short, ac.Text, true
	}
	switch {
	case errors.Is(c, context.DeadlineExceeded):
		return "deadline_exceeded", "upstream context reached its deadline",
			"The upstream context reached its deadline, but the setter did not attach a named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "canceller attached no named cause",
			"The upstream context was cancelled, but the canceller did not attach a named cause via context.WithCancelCause; please register a cause in agent/cancelcause.go and wire it into that cancellation point", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
