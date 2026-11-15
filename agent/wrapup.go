package agent

import (
	"strings"

	"github.com/TNduna/norma/harness"
)

// Wrap-up / settlement prompt: when an agent is terminated due to [turn budget exhaustion
// (MaxTurns)] or [timeout (run_seconds/MaxDuration)], the SDK's settlement phase injects
// this prompt so the agent first persists any identified but unwritten findings/facts/assets
// to the database and then outputs a one-sentence summary, avoiding an abrupt cutoff.
//
// Each agent's wrap-up prompt can be overridden on demand in the admin UI (stored in
// agents.wrapup_prompt); leaving it empty uses the built-in default here. Only the [prompt
// body] is editable; which tools are disabled and how many turns the settlement phase gets
// are fixed code policies.

// WrapupOverride, if set, returns the stored wrap-up prompt for an agent key and
// whether a non-empty one exists. Wired by the server to the agents table (like
// PromptOverride for system prompts). nil / empty → the built-in default is used.
var WrapupOverride func(agentKey string) (string, bool)

// WrapupMaxTurnsOverride, if set, returns the admin-configured turn budget for the
// wrap-up phase of an agent and whether a positive one exists. Wired to the agents
// table. nil / ≤0 → the built-in per-agent default (wrapupTurnDefaults) is used.
var WrapupMaxTurnsOverride func(agentKey string) (int, bool)

// Built-in default wrap-up prompts indexed by agent key. worker reuses the historically
// hardcoded settleWrapUpPrompt (defined in worker.go), while planner/mainagent each have
// their own version; unmatched keys (custom agents) fall back to genericWrapUpDefault.
var wrapupDefaults = map[string]string{
	"worker":    settleWrapUpPrompt,
	"planner":   plannerWrapUpDefault,
	"mainagent": mainAgentWrapUpDefault,
}

// wrapupTurnDefaults: built-in default turn budgets for each agent's settlement phase
// itself (overridable by >0 in admin UI). All default to 10 turns so the settlement phase
// has enough steps to persist results. Unmatched keys use genericWrapupTurns.
var wrapupTurnDefaults = map[string]int{
	"worker":    10,
	"planner":   10,
	"mainagent": 10,
}

const genericWrapupTurns = 10

const plannerWrapUpDefault = "Your planning step budget for this round is about to run out — note that only [this round] is ending; the system will wake you again to continue planning as the situation changes, so the task is not terminating and you do not need to wrap up the entire plan here. Please persist the conclusions you have already worked out in this round so the round is not wasted, but [do not force or pad intents just to wrap up] (producing 0 intents this round is still a completely normal outcome): (1) if you have already identified exploration directions that [should be dispatched right now], submit them in a single batch via add_intent (do not hold back directions you have already decided on); (2) for goals proven met by a finding or fact, call prove_goal to mark them met (do not miss them); (3) if you identified a serial exploitation chain that needs to be dispatched step by step, record it with TodoWrite so you can continue dispatching it on the next wake-up. Once done, end this round directly without outputting summary text."

const mainAgentWrapUpDefault = "Your step budget is about to run out and this interaction is ending. Do not initiate any new exploration or operations. Please **output a single plain-text sentence** summarizing current progress, key conclusions, and suggested next steps for the user."

const genericWrapUpDefault = "You are about to be terminated due to budget exhaustion. Please first persist any completed but unwritten results, then **output a single plain-text sentence** summarizing what you did and the key conclusions reached (this sentence will be displayed as the result of this run)."

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

// ---------- Task-level timeout wrap-up prompts (see task-level timeout & settlement design doc) ----------
//
// Separate from per-run wrap-up prompts: per-run means "your budget for this single run is up";
// task timeout means "the entire task has reached its deadline and is about to end". Their
// semantics are often opposite (especially for planner: per-run says "keep planning on future
// wakes", whereas task timeout says "stop planning and make final goal judgments"). Configured
// only for worker/planner.

// WrapupTaskTimeoutOverride / …TurnsOverride: DB overrides for task-timeout wrap-up prompt
// and turn budget (wired to agents.task_timeout_wrapup_prompt / _max_turns, worker/planner only).
var (
	WrapupTaskTimeoutOverride      func(agentKey string) (string, bool)
	WrapupTaskTimeoutTurnsOverride func(agentKey string) (int, bool)
)

var taskTimeoutWrapupDefaults = map[string]string{
	"worker":  workerTaskTimeoutDefault,
	"planner": plannerTaskTimeoutDefault,
}

const workerTaskTimeoutDefault = "**The entire task has reached its timeout limit and is about to end** (this is not just your current run's budget — the whole exploration has reached its deadline). This is your last chance: (1) persist [all] findings and observations you have identified but not yet written back — new assets via insert_assets, exploration conclusions/facts via record_fact, confirmed vulnerabilities via report_finding; (2) do not start any new commands or probes; (3) **finally, output a single plain-text sentence** summarizing your key conclusions on this intent."

const plannerTaskTimeoutDefault = "**The entire task has reached its timeout limit and is about to end** (not just this round — the whole task is terminating). Based on [all] current facts and findings, make one final goal evaluation: call prove_goal to mark any goal proven met by evidence (do not miss any). **Do not generate any new intents** (any intents dispatched now will not be executed). Finish immediately after judging goals, without outputting summary text."

// TaskTimeoutWrapupDefault returns the built-in default task-timeout wrap-up prompt for an agent
// (used by the admin UI for placeholders / restoring defaults).
func TaskTimeoutWrapupDefault(agentKey string) string {
	return taskTimeoutWrapupDefaults[agentKey] // unconfigured agents (mainagent/chat) return empty string
}

// resolveTaskTimeoutWrapup: DB override (non-empty) > built-in default. An empty string means
// the agent has no task-timeout prompt (non-worker/planner), in which case callers fall back
// to the per-run prompt.
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
	return resolveWrapupTurns(agentKey) // default to the per-run turn budget
}

// wrapupSettlementForTask builds settlement for a worker/planner run that is aware
// of the task deadline. See §5 of the design doc:
//   - clamped=true  → this run was clamped by the task deadline: settling due to Timeout means
//     the task deadline arrived → use task-timeout prompt; settling due to MaxTurns means steps
//     ran out first within the clamped window while task time remains → fall back to per-run prompt.
//   - clamped=false → task deadline is still far away: both reasons use the per-run prompt
//     (degenerates to wrapupSettlement).
//
// PromptByReason on the harness selects the prompt at settlement time based on the [actual]
// terminal reason, avoiding build-time mismatches.
func wrapupSettlementForTask(agentKey string, disabledTools []string, clamped bool) *harness.Settlement {
	perRun := resolveWrapup(agentKey)
	st := &harness.Settlement{
		Prompt:        perRun, // fallback (and value for both reasons when not clamped)
		DisabledTools: disabledTools,
		MaxTurns:      resolveWrapupTurns(agentKey),
	}
	if clamped {
		if tt := resolveTaskTimeoutWrapup(agentKey); tt != "" {
			st.PromptByReason = map[harness.TerminalReason]string{
				harness.ReasonTimeout:  tt,     // task deadline reached
				harness.ReasonMaxTurns: perRun, // step budget exhausted first while task time remains
			}
			st.MaxTurns = resolveTaskTimeoutTurns(agentKey)
		}
	}
	return st
}
