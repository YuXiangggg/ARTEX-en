package agent

import (
	"context"

	"github.com/Autumn-27/artex/db"
)

// FindingRecorder is injected by the host; agents never synthesize or copy
// evidence bodies themselves. Its implementation owns the atomic write.
type FindingRecorder interface {
	Record(context.Context, db.RecordFindingInput, []db.TrafficRef) (*db.RecordedFinding, error)
}

// Tool-use guidance is appended without replacing the user's editable prompt.
// It does not require capture or claim that unavailable traffic tools exist.
const findingTrafficGuidance = "\n\n**Finding traffic evidence (optional)**: When calling report_finding to report a finding, if you have HTTP requests/responses that you have reviewed and confirmed support the finding's conclusion, use traffic_refs to bind the real IDs in reproduction order; domain and time are only for candidate filtering and do not presume association. For non-HTTP findings such as TCP, when traffic was not captured, or when there is no exact match, omit it or pass []; keep other verifiable evidence such as command output and logs in evidence, and it is recommended to explain why nothing was bound. Do not guess IDs, and do not re-probe just to collect packets."

func (t *ToolSet) SetFindingRecorder(r FindingRecorder)   { t.findingRecorder = r }
func (w *Worker) SetFindingRecorder(r FindingRecorder)    { w.findingRecorder = r }
func (p *Planner) SetFindingRecorder(r FindingRecorder)   { p.findingRecorder = r }
func (m *MainAgent) SetFindingRecorder(r FindingRecorder) { m.findingRecorder = r }
