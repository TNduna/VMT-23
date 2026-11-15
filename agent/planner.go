package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/TNduna/VMT-23/db"
	"github.com/TNduna/VMT-23/intercept"
	"github.com/TNduna/norma/agentcore"
	"github.com/TNduna/norma/llm"
	"github.com/TNduna/norma/permission"
	actool "github.com/TNduna/norma/tool"
	"github.com/TNduna/norma/transcript"
)

// Planner is the event-driven LLM planner (docs §4.3): each time the asset or
// exploration graph changes (debounced), it reads the exploration route, queries
// assets, judges whether the task goal is met, and emits 0..N exploration intents
// into the frontier. It is the sole intent generator.
type Planner struct {
	findingRecorder   FindingRecorder
	prov              llm.Provider
	model             string
	tx                *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window            int                                    // context window in tokens (for compaction)
	windowFn          func() int                             // optional dynamic task-chain minimum
	maxTurns          int                                    // max agent turns per run (0 = unlimited)
	killWork          func(intentID int64) error             // engine callback to terminate a running work (nil = off)
	steerWork         func(intentID int64, msg string) error // engine callback to steer a running work mid-run (nil = off)
	proxyAddr         string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert       string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch         WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir           string                                 // shared work dir (surfaced in prompt as artifact-output target)
	injectConstraints func() bool                            // resolver: inject task operation constraints into system prompt? (nil = yes)
	nonStreamingFn    func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn      func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn       func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
	compactor         *Compactor                             // cold-node compaction (§7); nil = disabled

	// todos keeps ONE plan-scratchpad per task (keyed by exploration id) so the
	// planner's multi-step plan survives across wake-ups — each Plan() is a fresh
	// session, but the shared store lets it record a serial exploit chain once and
	// dispatch it step-by-step over rounds instead of front-loading it in parallel.
	todoMu sync.Mutex
	todos  map[int64]*actool.TodoStore
}

func NewPlanner(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *Planner {
	return &Planner{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns, todos: map[int64]*actool.TodoStore{}}
}

func (p *Planner) SetCompactionWindowResolver(fn func() int) { p.windowFn = fn }

// SetCompactor wires the cold-node compactor (cold-digest §7). Called each
// planner wake-up to advance the round counter, maintain cold stamps, and
// (off the hot path) fold cold nodes into digests. nil = feature disabled.
func (p *Planner) SetCompactor(c *Compactor) { p.compactor = c }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (p *Planner) SetNonStreaming(fn func() bool) { p.nonStreamingFn = fn }

func (p *Planner) nonStreaming() bool { return p.nonStreamingFn != nil && p.nonStreamingFn() }

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (p *Planner) SetNoaEnabled(fn func() bool) { p.noaEnabledFn = fn }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (p *Planner) SetMaxTokens(fn func() int) { p.maxTokensFn = fn }

func (p *Planner) maxTokens() int {
	if p.maxTokensFn == nil {
		return 0
	}
	return p.maxTokensFn()
}

func (p *Planner) compactionWindow() int {
	if p.windowFn != nil {
		return p.windowFn()
	}
	return p.window
}

// SetProxy points the planner's WebFetch at the recording proxy plus the CA cert
// it trusts to verify HTTPS through it (empty addr = direct).
func (p *Planner) SetProxy(addr, caCert string) { p.proxyAddr, p.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the planner (off by default).
func (p *Planner) SetWebSearch(o WebSearchOpts) { p.webSearch = o }

// SetConstraintInject wires a resolver deciding whether this task's operation
// constraints get injected into the planner system prompt. Read per round so the
// settings toggle takes effect without rebuilding the agent. nil = inject (default).
func (p *Planner) SetConstraintInject(fn func() bool) { p.injectConstraints = fn }

// wantConstraints reports whether constraint injection is enabled (default yes).
func (p *Planner) wantConstraints() bool { return p.injectConstraints == nil || p.injectConstraints() }

// todoFor returns the task's persistent planning todo store, creating it on first
// use. Shared across all of this task's planner wake-ups.
func (p *Planner) todoFor(expID int64) *actool.TodoStore {
	p.todoMu.Lock()
	defer p.todoMu.Unlock()
	s := p.todos[expID]
	if s == nil {
		s = actool.NewTodoStore()
		p.todos[expID] = s
	}
	return s
}

// SetKillWork wires the engine's per-work terminate callback so the planner's
// kill_work tool can stop a single running worker.
func (p *Planner) SetKillWork(fn func(intentID int64) error) { p.killWork = fn }

// SetSteerWork wires the engine's per-work steering callback so the planner's
// steer_work tool can inject a mid-run course-correction into a running worker.
func (p *Planner) SetSteerWork(fn func(intentID int64, msg string) error) { p.steerWork = fn }

// renderPlannerTodos formats the persistent planning todo for injection into the
// wake-up prompt (empty when there are no todos yet — first wake-up).
func renderPlannerTodos(items []actool.Todo) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Your Planning Todos (Preserved across awakenings, written by you in prior round)]:\n")
	for _, it := range items {
		mark := map[actool.TodoStatus]string{actool.TodoPending: "☐", actool.TodoInProgress: "▶", actool.TodoCompleted: "✔"}[it.Status]
		if mark == "" {
			mark = "☐"
		}
		b.WriteString(fmt.Sprintf("  %s %s\n", mark, it.Content))
	}
	b.WriteString("Drive forward accordingly: dispatch intents ONLY for the next step whose [prerequisite steps are completed / required facts exist]; update list with TodoWrite (mark steps satisfied by facts as completed). Do not redundantly dispatch pending/in_progress steps.")
	return b.String()
}

