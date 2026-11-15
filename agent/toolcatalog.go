package agent

import (
	"context"
	"encoding/json"

	actool "github.com/TNduna/norma/tool"
)

// This file turns "built-in tools" from pure code into an enumerable, DB-overridable catalog:
//   - BuiltinToolSeeds(): expands the built-in tool sets of the execution agents into seed
//     records (key + description + parameter schema + default bound agents) so the server can
//     idempotently seed the tools table on startup.
//   - ToolResolve hook: at runtime, filters assembled tools per agent, overrides descriptions/
//     schemas, and injects parameter defaults according to the DB tools rows. Keys and handlers
//     stay in code; the DB only modifies prose and default values.
// The handler (Call behavior) always comes from code — the DB cannot alter it, only the
// model-facing documentation and default input arguments.

// ToolSeed is a seedable snapshot of a built-in tool: Key is CoreTool.Name() (hard-bound to
// the handler and read-only in the UI), Desc/Schema come from the in-code tool definition,
// and Agents lists which agents receive it by default.
type ToolSeed struct {
	Key    string         // = CoreTool.Name(), primary key, immutable
	Desc   string         // top-level description (overridable in UI)
	Schema map[string]any // parameter JSON-Schema (structure is read-only; description/default are editable in UI)
	Agents []string       // default bound agent keys (worker/planner/mainagent)
}

// builtinToolsByAgent constructs each execution agent's domain tool set using a "read-only
// empty shell" ToolSet (nil stores). Tool constructors only wrap closures into Spec without
// dereferencing stores at construction time, so nil is safe — these tools are only used here
// to read Name()/Description()/InputSchema(), never Call.
//
// Deliberately EXCLUDES SDK generic tools actool.DefaultTools() (Read/Write/Edit/MultiEdit/LS/
// Glob/Grep/Bash): every agent owns them unconditionally without per-agent binding trade-offs,
// and most of their instructions live in Prompt() (this table only overrides Description(),
// which would create misleading partial overrides). Not seeded → no DB row → ToolResolve passes
// them through untouched, preserving legacy behavior. Only VMT-23's own domain tools are managed here.
func builtinToolsByAgent() map[string][]actool.CoreTool {
	ts := NewToolSet(nil, "")
	return map[string][]actool.CoreTool{
		"mainagent": ts.MainAgentTools(),
		"planner":   ts.PlannerTools(),
		"worker":    ts.WorkerTools(),
		// goals (goal decomposer) binds set_goals + set_constraints by default to persist decomposed
		// goals and extracted operation constraints. Shares the same managed tools with mainagent so
		// descriptions/schemas and per-agent bindings can be edited in the web UI.
		"goals": {ts.setGoals(), ts.setConstraints()},
		// auto binds vulnerability reporting + asset management tools by default; other domain tools
		// can be checked on demand in the UI.
		// New databases are populated by this seed; existing databases migrate via seedAutoDefaultBindings.
		"auto": {ts.addFinding(), ts.insertAssets(), ts.addCompanyScope(), ts.listAssets(), ts.listCompanies()},
		// pentest (solo pentest agent) binds by default: query assets / insert assets / report findings / list findings / list companies.
		// New databases are populated by this seed; existing databases migrate via seedPentestDefaultBindings.
		"pentest": {ts.listAssets(), ts.insertAssets(), ts.addFinding(), ts.listFindings(), ts.listCompanies()},
	}
}

// defaultUnbound: these system tools are still cataloged (visible in the web UI and manually
// bindable per agent), but are bound to NO agents by default — ToolResolve drops tools with
// empty bindings for all agents unless explicitly opted in.
// They remain in an agent's base tool set (e.g. goal_met in PlannerTools) so that: (1) seeding
// can construct them to extract desc/schema, and (2) if a user manually binds them back, the tool
// is present in base at runtime for ToolResolve to retain.
//
// goal_met: bypasses per-goal prove_goal to declare the [entire task complete] globally; it carries
// high impact and risk of premature completion, and duplicates "marking the last goal via prove_goal
// → automatic task completion", so it is unbound by default and can be manually bound when needed.
var defaultUnbound = map[string]bool{"goal_met": true}

