package agent

import (
	"context"

	"github.com/mokerenki/cautious-memory/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Vulnerability Traffic Evidence (Optional)**: When calling report_finding to report a vulnerability, if you have inspected and confirmed HTTP requests/responses that support the vulnerability conclusion, you may bind their real IDs in reproduction order via traffic_refs; domain and timestamp are only for candidate filtering and do not imply association. For non-HTTP vulnerabilities (such as TCP), when traffic capture is off, or when there is no exact match, omit traffic_refs or pass [], and retain command outputs, logs, or other verifiable evidence in evidence (explaining why traffic was not bound is recommended). Do not guess IDs, and do not re-probe solely to generate traffic captures."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