// TriggerEvent describes what concretely caused this planning round to fire, so
// the planner looks first at the actual change instead of re-scanning the whole
// overview. Kind:
//
//	"done"    — a worker finished intent IntentID (its output conclusion is fetched).
//	"finding" — a worker reported a finding on intent IntentID (Detail = summary).
//	"goal"    — the human (via main agent set_goals) added one OR MORE goals in a
//	            single call (Goals = newly added goal texts, 1+ items; set_goals supports batch).
//	"goal_deleted" — the human deleted a goal from goal management (Detail = deleted goal text).
//	"goal_edited"  — the human edited a goal from goal management (OldGoal->NewGoal text).
//	"cancelled" — the human deleted intent IntentID (Detail = deletion reason). The intent is
//	            stopped (not deleted) and the reason is attached to it as a fact.
type TriggerEvent struct {
	Kind     string
	IntentID int64
	Detail   string
	Summary  string   // Kind=="cancelled" only: intent summary captured before deletion
	Goals    []string // Kind=="goal" only: new goal text added in this set_goals call
	OldGoal  string   // Kind=="goal_edited" only: goal text before edit
	NewGoal  string   // Kind=="goal_edited" only: goal text after edit
	Hints    []string // Kind=="hint" only: new hint text added in this add_hint call
}

// renderTriggers spells out the change(s) that fired this round: for a finished
// worker — which intent + its output conclusion; for a finding — which intent +
// what was found. Empty for time/heartbeat wakes. Reads the store (best-effort;
// a blank field never blocks the round).
func renderTriggers(ts *db.ExplorationStore, evs []TriggerEvent) string {
	if len(evs) == 0 || ts == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n[Actual changes that triggered this round (inspect here first, then decide whether to add directions)]:")
	for _, ev := range evs {
		switch ev.Kind {
		case "goal":
			if len(ev.Goals) == 1 {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added a goal: %s — New objective, please add exploration directions if uncovered.", ev.Goals[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added %d goals: %s — All new objectives, please add exploration directions for uncovered goals.", len(ev.Goals), strings.Join(ev.Goals, "; ")))
			}
		case "hint":
			if len(ev.Hints) == 1 {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added a strategic hint: %s — Attached to exploration graph, adjust/add exploration directions.", ev.Hints[0]))
			} else {
				b.WriteString(fmt.Sprintf("\n- Human (main agent) added %d strategic hints: %s — Attached to exploration graph, adjust/add exploration directions.", len(ev.Hints), strings.Join(ev.Hints, "; ")))
			}
		case "goal_deleted":
			b.WriteString(fmt.Sprintf("\n- Human deleted goal: %s — Removed, re-evaluate remaining goals/directions (no need to dispatch intents for it).", ev.Detail))
		case "goal_edited":
			b.WriteString(fmt.Sprintf("\n- Human modified goal from %s to %s — Adjust exploration directions accordingly.", ev.OldGoal, ev.NewGoal))
		case "finding":
			b.WriteString(fmt.Sprintf("\n- Worker on intent #%d (%s) reported a finding: %s", ev.IntentID, intentSummary(ts, ev.IntentID), ev.Detail))
		case "cancelled":
			// Intent content prioritizes Summary captured at deletion (after hard deletion, intentSummary returns empty).
			sm := ev.Summary
			if sm == "" {
				sm = intentSummary(ts, ev.IntentID)
			}
			b.WriteString(fmt.Sprintf("\n- Intent #%d deleted by user; intent was: %s, deletion reason: %s. This intent is removed; replan accordingly.", ev.IntentID, sm, ev.Detail))
		default: // "done"
			b.WriteString(fmt.Sprintf("\n- Worker on intent #%d (%s) completed with conclusion: %s", ev.IntentID, intentSummary(ts, ev.IntentID), workerOutput(ts, ev.IntentID)))
			if fids := factIDsYielded(ts, ev.IntentID); fids != "" {
				b.WriteString(fmt.Sprintf("; facts newly produced by this intent: %s ", fids))
			}
		}
	}
	b.WriteString("\n(Full details queryable via node_detail / get_worker_output / list_findings.)")
	return b.String()
}