// BuiltinToolSeeds deduplicates and merges each agent's built-in tool set into a seed list:
// tools with the same name (e.g. list_assets shared across agents) merge into one entry with
// the union of Agents; tools in defaultUnbound have their agent bindings forced to empty.
func BuiltinToolSeeds() []ToolSeed {
	byAgent := builtinToolsByAgent()
	order := []string{"mainagent", "goals", "planner", "worker", "auto", "pentest"}

	type acc struct {
		tool   actool.CoreTool
		agents []string
	}
	m := map[string]*acc{}
	var keys []string
	for _, ak := range order {
		for _, t := range byAgent[ak] {
			a, ok := m[t.Name()]
			if !ok {
				a = &acc{tool: t}
				m[t.Name()] = a
				keys = append(keys, t.Name())
			}
			a.agents = append(a.agents, ak)
		}
	}

	out := make([]ToolSeed, 0, len(keys))
	for _, k := range keys {
		a := m[k]
		agents := a.agents
		if defaultUnbound[k] {
			agents = []string{} // cataloged and manually bindable, but unbound by default (stored as [] rather than null for consistency)
		}
		out = append(out, ToolSeed{
			Key:    k,
			Desc:   a.tool.Description(),
			Schema: a.tool.InputSchema(),
			Agents: agents,
		})
	}
	return out
}

// ToolResolve, if set, post-processes an agent's fully-assembled tool list against
// the DB tools table: it drops tools not bound to this agent (or globally disabled)
// and wraps the rest so the model sees the DB-overridden description/schema and
// default input parameters get injected. Tools with no matching DB row (MCP/skill/host tools like
// traffic) pass through untouched. nil = tools unchanged. Wired in server/assembly.go.
var ToolResolve func(ctx context.Context, agentKey string, tools []actool.CoreTool) []actool.CoreTool

// DecorateTool wraps t so Description()/InputSchema() report the DB overrides and
// Call() injects scalar parameter defaults (from schema's "default" props) whenever
// the model omitted them. Name/Prompt/permission/scheduler flags delegate to t, so
// the tool's identity and handler are unchanged. Empty desc/schema fall back to t's.
func DecorateTool(t actool.CoreTool, desc string, schema map[string]any) actool.CoreTool {
	if desc == "" {
		desc = t.Description()
	}
	if len(schema) == 0 {
		schema = t.InputSchema()
	}
	return &overriddenTool{CoreTool: t, desc: desc, schema: schema}
}

// overriddenTool is a CoreTool decorator: it embeds the original (so all behavioral
// methods — Prompt/IsReadOnly/IsConcurrencySafe/CheckPermissions/Name — delegate)
// and overrides only the model-facing description/schema plus default injection.
type overriddenTool struct {
	actool.CoreTool
	desc   string
	schema map[string]any
}

func (o *overriddenTool) Description() string         { return o.desc }
func (o *overriddenTool) InputSchema() map[string]any { return o.schema }

func (o *overriddenTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	return o.CoreTool.Call(ctx, injectDefaults(in, o.schema), tc)
}

// injectDefaults fills scalar parameter defaults declared in the (possibly edited)
// schema into the input JSON whenever the model omitted the field or left it empty/
// null. Structure (names/types/required) is untouched — only default values are merged in.
func injectDefaults(in json.RawMessage, schema map[string]any) json.RawMessage {
	defs := scalarDefaults(schema)
	if len(defs) == 0 {
		return in
	}
	m := map[string]json.RawMessage{}
	if len(in) > 0 {
		if err := json.Unmarshal(in, &m); err != nil {
			return in // non-object input: don't touch it
		}
	}
	changed := false
	for k, dv := range defs {
		if cur, ok := m[k]; !ok || isEmptyJSON(cur) {
			m[k] = dv
			changed = true
		}
	}
	if !changed {
		return in
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in
	}
	return b
}

// scalarDefaults extracts properties[k]["default"] for scalar params (string/
// integer/number/boolean). Array/object defaults are skipped: merging them is
// ambiguous and not worth the surprise.
func scalarDefaults(schema map[string]any) map[string]json.RawMessage {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return nil
	}
	out := map[string]json.RawMessage{}
	for name, raw := range props {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dv, ok := p["default"]
		if !ok || dv == nil {
			continue
		}
		switch p["type"] {
		case "string", "integer", "number", "boolean":
			if b, err := json.Marshal(dv); err == nil {
				out[name] = b
			}
		}
	}
	return out
}

func isEmptyJSON(raw json.RawMessage) bool {
	s := string(raw)
	return s == "null" || s == `""`
}
