package agent

import (
	"encoding/json"
	"fmt"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

const findingIDGuidance = "\n\n**Finding ID conventions**: finding_id is the standalone finding record ID; finding_node_id is the exploration node ID. The id returned by list_findings / list_task_findings / node_detail / get_task_node_detail stays the exploration node ID; read the standalone number from the finding_id in the same response. get_finding_traffic / bind_finding_traffic use the standalone finding_id. The legacy update_finding_report finding_id parameter still takes finding_node_id. Do not use the number on the first line of report_finding for the evidence tools, and do not guess other numbers after hitting an ID error."

// The server supplies the persisted setting. A missing setting/host is off.
// Consulted at assembly and again on writes so an already-running session
// cannot keep binding after the user switches the feature off.
var FindingTrafficBindingEnabled func() bool

func findingTrafficBindingEnabled() bool {
	return FindingTrafficBindingEnabled != nil && FindingTrafficBindingEnabled()
}

// Applied after ToolResolve: user descriptions and prompts remain intact, while
// all actual reporters (including Planner and custom chat agents) see the same
// API contract. Disabled/unbound tools are never reintroduced here.
func findingWorkflowTools(agentKey string, tools []actool.CoreTool) ([]actool.CoreTool, string) {
	if !findingTrafficBindingEnabled() {
		out := make([]actool.CoreTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Name() == "bind_finding_traffic" {
				continue
			}
			if agentKey == "reporter" && (tool.Name() == "traffic_search" || tool.Name() == "traffic_get" || tool.Name() == "traffic_blob") {
				continue
			}
			switch tool.Name() {
			case "report_finding", "add_hint", "add_task_hint":
				// Work on a copy: toggling back on must restore the original schema.
				raw, _ := json.Marshal(tool.InputSchema())
				var schema map[string]any
				if json.Unmarshal(raw, &schema) == nil {
					stripTrafficParameters(schema)
					tool = DecorateTool(tool, tool.Description(), schema)
				}
			}
			out = append(out, tool)
		}
		return out, ""
	}
	out := append([]actool.CoreTool(nil), tools...)
	has := map[string]bool{}
	for i, tool := range out {
		has[tool.Name()] = true
		note := ""
		switch tool.Name() {
		case "report_finding":
			note = "\nBy default the reporter Agent reviews and binds traffic before writing the report. The reporter keeps the verification commands, key output, and existing real traffic IDs and their purposes in evidence for the reporter Agent to cross-check against the execution record; no extra packet lookup is needed for binding. Explicit immediate binding is also supported: traffic_refs or evidence_hint_id may submit already-verified references, the latter reading the structured references of the specified hint in this task; if either is invalid the whole report fails. TCP / no-packet cases do not need these optional parameters. The returned finding_id and finding_node_id denote the standalone record and the exploration node respectively."
		case "add_hint", "add_task_hint":
			note = "\nWhen handing off a confirmed finding, keep the verified traffic IDs, purposes, notes, and order in the corresponding hint traffic_refs (a single one at the top level, a batch in the matching hints element), and describe in text the specific finding it proves. The caller must not hand off only text while discarding existing traffic references. Unverified candidates must not be passed on as evidence."
		case "get_finding_traffic", "bind_finding_traffic", "list_findings", "list_task_findings", "node_detail", "get_task_node_detail", "update_finding_report":
			note = findingIDGuidance
		}
		if note != "" {
			out[i] = DecorateTool(tool, tool.Description()+note, tool.InputSchema())
		}
	}
	guidance := ""
	if has["report_finding"] || has["add_task_hint"] || has["add_hint"] {
		guidance = "\n\n**Traffic evidence handoff (optional)**: By default auto-binding is done by the reporter Agent after the finding is stored and before the report is written. The reporter should keep the verification commands, key output, and existing real traffic IDs and their purposes in evidence, carrying intent_id in the task so the reporter Agent can trace it; no extra packet lookup is needed for binding. When Auto / Planner report on someone else behalf, do not discard the references the executor already has. add_hint / add_task_hint can hand off via traffic_refs; explicit immediate binding is still compatible with report_finding traffic_refs / evidence_hint_id. For TCP or no-packet cases, register normally; do not guess IDs, and do not re-probe just to collect packets."
		if has["add_task_hint"] && !has["add_hint"] {
			guidance += "\nWhen the platform conversation has no task context, do not call report_finding directly; hand off to the existing corresponding task via add_task_hint, let the task Agent register it, and verify the result with list_task_findings."
		}
		if has["prove_goal"] || has["goal_met"] {
			guidance += "\nBefore deciding the goal is complete, first finish reporting/handing off the evidence you already have. Do not end the task or cancel Workers just because the textual finding is registered while the evidence handoff is not yet complete; no-packet cases do not require waiting or forcing a capture."
		}
	}
	if has["update_finding_report"] && has["bind_finding_traffic"] && has["get_finding_traffic"] {
		guidance += "\n\n**Auto-link traffic before reporting (enabled)**: You are responsible for reviewing and binding traffic for the finding triggered this time, then writing the report. First obtain the explicit finding_id and finding_node_id from the report_finding return JSON or from get_task_node_detail / list_task_findings. Read the finding details, the execution record of the corresponding intent, and the list of existing evidence, preferring the real IDs handed off by the reporter. If this verification is HTTP and the traffic tools are available, use traffic_search to filter candidates, then use traffic_get to verify one by one that the request/response actually supports the finding; domain and time are only for filtering and do not prove attribution. Associate the confirmed evidence in reproduction order via bind_finding_traffic(finding_id, traffic_refs), choosing baseline / proof / verification / supporting and describing its purpose. Operate only on this finding; do not re-create findings or re-probe the target; after binding succeeds, call get_finding_traffic again to get the latest version, read the needed body, then pass the version you actually read as evidence_version to update_finding_report (whose finding_id parameter still takes finding_node_id). Existing bindings need not be appended again. For TCP, uncaptured, tool-unavailable, or no-exact-match cases, skip auto-binding, write the report normally based on textual/command evidence and explain why, and do not guess just to assemble traffic. Do not claim success on a failed bind; keep the existing evidence and explain in the report why nothing was bound."
	}
	if guidance != "" || has["get_finding_traffic"] || has["update_finding_report"] {
		guidance += findingIDGuidance
	}
	return out, guidance
}

func stripTrafficParameters(schema map[string]any) {
	props, _ := schema["properties"].(map[string]any)
	delete(props, "traffic_refs")
	delete(props, "evidence_hint_id")
	if required, ok := schema["required"].([]any); ok {
		kept := required[:0]
		for _, key := range required {
			if key != "traffic_refs" && key != "evidence_hint_id" {
				kept = append(kept, key)
			}
		}
		schema["required"] = kept
	}
	if hints, ok := props["hints"].(map[string]any); ok {
		if items, ok := hints["items"].(map[string]any); ok {
			stripTrafficParameters(items)
		}
	}
}

// HintTrafficSchema is shared by the task-local and cross-task hint tools.
func HintTrafficSchema() map[string]any {
	return map[string]any{"type": "array", "description": "Optional: verified traffic references corresponding to the specific finding in this hint, order preserved; after handoff, report_finding can pass evidence_hint_id to carry these references.", "items": obj(map[string]any{"traffic_id": str("real traffic ID"), "role": str("baseline / proof / verification / supporting"), "note": str("what conclusion this traffic supports")}, "traffic_id")}
}

func (t *ToolSet) findingRefsFromHint(hintID int64, explicit []db.TrafficRef) ([]db.TrafficRef, error) {
	if hintID <= 0 {
		return db.NormalizeTrafficRefs(explicit)
	}
	n, err := t.ts.GetNode(hintID) // local store only: inherited hints cannot supply evidence
	if err != nil {
		return nil, err
	}
	if n == nil || n.Kind != db.KindHint {
		return nil, fmt.Errorf("evidence_hint_id=%d must be a hint node of this task (inherited hints cannot be used directly for binding)", hintID)
	}
	var payload struct {
		Refs []db.TrafficRef `json:"traffic_refs"`
	}
	if err := json.Unmarshal(n.Payload, &payload); err != nil {
		return nil, err
	}
	return db.NormalizeTrafficRefs(append(append([]db.TrafficRef{}, explicit...), payload.Refs...))
}
