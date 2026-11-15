package agent

import (
	"context"
	"fmt"

	"github.com/TNduna/VMT-23/db"
	"github.com/TNduna/VMT-23/intercept"
	"github.com/TNduna/norma/agentcore"
	"github.com/TNduna/norma/llm"
	"github.com/TNduna/norma/permission"
	actool "github.com/TNduna/norma/tool"
	"github.com/TNduna/norma/transcript"
)

// MainAgent is the thin human-interface orchestrator (docs §4.2 / §7). The human
// chats with it; it observes (read tools), and steers by injecting hints
// (→planner) or direct high-priority intents (→frontier). It does NOT run the
// autonomous intent-generation loop (that is the planner's job).
type MainAgent struct {
	findingRecorder FindingRecorder
	prov            llm.Provider
	model           string
	tx              *transcript.Store                      // raw LLM conversation persistence (nil = off)
	window          int                                    // context window in tokens (for compaction)
	windowFn        func() int                             // optional dynamic task-chain minimum
	maxTurns        int                                    // max agent turns per run (0 = unlimited)
	proxyAddr       string                                 // recording proxy for WebFetch (empty = direct)
	proxyCACert     string                                 // recording proxy's CA cert path (HTTPS verify)
	webSearch       WebSearchOpts                          // web_search tool backend selection (off by default)
	workDir         string                                 // shared work dir (surfaced in prompt as artifact-output target)
	steerWork       func(intentID int64, msg string) error // engine callback: steer a running work (nil = off)
	nonStreamingFn  func() bool                            // resolver: use non-streaming (Complete) path? (nil = streaming)
	noaEnabledFn    func() bool                            // resolver: use experimental noa compaction? (nil = off)
	maxTokensFn     func() int                             // resolver: per-reply output cap (nil/0 = send no cap)
}

// SetNoaEnabled wires a resolver deciding whether runs use the experimental noa
// context-compression mechanism. nil/unset = off (built-in compaction). Read per
// run so the settings toggle takes effect without rebuilding the agent.
func (m *MainAgent) SetNoaEnabled(fn func() bool) { m.noaEnabledFn = fn }

// SetNonStreaming wires a resolver deciding whether runs use the non-streaming
// model path (true = non-streaming). nil/unset = streaming (default).
func (m *MainAgent) SetNonStreaming(fn func() bool) { m.nonStreamingFn = fn }

func (m *MainAgent) nonStreaming() bool { return m.nonStreamingFn != nil && m.nonStreamingFn() }

// SetMaxTokens wires a resolver for the per-reply output cap. nil/unset or 0 =
// send no cap and let the endpoint decide. Read per run, like nonStreaming.
func (m *MainAgent) SetMaxTokens(fn func() int) { m.maxTokensFn = fn }

func (m *MainAgent) maxTokens() int {
	if m.maxTokensFn == nil {
		return 0
	}
	return m.maxTokensFn()
}

func NewMainAgent(prov llm.Provider, model, workDir string, tx *transcript.Store, window, maxTurns int) *MainAgent {
	return &MainAgent{prov: prov, model: model, workDir: workDir, tx: tx, window: window, maxTurns: maxTurns}
}

func (m *MainAgent) SetCompactionWindowResolver(fn func() int) { m.windowFn = fn }

func (m *MainAgent) compactionWindow() int {
	if m.windowFn != nil {
		return m.windowFn()
	}
	return m.window
}

// SetProxy points the main agent's WebFetch at the recording proxy plus the CA
// cert it trusts to verify HTTPS through it (empty addr = direct).
func (m *MainAgent) SetProxy(addr, caCert string) { m.proxyAddr, m.proxyCACert = addr, caCert }

// SetWebSearch selects the web_search backend for the main agent (off by default).
func (m *MainAgent) SetWebSearch(o WebSearchOpts) { m.webSearch = o }

// SetSteerWork wires the engine callback that lets the main agent's steer_work
// tool inject a mid-run course-correction into a running work (nil = tool off).
func (m *MainAgent) SetSteerWork(fn func(intentID int64, msg string) error) { m.steerWork = fn }

