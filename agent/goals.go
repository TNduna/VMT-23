package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TNduna/VMT-23/db"
	"github.com/TNduna/norma/agentcore"
	"github.com/TNduna/norma/llm"
	acperm "github.com/TNduna/norma/permission"
	actool "github.com/TNduna/norma/tool"
	"github.com/TNduna/norma/transcript"
)

// goalsDefaultTmpl is the built-in EDITABLE body (section [A]) of the goals-decomposer
// prompt, seeded into agent_prompts. No template vars are used today.
const goalsDefaultTmpl = `You are a penetration testing goal decomposer. Your responsibility is to identify the **final outcomes to be achieved** from the user's input, rather than planning attack steps.

**Step 1 (Do this BEFORE splitting goals): Extract Operation Constraints**
Identify explicit rules from the "Task Goal / Task Description" specifying [what operations are allowed and what operations are prohibited], and call set_constraints to register them one by one (if the description and goal do not mention any operation constraints, skip constraint extraction):
- type=deny: Prohibited operations (e.g., "do not scan ports", "do not perform write/delete operations on production", "no brute-forcing", "do not touch a specific subdomain").
- type=allow: Explicitly permitted/restricted operational scope (e.g., "passive reconnaissance only", "only target a specific domain").
- Constraints ≠ goals, and ≠ attack steps: they define operational behavior boundaries.
- **Constraints must be [self-contained with concrete targets hardcoded]**: Replace pronouns such as "current target / current port / current IP / current domain / this site" with the **concrete values** from the task goal/description. Constraints are injected standalone into execution-phase prompts, where pronouns cannot be resolved without context.
  Example: If the target is https://abc.example.net → write "Only test abc.example.net" rather than "Only test the current target"; write "Only test target port 443, do not scan other ports" rather than "Only test the current port". If the original text says "current target" and the target address is specified, substitute the actual address.
- **Only register constraints that are [explicitly stated or emphasized] in the goal/description — never fabricate**; when unsure between allow and deny, use deny (more conservative).
- If there are genuinely no operation constraints in the goal/description, **do not** call set_constraints.
After registering constraints (if any), proceed to goal decomposition below.

**Goal = Final deliverable / verifiable outcome**

**What is NOT a goal (do not list as sub-goals)**:
- Information gathering, reconnaissance, endpoint scanning
- Vulnerability analysis and verification processes
- Attack steps, exploitation techniques
- Result verification steps

**Decomposition Principles**:
- If the user describes only one final goal → output one
- If there are multiple **mutually independent** final deliverables → list them separately
- If a goal maps to a specific vulnerability class, tag ` + "`vulnclass`" + `; leave ` + "`vulnclass`" + ` empty for information-gathering or business-logic goals
- Strictly do not fabricate goals not mentioned by the user

Call set_goals to submit the results.`

// goalsScopeTail is the code-owned tail appended after the editable goals body
// WHEN an asset store + task context are available. It teaches the decomposer to
// also lift the explicit asset scope out of the goal/description and register it
// via add_task_scope. Kept in code (not the DB-editable body) so it always applies
// on released DBs and can't be edited away — same pattern as the trafficTool tail.
const goalsScopeTail = `

**Additional Responsibility: Register Test Asset Scope**
In addition to decomposing goals, you must identify the **explicitly specified test asset scope** from the "Task Goal / Task Description" and call add_task_scope to register it (this is the task's authorization boundary and the denominator for asset test coverage). **Principle of Minimal Scope: only register the exact target(s) explicitly named by the user, and never broaden the scope on your own.**
- If the target is a URL or an address with a hostname (e.g., https://xxx.example.com/path, app.example.com) → extract its **full hostname**, with kind=subdomain, value=full hostname.
  Example: Target https://a1b2c3.lab.example.net/path → kind=subdomain, value=a1b2c3.lab.example.net (**not** example.net).
  **Strictly do not** reduce a subdomain hostname to its root domain — registering all of example.com when given xxx.example.com expands scope beyond the user's target and violates the minimal scope principle.
- Only when the user provides a **bare root domain without any subdomain** (e.g., literally writing example.com), or explicitly states "entire site / all subdomains / whole domain" → use kind=root_domain, value=example.com.
- Pure IP or CIDR block → kind=ip / cidr, value=IP or CIDR.
- **Do not** register company scope (company) — the task was just created and the company usually does not exist in the asset store yet; company-level scope is handled later during the planning phase.
Other rules:
- Only register scope **explicitly written in the goal/description**; strictly do not fabricate or infer unmentioned domains/IPs.
- Briefly state in reason which sentence it came from for auditability.
- If no explicit asset scope is present in the goal/description, **do not** call add_task_scope.
First call add_task_scope to register the scope (if any), then call set_goals to submit the goals.`

// GoalSpec is one decomposed objective.
type GoalSpec struct {
	Text      string `json:"text"`
	VulnClass string `json:"vulnclass,omitempty"`
}