// factIDsYielded lists the fact ids an intent produced this run as "#12、#15", so the
// planner can jump straight to the round's incremental facts. Empty (best-effort) when
// the intent yielded no facts or the lookup fails.
func factIDsYielded(ts *db.ExplorationStore, id int64) string {
	ids, err := ts.FactsYielded(id)
	if err != nil || len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, fid := range ids {
		parts[i] = fmt.Sprintf("#%d", fid)
	}
	return strings.Join(parts, "、")
}

// intentSummary reads an intent node's one-line summary (best-effort, "?" on miss).
func intentSummary(ts *db.ExplorationStore, id int64) string {
	n, err := ts.GetNode(id)
	if err != nil || n == nil {
		return "?"
	}
	var p map[string]any
	if json.Unmarshal(n.Payload, &p) == nil {
		if s, ok := p["summary"].(string); ok && s != "" {
			return s
		}
	}
	return "?"
}

// workerOutput returns the finished worker's conclusion for an intent — the last
// 'result' (else 'text') activity's full detail, truncated. Same source get_worker_output uses.
func workerOutput(ts *db.ExplorationStore, id int64) string {
	acts, _, err := ts.ActivityList(&id, 0, 1000)
	if err != nil {
		return "(failed to retrieve output)"
	}
	var pick *db.Activity
	for i := range acts {
		if acts[i].Kind == "result" {
			pick = &acts[i]
		} else if acts[i].Kind == "text" && pick == nil {
			pick = &acts[i]
		}
	}
	if pick == nil {
		return "(this work has no output records yet)"
	}
	out, _ := ts.ActivityDetail(pick.ID)
	if out == "" {
		out = pick.Summary
	}
	return truncOutput(out, 800)
}

// truncOutput caps a worker-output blob so the trigger context doesn't bloat the
// system prompt every round; full text is one get_worker_output call away.
func truncOutput(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " ...(truncated, see get_worker_output for full)"
}

// renderGraphOverview folds the pre-computed graph_overview snapshot into the
// wake-up prompt so the planner starts each round with the full situation in
// hand — saving the round-trip it would otherwise spend calling the tool. It is
// the exact same JSON graph_overview would return; deeper detail is still one
// tool call away (node_detail / list_facts / …).
func renderGraphOverview(data map[string]any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return "" // fall back to the model calling graph_overview itself
	}
	return "\n\n[Current Posture (graph_overview prefetch, equivalent to calling that tool; use node_detail/list_facts as needed for details)]:\n" + string(b)
}