// mainAgentDefaultTmpl is the built-in EDITABLE body (section [A]) of the main agent
// prompt, seeded into agent_prompts. Goal is a {{.Goal}} template var; the intermediate
// artifact output specification tail is code-owned (artifactSpec), appended after rendering.
const mainAgentDefaultTmpl = `You are the "Main Agent" of an authorized penetration testing system and serve as the interface for the human operator. You do not perform exploration yourself, nor do you autonomously generate intents in a loop (that is the planner's job). Your responsibilities:

1. Observe: Use graph_overview / list_findings / list_facts / list_assets / get_worker_output to answer the operator's questions about current progress.
2. Steer (translate the operator's intent into the system):
   - Operator wants to "change direction / emphasize a vulnerability class / focus on an area" → use add_hint to record a hint (the planner will read it on its next round).
   - Operator wants to "immediately test a specific target" → use add_intent to directly inject a high-priority intent (priority 8-10). The system will automatically pull a completed task back to running, let a worker claim and execute this intent, and return to completed status once finished.
     **When all task goals are already met** (all goals in graph_overview are met): before dispatching, first judge whether this intent implies a "new final outcome to be achieved". If so, restate the inferred goal to the operator in one sentence and **ask whether to register it as a formal goal** — if yes → use set_goals to register it (the task then enters normal planning and the planner will autonomously drive it forward); if no / just a one-off check → only call add_intent to dispatch this single intent, and once the worker finishes, the task returns to completed status (without continuing autonomously). If the intent is clearly just a one-time check that does not imply a new goal, call add_intent directly without asking every time.
   - Operator wants to "steer a currently running intent (work) in real time (stop doing X, focus on Y)" → use steer_work (takes effect before the worker's next action without interrupting or losing progress); call get_worker_output first to see what it is doing. If the entire direction is wrong, use add_intent to dispatch a new intent instead.
   - Operator wants to "add a new final goal to achieve" → use set_goals to add the goal. The system writes the goal into the task graph and **automatically pulls a completed/paused task back to running** (the planner will then re-evaluate whether goals are met), without requiring manual resume.
   - Operator wants to "add/modify testing constraints (allow/prohibit certain operations, such as 'only test the current port', 'no brute-forcing', 'passive recon only')" → use set_constraints to register them (type=allow / type=deny). Constraints are injected into the planner/worker prompts on the next planning round to bound exploration; they can also be added, edited, or deleted under "Constraint Management" in the overview.
3. Reply concisely in plain language explaining what you did.

Current Task Goal: {{.Goal}}

Do not fabricate findings; answer strictly based on real data returned by tools.`

func mainAgentSystem(goal, dataDir, workDir string) string {
	body := renderSystem("mainagent", mainAgentDefaultTmpl, MainVars{Goal: goal, DataDir: dataDir, Now: nowStr()})
	return body + artifactSpec(workDir)
}

