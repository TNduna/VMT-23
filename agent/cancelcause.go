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
	AbortPausedByUser = cause("paused_by_user", "User paused the task",
		"The user paused the task via the task control endpoint (POST /api/tasks/{id}/control, action=pause). This Planner/Worker run was actively canceled; running intents return to frontier(open) and will be reclaimed and re-executed from the beginning after the task is resumed")
	AbortPausedByOrchestrator = cause("paused_by_orchestrator", "Orchestrator agent paused the task",
		"The orchestrator agent called the pause_task tool to pause this task. This Planner/Worker run was actively canceled; running intents return to frontier(open) and will be re-executed after resume")
	AbortTaskDeleted = cause("task_deleted", "Task was deleted",
		"The task is being deleted (DELETE /api/tasks/{id}), and the deletion barrier canceled the task's running Planner, Worker, and Main Agent; the result of this run will no longer be used")
	AbortPausedOnReload = cause("paused_on_reload", "Backend restored paused task state",
		"On startup, the backend restored the task's paused state from the database. This run was canceled; normally no agents are running during the recovery phase")
	AbortGoalMet = cause("goal_met", "Planner judged task goal met",
		"The planner determined that the task goal was met, marked the task as done, and subsequently canceled still-running Workers; these intents are marked as stopped rather than failed")
	AbortSettleDrainTimeout = cause("settle_drain_timeout", "Task timeout settle drain expired",
		"After reaching its timeout, the task waited for running Workers to wrap up gracefully, but the 90-second drain grace period expired and a hard cancel was issued; the intent is marked as exhausted, while facts and assets already written during wrap-up are preserved")

	// Per-work context.
	AbortKilledByPlanner = cause("killed_by_planner", "Planner terminated this intent",
		"The planner actively terminated this intent by calling kill_work, typically indicating the direction went off track or has no further value; the intent is marked as stopped and will not be automatically reclaimed")
	AbortWorkPausedByUser = cause("work_paused_by_user", "User paused this Worker intent",
		"The user paused the running Worker. This invocation was canceled and the intent transitioned to paused; all recorded intents, facts, findings, and activity records are preserved, and execution will restart from the beginning upon resume")
	AbortWorkCancelledByUser = cause("work_cancelled_by_user", "User deleted this Worker intent",
		"The user deleted the running Worker. This invocation was canceled; after the Worker exits the write zone, the server processes the intent according to the user's selected deletion mode — soft delete only marks it as deleted while preserving all outputs, whereas hard delete cascades to remove the intent and downstream nodes supported solely by it")
	AbortWorkFinished = cause("work_finished", "Worker finished and released context",
		"The Worker finished normally and the engine released its context resources in detachWork. This is not a run interruption; if it appears in an interruption message, a race occurred between cancellation and completion events")
	AbortPausedRaceGuard = cause("paused_race_guard", "Rejected new run while task paused",
		"While the task was paused, the engine refused to issue a new execution context to prevent a race between claim and pause from starting a Worker; the claimed intent is returned to frontier")

	// Main Agent and standalone conversation contexts.
	AbortChatStoppedByUser = cause("chat_stopped_by_user", "User stopped this chat turn",
		"The user clicked stop to actively abort the current Main Agent or Chat Agent turn. Activity records already generated are preserved, and you can continue by sending the next message")
	AbortChatPausedWithTask = cause("chat_paused_with_task", "Task paused and aborted main chat",
		"When the user paused the task, the running Main Agent conversation was also canceled synchronously. Activity records already generated are preserved; resuming the task will not automatically replay this turn's message")
	AbortChatTurnFinished = cause("chat_turn_finished", "Chat turn finished and released context",
		"This conversation turn finished normally and the server is releasing the turn's context resources. This is not a run interruption; if it appears in an interruption message, a race occurred between cancellation and completion events")

	// Process-level and per-run hard backstop.
	AbortShutdown = cause("shutdown", "Backend process is shutting down",
		"The backend process received SIGINT or SIGTERM and is restarting, updating, or shutting down. All running agents are canceled; any remaining running intents after restart will be reset to open and re-executed")
	AbortRunHardTimeout = cause("run_hard_timeout", "Single-run hard timeout triggered",
		"A single run exceeded its soft wall-clock budget plus the extra grace period, indicating that a model request or tool call hung for a long time and prevented normal turn-boundary settlement. Please inspect the last unreturned tool call before interruption")
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
		return "deadline_exceeded", "Upstream context reached deadline",
			"Upstream context reached its deadline, but the caller did not attach a named cause via WithTimeoutCause: " + c.Error(), true
	case errors.Is(c, context.Canceled):
		return "canceled_no_cause", "Caller did not attach a named cause",
			"Upstream context was canceled, but the caller did not attach a named cause via context.WithCancelCause; please register a cause in agent/cancelcause.go and wire it to that cancellation site", true
	default:
		return "other", firstLine(c.Error(), 80), c.Error(), true
	}
}