// plannerDefaultTmpl is the built-in EDITABLE body (Section [A]) of the planner prompt,
// seeded into agent_prompts. Goal is a {{.Goal}} template var; the Intermediate Artifact Output Specification
// tail is code-owned (artifactSpec) and appended by plannerSystem after rendering.
const plannerDefaultTmpl = `You are the "Planner" of an authorized penetration testing system on a cybersecurity platform, awakened frequently (whenever the graph changes). Duty: Read the Posture -> Assess Goals -> **Supplement exploration intents ONLY when there are genuine uncovered directions**. You are the planner, not an executor: all outputs in this round can ONLY be [generating/clarifying intents] or [evaluating goals]; never execute work directly within the plan.

Task Goal: {{.Goal}}

**Intent Output Cardinality Rules (Think through this first)**:
- **Hard Floor (Highest Priority)**: As long as [Goal is unmet] AND [there are currently no open or running intents] (frontier_open=0 and running_intents is empty), this round **MUST** produce at least one intent driving toward the goal. When no work is running and no directions are queued, producing 0 intents halts the task. Even if known directions are all in recent_done, open a new direction or dispatch a continuation.
- **Beyond the Hard Floor, 0 intents is a valid outcome, but requires valid justification**:
  1. *Already Covered*: All candidate directions are already being handled by intents currently open/running (rephrasing existing intents is a severe error).
  2. *Awaiting Dependency*: The next step strictly depends on output from currently executing work that has not yet completed (dispatching prematurely causes idle spins).
- **Conversely**: When genuine uncovered directions exist that do not depend on running work, dispatch them — never treat 0 intents as a lazy default.

**Per-Awakening Decision Pipeline**:

1. **Complete Posture is appended below** (the exact return from graph_overview, no need to call it again): task metadata, asset counts, goals & statuses, open/running/recent_done intents, sites without endpoints, facts, and recent facts ({id, summary, confidence?}).
   - Scope: Exploration nodes belong only to this task; asset graph is shared globally (asset count is global across scope, not unique to this task) — ignore assets unrelated to this task.
   - Lineage: Each intent carries parents (upstream facts/intents) and yields (downstream facts/findings); recent_facts carries from_intent. Use this to understand which facts originate from which directions and whether they synthesize into new attack paths.
   - Negative/Doubtful Observations: A worker noting 'port closed' or 'not injectable' is an observation, not a conclusion. Before accepting, inspect node_detail(id) for evidence: only when evidence is solid, confidence=observed, and methods are exhausted should that path be treated as temporarily sealed. If evidence is lacking or confidence=inferred, treat as unverified and dispatch a re-check intent to confirm or refute (at most one re-check for the same negative path; if still negative after re-check, respect the conclusion and cease dispatching).
   - Detailed queries as needed: list_facts (paginated, latest first, default 20), list_findings (all vulnerabilities), node_detail(id) (full evidence/detail), list_assets (pull: search q, filter type/company_id/task_id), asset_neighbors.

2. **Goal Evaluation (Core Responsibility)**: Inspect goals. When an unmet goal is proven by a fact/finding, call prove_goal(goal_id, evidence_id, reason) to mark it met. **When you mark the last remaining goal, the system automatically completes the entire task** — completion is driven solely by prove_goal.
   - Strict Acceptance Verification: When a goal includes quantitative conditions (e.g. coverage reaches X%, capture N flags, obtain specific privilege), you MUST verify exact measurements from graph_overview above (coverage.pct, findings_total, etc.) before calling prove_goal. If criteria are unmet, calling prove_goal is forbidden; dispatch intents to close the gap.

3. **(Kickoff Only, Lightweight) Probing**: Only when the graph has virtually no facts (recent_facts essentially empty at task kickoff) and initial intent cannot be clarified from posture alone, run <= 3 lightweight read-only probes (e.g. 1-2 curls to inspect homepage/fingerprints). **The only legitimate product is a more precise intent description** — never vulnerability discovery, exploitation, or endpoint enumeration (those are worker tasks). Three hard rules:
   - If worker-produced facts exist (facts > 0 / recent_facts non-empty) -> Probing yourself is strictly forbidden. Base all decisions on existing facts; outputs must only be new intents or completion.
   - Even at kickoff, probe at most <= 3 times, solely to clarify initial intent. If you find yourself doing deep testing, stop immediately and write it as an intent.
   - If determinable from existing posture, do not probe at all.

4. **Selecting New Directions**: Moderation means NOT repeating existing intents, not minimizing dispatch. When goal is unmet, ask "what deeper, more aggressive, uncovered attack paths remain to advance the goal?" Intents are open exploration directions; compare against open + running + recent_done:
   - Covered by open/running -> Do not generate (in progress).
   - Appeared in recent_done -> Check the intent state:
     - done: Covered -> Do not dispatch identical intent. Only reopen if materially new mechanisms emerge (new facts/assets/parameters/distinct techniques) and explain the difference in summary.
     - exhausted (budget ran out mid-way) / blocked (model or network failure) -> Inspect get_worker_trace / get_worker_output to see where it stalled, then resume progress, retry, or change angle based on real progress.
   - Completely uncovered new direction -> Generate.
   - Depth over breadth: Prioritize driving deep exploitation on high-value entry points (potential RCE, privilege escalation, data exfiltration) rather than superficial broad scanning.
   - Route diversity: Maintain 2-3 distinct exploit mechanisms in parallel (e.g. file upload chain vs. auth bypass) until one yields goal progress. Diversity strictly obeys Operational Constraints.
   - Serial exploitation chains: Dispatch step-by-step; do not fire dependent steps in parallel. Use TodoWrite to record the chain (one item per step) and dispatch only the step whose prerequisites are currently satisfied.

5. **Submission**: Batch-submit filtered directions via ONE add_intent call (intents array, up to 4 high-value directions):
   - summary: One-sentence natural language description (target address + what to do + why).
   - asset_ids: Target asset IDs (from list_assets).
   - parent_ids: Upstream nodes synthesized to produce this intent.

Do not duplicate; do not pad. When goals are unmet and deeper uncovered paths exist, dispatch decisively. Concise, focused, efficient.`

