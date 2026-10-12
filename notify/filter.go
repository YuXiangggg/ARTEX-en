package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: a channel instance's filter conditions.
// All fields are optional and default to "no filtering", which is exactly the fallback semantics for malformed config; see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical); empty = no threshold.
	MinSeverity string `json:"min_severity"`
	// Empty TaskIDs / AssetIDs mean unrestricted; when non-empty the event must intersect them.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// Empty VulnClassInclude accepts everything; when non-empty the vulnclass must match one of its keywords.
	// Matching any VulnClassExclude keyword excludes the event (exclusion takes precedence over inclusion).
	// Matching is a case-insensitive substring, which is safer than regex: a mistyped regex cannot silently disable the channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange decides whether the channel receives finding status change events (meaningful only in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses a channel's filter config.
//
// **It never returns an error.** This is a deliberate design choice: a malformed filter config always degrades to
// the zero-value Filter (= no filtering = everything matches), because for a vulnerability notification system
// **one extra push is far better than silently missing a high-severity finding**. Turning a parse failure into
// "don't push" would hand the user a channel that looks configured but actually pushes nothing, the worst failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On parse failure f stays zero-valued, i.e. no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid severity threshold (empty string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks the **value-restricted** fields of a filter config; called when a channel is saved.
//
// Why it must be blocked on write: Match evaluates an unknown threshold as `rank >= 0`, which is always true,
// so a single typo in min_severity ("hgih") makes the filter **silently stop working** and become "push everything".
// That is consistent with this package's "better to over-push than miss" trade-off (nothing is missed),
// but the result is that the user thinks they have severity-based pushes while actually flooding the group with
// every finding, with no hint that they misconfigured it. This kind of "silent degradation" should be stopped at the entry point.
//
// Note Validate is used only on the **write** path. The read path still uses ParseFilter's lenient semantics,
// so bad values already in historical data don't make the whole channel unreadable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("invalid minimum severity %q; options: low / medium / high / critical, or leave empty for no limit", f.MinSeverity)
	}
	return nil
}

// Match decides whether an event should be delivered to a channel with this filter.
//
// **It never returns an error**, for the same reason as ParseFilter: any internal anomaly is treated as a "match".
// Evaluation order: event type → severity threshold → task/asset scope → vulnclass keywords.
func Match(f Filter, s Snapshot) bool {
	// Status change events are only received by channels that explicitly opt in. Off by default, because most users
	// expect "push" to mean "new finding discovered", not a running log of every status transition.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion first: matching any exclude keyword rules it out, even if the include list also matches.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A linear scan is fine for small sets; both sides are on the order of "a few dozen picked by hand",
	// so building a map costs more than it saves.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of keywords (case-insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