// DecomposeGoals asks the LLM to break a pentest task goal into discrete,
// independently-verifiable objectives (each becomes a goal node). Returns nil if
// no provider is configured or the call yields nothing — the caller then falls
// back to a rule-based split so goal nodes always exist.
//
// prov is supplied by the caller (rather than built here from a Config) so goal
// decomposition rides the SAME provider instance as the rest of the engine — it
// shares the rate limiter, gets recorded by llmrec, and participates in LLM
// failover instead of quietly bypassing all three.
//
// desc is the task's free-text description (background: target scope / flag count / engagement notes, etc.).
// It is fed alongside the goal so the decomposer no longer splits blind — the
// prompt still forbids inventing anything the two texts don't state.
//
// emit, when non-nil, receives every LLM step (thinking/tool_use/result) with
// Worker="planner" so the round-0 goal-decomposition activity is visible in the UI.
//
// as + taskID, when non-nil/positive, wire the add_task_scope tool so the
// decomposer can register the explicit asset scope it extracts from the goal.
//
// ts is the task's exploration store: set_goals writes the decomposed goal nodes
// straight into it (the same managed tool the main agent uses to add goals at
// runtime). The returned specs are read back from the store so callers can emit
// per-goal activity and detect the "LLM produced nothing" case for their fallback.
func DecomposeGoals(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	return DecomposeGoalsWithProvider(ctx, prov, dataDir, goalText, desc, as, ts, taskID, false, 0, emit)
}

// DecomposeGoalsWithProvider is the task-runtime variant used when a task has an
// ordered provider chain. It preserves the same tools and write behavior while
// letting the caller own provider selection/failover. maxTokens is the profile's
// per-reply output cap (0 = send none).
func DecomposeGoalsWithProvider(ctx context.Context, prov llm.Provider, dataDir, goalText, desc string, as *db.AssetStore, ts *db.ExplorationStore, taskID int64, nonStreaming bool, maxTokens int, emit func(db.Activity)) []GoalSpec {
	if prov == nil {
		return nil
	}
	// Goal decomposition is a one-shot call without a transcript store, so agentcore
	// does not attach a session id to ctx (it only does so when a writer is present;
	// see agentcore.Prompt). Gateways that use a session-id header for prompt caching /
	// sticky routing (e.g. opencode zen returns 400 MissingSessionID without x-opencode-session)
	// read this value from ctx — without it, chat works while goal decomposition gets a 400.
	// Explicitly attach a stable ID: decomposition requests for the same exploration share
	// it (aiding cache hits), and its naming avoids colliding with planner/worker while
	// allowing llmrec.parseSession to attribute usage accurately.
	if ts != nil {
		ctx = transcript.WithSessionID(ctx, fmt.Sprintf("exp%d-goals", ts.ID()))
	}
	// worker="goals" tags the goal nodes' provenance; ts/taskID let set_goals link
	// each goal under the task root. This is the catalog's real set_goals tool, so a
	// web-edited description/schema on it applies here too.
	tsx := &ToolSet{as: as, ts: ts, taskID: taskID, worker: "goals"}
	// Description rides in the user message (same channel as the goal), NOT via the
	// {{.EngagementDescription}} template var — else a prompt that references the var
	// would inject the description twice. System prompt stays pure static instructions.
	sys := renderSystem("goals", goalsDefaultTmpl, GoalsVars{DataDir: dataDir, Now: nowStr()})
	// set_constraints is always available (does not depend on asset store): the prompt body
	// already includes the "extract operation constraints before splitting goals" step
	// (editable on the agent management page), so we only need to wire the tool here.
	tools := []actool.CoreTool{tsx.setGoals(), tsx.setConstraints()}
	// Wire add_task_scope only when we have a real asset store + task to write to.
	// The scope-extraction tail is appended in lockstep so the prompt never asks for
	// a tool that isn't present.
	if as != nil && taskID > 0 {
		tools = append(tools, tsx.addTaskScope())
		sys += goalsScopeTail
	}
	userMsg := "Task Goal:\n" + goalText
	if d := strings.TrimSpace(desc); d != "" {
		userMsg += "\n\nTask Description (background context, may include target scope / flag count / engagement notes; for reference only, do not fabricate anything not mentioned in it):\n" + d
	}
	// Use captureRun so every LLM step is emitted as an activity record (visible in
	// the plan tab under the round-0 marker). Falls back gracefully when emit is nil.
	captureEmit := func(r db.Activity) {
		if emit != nil {
			r.Worker = "planner"
			emit(r)
		}
	}
	captureRun(ctx, agentcore.Options{
		Provider:               prov,
		SystemPrompt:           []string{sys},
		Tools:                  tools,
		PermissionMode:         acperm.ModeBypass,
		DisableBackgroundTasks: true,
		// 3 steps (extract constraints → register scope → split goals) each require a tool call; give enough turns to avoid missing set_goals before wrap-up.
		MaxTurns:     8,
		NonStreaming: nonStreaming, // uses Provider.Complete when non-streaming is selected for this profile
		MaxTokens:    maxTokens,    // 0 = send no cap, let server default decide
	}, userMsg, captureEmit)
	// set_goals persisted the goals directly; read them back so the caller sees what
	// was written (empty slice ⇒ the LLM produced nothing ⇒ caller falls back).
	if ts == nil {
		return nil
	}
	nodes, _ := ts.ListByKind(db.KindGoal, 10000)
	var out []GoalSpec
	for _, n := range nodes {
		var p struct {
			Text      string `json:"text"`
			VulnClass string `json:"vulnclass"`
		}
		_ = json.Unmarshal(n.Payload, &p)
		if strings.TrimSpace(p.Text) != "" {
			out = append(out, GoalSpec{Text: p.Text, VulnClass: p.VulnClass})
		}
	}
	return out
}