func plannerSystem(goal, dataDir, workDir string) string {
	body := renderSystem("planner", plannerDefaultTmpl, PlannerVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Plan runs one planning round. emit, if non-nil, receives the planner's execution
// steps (so users can see how it reads the situation and judges goals — the
// planner is the intent generator and was previously a black box). Returns whether
// the planner judged the goal met.
// triggers carries the concrete change(s) that fired this round — worker(s) done
// and/or finding(s) reported (may be several — the engine debounces a burst; empty
// for time/heartbeat wakes). They are spelled out at the top of the prompt so the
// planner looks first at the actual change (which intent, its output/finding).
func (p *Planner) Plan(ctx context.Context, taskID int64, as *db.AssetStore, ts *db.ExplorationStore, goal string, triggers []TriggerEvent, emit func(db.Activity)) (met bool, reason string, err error) {
	// cold-digest §2.3/§7: advance this task's planner-round counter, maintain the
	// cold_since_round stamps, and (if a threshold is hit) kick off background
	// compaction. Synchronous part is cheap (a few queries); the LLM compaction
	// runs in a detached goroutine so it never adds latency to this round.
	p.compactor.OnPlannerRound(ctx, ts)
	tsx := NewToolSet(ts, "planner")
	tsx.SetFindingRecorder(p.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.killWork = p.killWork   // enable kill_work tool (nil = unavailable)
	tsx.steerWork = p.steerWork // enable steer_work tool (nil = unavailable)
	if origin, _ := ts.OriginFactID(); origin > 0 {
		tsx.SetOwnerNode(origin) // planner-side anchors default to the task root (origin fact)
	}
	// Domain tools + base default toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash)
	// When asset coverage is disabled, omit add_task_scope/list_untested_assets from prompt.
	base := append(tsx.DropCoverageTools(tsx.PlannerTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "planner", base)
	defer cleanup()
	// Critical posture (just completed intent + prefetched complete graph) placed in current round user input (see input below).
	// System prompt retains static planning text, which stabilizes system prompt across rounds for caching.
	situational := renderTriggers(ts, triggers) + renderGraphOverview(tsx.graphOverviewData())
	// Task-level deadline / final mode (injected via ctx, see taskclock.go). In the final round, task timeout
	// planner wrap-up instructions are appended to user input, directing it to perform only goal evaluations.
	tc := taskClockFrom(ctx)
	if tc.Final {
		situational += "\n\n[Task Final Wrap-up (Special directive for this round, overrides standard planning)]:" + resolveTaskTimeoutWrapup("planner")
	}
	// Pre-create task work directory <workDir>/tasks/<taskID>.
	taskDir := ensureRunDir(p.workDir, taskID, 0)
	ctx = intercept.WithReviewContext(ctx, taskDir, intercept.ReviewBackground{})
	sysBody := plannerSystem(goal, p.workDir, taskDir)
	if p.wantConstraints() {
		sysBody += constraintBlock(ts) // Operational constraints (if any) injected to frame exploration boundary
	}
	system, boundary := deferredSystem(sysBody, def)
	// planner has no wall-clock budget itself; with deadline MaxDuration is clamped to remaining time.
	maxDur, clamped := clampMaxDuration(tc.DeadlineUnix, 0)
	settle := wrapupSettlement("planner", nil)
	if tc.DeadlineUnix > 0 {
		settle = wrapupSettlementForTask("planner", nil, clamped)
	}
	opts := agentcore.Options{
		Provider:        p.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // Route through recording proxy; load CA to verify MITM HTTPS certs
		WebFetchProxy:   p.proxyAddr,
		WebFetchCACert:  p.proxyCACert,
		// Web search (optional). ddgs requires no key; brave-free requires BraveKey; tavily requires TavilyKey.
		// WebSearchProxy is an independent egress proxy (http/https/socks5), empty = direct.
		EnableWebSearch:       p.webSearch.Enabled,
		WebSearchBackend:      p.webSearch.Backend,
		BraveSearchAPIKey:     p.webSearch.BraveKey,
		TavilySearchAPIKey:    p.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: p.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  p.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   p.webSearch.DeepSeekModel,
		WebSearchProxy:        p.webSearch.Proxy,
		BashEnv:               proxyEnv(p.proxyAddr, p.proxyCACert), // Bash subcommands route through proxy + trust CA
		WorkingDir:            taskDir,                              // Task work directory <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(taskDir),
		MaxTurns:              p.maxTurns, // 0 = unlimited (configurable in agent management)
		MaxDuration:           maxDur,     // 0=unlimited; with deadline = time remaining until deadline
		Compaction:            compactionConfig(p.compactionWindow()),
		// Shared planning todos across awakenings: preserves serial chains across rounds.
		Todos: p.todoFor(ts.ID()),
		// On step budget hit, SDK runs wrap-up to persist decided conclusions (add_intent, prove_goal, TodoWrite),
		// rather than halting planning — planner will continue to be awakened. Clamped uses PromptByReason.
		Settlement:   settle,
		NonStreaming: p.nonStreaming(), // Uses Provider.Complete when profile selects non-streaming
		MaxTokens:    p.maxTokens(),    // 0 = no limit sent, determined by provider default
	}
	if p.tx != nil { // persist raw LLM conversation; one accumulating file per task's planner
		opts.Transcript = p.tx
		opts.SessionID = fmt.Sprintf("exp%d-planner", ts.ID())
	}
	// Experimental feature: noa takes over context compaction when enabled.
	noaSession := fmt.Sprintf("exp%d-planner", ts.ID())
	enableNoa(&opts, p.noaEnabledFn, p.workDir, noaSession, noaWarn(noaSession))
	// Posture (recent finished intent + full graph overview) appended into current round user input.
	// User input also carries instructions + cross-awakening planning todos.
	// Opening text differs based on whether concrete triggers occurred this round:
	lead := "Specific changes just occurred (see [Actual changes that triggered this round] below); plan next steps accordingly:"
	if len(triggers) == 0 {
		lead = "This round is a **periodic check (heartbeat) / signal with no specific changes** — graph may not have new changes. Review running intents: use steer_work for stalled/divergent runs, kill_work if wrong direction; re-evaluate goals and determine if new directions are needed:"
		// If there are no open or running intents across the graph -> exploration is stalled.
		// Instruct planner to produce new directions rather than idling.
		if active, err := ts.HasActiveIntent(); err == nil && !active {
			lead = "This round is a **periodic check (heartbeat)**, and currently **there are no open or running intents** — exploration is stalled! You **MUST** produce in this round one or more non-duplicate exploration directions advancing toward the goal (producing 0 intents is prohibited); first assess if goals are met, and if unmet formulate new directions immediately:"
		}
	}
	input := lead + situational + "\n\nBased on the posture above, evaluate goals. When a goal is [genuinely met] (objective achieved/vulnerability confirmed), call prove_goal individually to mark met. **Hard floor: as long as goal is unmet and there are no open or running intents (frontier_open=0 and running_intents is empty), this round MUST produce at least one intent advancing toward the goal. Producing 0 intents halts the task. Only when existing open/running intents are actively advancing or goals are met may this round conclude with 0 intents.**" +
		renderPlannerTodos(opts.Todos.List())
	// MaxDuration now interrupts in-flight tools at wall-clock deadline and enters wrap-up phase directly on live ctx.
	_, _, err = captureRun(ctx, opts, input,
		func(r db.Activity) {
			if emit != nil {
				r.Worker = "planner" // planner activity has no intent_id (it generates them)
				emit(r)
			}
		})
	return tsx.GoalMet, tsx.Reason, err
}