// Chat handles one human message and returns the assistant reply. emit, if
// non-nil, receives each execution step (thinking / tool_use / tool_result /
// text / result) so the main-agent session shows its work — exactly like the
// worker/planner sessions — not just the final answer.
func (m *MainAgent) Chat(ctx context.Context, taskID int64, mainSeg int, as *db.AssetStore, ts *db.ExplorationStore, goal, message string, emit func(db.Activity), notify, resume func(), notifyGoal, notifyHint func([]string)) (string, error) {
	tsx := NewToolSet(ts, "human")
	tsx.SetFindingRecorder(m.findingRecorder)
	if as != nil {
		tsx.SetAssetStore(as, as.Companies())
	}
	tsx.SetTaskID(taskID)
	tsx.SetCoverageEnabled(as == nil || as.CoverageEnabled(taskID))
	tsx.SetNotify(notify)         // general wake-up (used by write operations without a dedicated callback, debounced)
	tsx.SetResumeTask(resume)     // set_goals adds a goal → pull completed/paused task back to running
	tsx.SetNotifyGoal(notifyGoal) // set_goals adds a goal → record an "operator added goal: ..." trigger for planner
	tsx.SetNotifyHint(notifyHint) // add_hint adds a hint → record an "operator added N strategic hints: ..." trigger for planner
	tsx.steerWork = m.steerWork   // enable steer_work tool (nil = unavailable)
	// Domain tools + default SDK toolset (Read/Write/Edit/MultiEdit/LS/Glob/Grep/Bash).
	// When asset coverage is disabled, filter out add_task_scope/list_untested_assets (excluded from prompt).
	base := append(tsx.DropCoverageTools(tsx.MainAgentTools()), actool.DefaultTools()...)
	ctx = WithRunInfo(ctx, RunInfo{TaskID: taskID, ExplorationID: explorationID(ts)})
	tools, def, cleanup := AugmentTools(ctx, "mainagent", base)
	defer cleanup()
	// Ensure this task's working directory <workDir>/tasks/<taskID> exists.
	mainDir := ensureRunDir(m.workDir, taskID, 0)
	ctx = intercept.WithReviewWorkingDirectory(ctx, mainDir)
	system, boundary := deferredSystem(mainAgentSystem(goal, m.workDir, mainDir), def)
	opts := agentcore.Options{
		Provider:        m.prov,
		SystemPrompt:    system,
		DynamicBoundary: boundary,
		Tools:           tools,
		DeferredTools:   def.Deferred,
		UnlockSet:       def.Unlock,
		PermissionMode:  permission.ModeBypass,
		EnableWebFetch:  true, // route through recording proxy for audit trail; load proxy CA to verify MITM-resigned HTTPS certs
		WebFetchProxy:   m.proxyAddr,
		WebFetchCACert:  m.proxyCACert,
		// Web search (optional). ddgs requires no key; brave-free requires BraveKey; tavily requires TavilyKey.
		// WebSearchProxy is an independent egress proxy (http/https/socks5), unrelated to the traffic-recording MITM proxy; empty means direct.
		EnableWebSearch:       m.webSearch.Enabled,
		WebSearchBackend:      m.webSearch.Backend,
		BraveSearchAPIKey:     m.webSearch.BraveKey,
		TavilySearchAPIKey:    m.webSearch.TavilyKey,
		DeepSeekSearchBaseURL: m.webSearch.DeepSeekBaseURL,
		DeepSeekSearchAPIKey:  m.webSearch.DeepSeekAPIKey,
		DeepSeekSearchModel:   m.webSearch.DeepSeekModel,
		WebSearchProxy:        m.webSearch.Proxy,
		BashEnv:               proxyEnv(m.proxyAddr, m.proxyCACert), // Bash subprocesses route through proxy and trust CA by default
		WorkingDir:            mainDir,                              // task working directory <workDir>/tasks/<taskID>
		ToolOutputDir:         cmdOutDir(mainDir),
		MaxTurns:              m.maxTurns,                             // 0 = unlimited (configurable in agent management)
		Compaction:            compactionConfig(m.compactionWindow()), // long chats stay within the window
		Todos:                 actool.NewTodoStore(),                  // session-scoped scratchpad todos (TodoWrite), for planning only, discarded on exit
		// On hitting the step budget → SDK runs settlement: output a one-sentence progress summary to the user. Prompt and turn budget are editable in admin UI (default 10 turns).
		Settlement:   wrapupSettlement("mainagent", nil),
		NonStreaming: m.nonStreaming(), // uses Provider.Complete when non-streaming is selected for this profile
		MaxTokens:    m.maxTokens(),    // 0 = send no cap, let server default decide
	}
	if m.tx != nil { // persist raw human↔AI conversation; one accumulating file per segment
		opts.Transcript = m.tx
		// Segment 0 keeps the legacy "exp%d-main" name so existing transcripts still
		// load; each new session (seg>=1) gets its own file for a clean context.
		opts.SessionID = fmt.Sprintf("exp%d-main", ts.ID())
		if mainSeg > 0 {
			opts.SessionID = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
		}
	}
	// Experimental feature: when enabled, noa takes over context compaction (archives are persisted centrally under <workDir>/noa/<SessionID>).
	// Session ID follows the same segment-aware rule as transcript so archives align with resume.
	noaSession := fmt.Sprintf("exp%d-main", ts.ID())
	if mainSeg > 0 {
		noaSession = fmt.Sprintf("exp%d-main-s%d", ts.ID(), mainSeg)
	}
	enableNoa(&opts, m.noaEnabledFn, m.workDir, noaSession, noaWarn(noaSession))
	ctx = attachSideCapture(ctx, &opts)
	s := agentcore.NewSession(opts)
	defer s.Close()
	// reload the prior conversation from the transcript so the agent has context
	// across turns (each Chat is a fresh session; without this it can't see earlier
	// messages). First turn: no file yet → Resume loads nothing and proceeds.
	if m.tx != nil {
		_ = s.Resume(opts.SessionID)
	}
	// C2: this session is fresh each turn; re-unlock skill-gated MCPs from prior
	// Skill() calls in the reloaded history so revealed tools stay callable.
	seedUnlockFromHistory(s.Messages(), def.UnlockSkill)
	text, _, err := captureRunSession(ctx, s, message, func(r db.Activity) {
		if emit != nil {
			r.Worker = "mainagent"
			emit(r)
		}
	})
	return text, err
}
