// Package notify implements the IM / email notification channel adapter layer for vulnerability findings.
//
// Layering: this is a **leaf package** depending only on the standard library. It has no knowledge of the database or server.
// Channel configuration is passed in as map[string]any (corresponding to the notification_channels.config JSONB column),
// and the content to deliver is passed in as Message. Separating it this way allows signature calculation, UTF-8 truncation,
// and filter matching—the parts most prone to subtle bugs—to be unit-tested without PostgreSQL, leaving orchestration to the server layer.
//
// Concurrency contract: Channel implementations must be **stateless**. A single Channel instance is reused concurrently
// across multiple channel configurations (even multiple bot instances of the same channel kind); all credentials must be passed
// via the cfg parameter, and implementations must never cache things like webhook URLs in struct fields.
package notify

// Channel kind identifiers. These values also form the valid set for notification_channels.kind, validated against
// an allowlist on the server side (just like findings.status, avoiding DB CHECK constraints to make adding channels easier).
const (
	KindDingTalk = "dingtalk" // DingTalk custom bot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom bot
	KindWeCom    = "wecom"    // WeCom group bot
	KindWebhook  = "webhook"  // Generic Webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event kinds, corresponding to notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the fallback value when kind in config is empty.
const InitKind = KindDingTalk

// severityRank maps vulnerability severity to a comparable ordinal. Unknown severities return 0, so any
// min_severity setting blocks unknown severities—when in doubt, do not push, avoiding false-positive spam.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the ordinal of a severity; unknown severities return 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel returns the severity label with an emoji, used for message titles and card color schemes.
// Unknown severities are echoed as-is without fabrication.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Critical"
	case "high":
		return "🟠 High"
	case "medium":
		return "🟡 Medium"
	case "low":
		return "🔵 Low"
	default:
		return severity
	}
}

// StatusLabel translates a finding status into a human-readable label for status-change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "In Progress"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Resolved"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "False Positive"
	case "ignored":
		return "Ignored"
	case "duplicate":
		return "Duplicate"
	case "risk_accepted":
		return "Risk Accepted"
	default:
		return status
	}
}

// AtLeast reports whether severity meets the min threshold. An empty min means no threshold and always passes.
// Note that an unknown severity has rank 0 and will be rejected by any non-empty min (see severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
