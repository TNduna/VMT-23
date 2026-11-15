package notify

// Snapshot is the contract for the notification_events.snapshot JSONB column. The writer is the finding
// persistence transaction in the db layer, and the readers are the delivery engine and filter matcher in the server layer.
// It is defined in this package because it is a "notification domain" payload: db only handles serialization without understanding field semantics.
//
// Why store finding fields redundantly instead of querying them back at render time: findings may be renamed, re-graded, or have their status changed later,
// whereas notifications should reflect the conclusion **at the time of the event**—querying back could yield a dangerously misleading
// "later downgraded to low". Furthermore, fan-out and rendering do not need to JOIN across findings/tasks/assets tables.
type Snapshot struct {
	// Event kind: finding_created / finding_status_changed
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

// Item is a pending vulnerability finding to be rendered by a channel.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets are the resolved display names of assets (e.g., domain/IP). Populated by the server layer—
	// this package does not touch the database and cannot resolve names on its own.
	Assets []string
	// DetailURL is the link back to the finding detail page; empty means public_base_url is not configured and the link is omitted when rendering.
	DetailURL string
	// Dedicated to status-change events; when non-empty, rendered as "Pending → Fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether the item represents a status-change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the display title of the item: prefers the human-assigned name, falls back to vulnclass,
// and uses a placeholder if both are empty—never outputs an empty title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(Unnamed Vulnerability)"
}

// Message is the complete content of a single channel delivery.
type Message struct {
	// Length is 1 for single-item pushes, or an entire batch for digest pushes.
	// An empty slice is invalid; callers must guarantee at least one item.
	Items []Item
	// When Batch=true, renders as a digest message (different title, includes time window and count).
	Batch bool
	// WindowMinutes is the digest window (in minutes), used only when Batch=true for the "Last N minutes" copy.
	// Intentionally passed explicitly from configuration rather than computing time.Since at render time so rendering stays deterministic and testable.
	WindowMinutes int
	// HomeURL is the platform dashboard URL (global public_base_url); if empty, no dashboard link is included.
	HomeURL string
}
