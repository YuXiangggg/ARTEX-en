package notify

// Snapshot is the contract for the notification_events.snapshot JSONB column. The writer is the db layer's
// finding-persistence transaction; the readers are the server layer's delivery engine and filter matching. It is
// defined in this package because it is a "notification domain" payload: db only serializes it and does not
// understand the fields.
//
// Why finding fields are stored redundantly rather than looked up at render time: findings get renamed, re-graded
// and change status afterwards, while a push should reflect the conclusion **at the time of the event**; a lookup
// could yield the dangerously misleading "later changed to low". It also spares fan-out and rendering from JOINing
// the findings/tasks/assets tables.
type Snapshot struct {
	// Event type: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Non-empty only when kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is one finding to be pushed, for channel rendering.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets are the resolved asset display names (e.g. domain/IP). Filled in by the server layer:
	// this package does not touch the database and cannot get the names.
	Assets []string
	// DetailURL links back to the finding detail; empty means public_base_url is not configured, and it is omitted when rendering.
	DetailURL string
	// Only for status change events; when both are non-empty it renders as "Pending → Fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether the item is a status change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the item's display title: prefer the human-assigned name, fall back to the vulnclass,
// and use a placeholder when both are empty; never output an empty title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(unnamed finding)"
}

// Message is the complete content of one channel send.
type Message struct {
	// Length 1 for a single push; a whole batch for a digest push.
	// An empty slice is invalid; the caller must guarantee at least one item.
	Items []Item
	// When Batch=true it renders as a digest (different title, with time window and item count).
	Batch bool
	// WindowMinutes is the digest period (minutes), used only when Batch=true for the text "last N minutes".
	// Deliberately passed explicitly from config rather than computed via time.Since at render time: keeping rendering deterministic makes it testable.
	WindowMinutes int
	// HomeURL is the platform dashboard address (global public_base_url); if empty, no dashboard link is included.
	HomeURL string
}
