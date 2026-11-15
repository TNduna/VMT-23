// Channel field table and configuration value parsing utilities.
//
// Separated from the page because this is **data** rather than view: it describes
// which fields each channel type has, what control each should use, and two-way
// conversion between form text and JSON configuration values.
// Keeping it in its own file means adding a new channel only requires editing here
// without touching the page component itself.
// Display names for channel types. Kept on the frontend since they only affect UI copy.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "DingTalk",
  feishu: "Feishu",
  wecom: "WeCom",
  webhook: "Generic Webhook",
  telegram: "Telegram",
  email: "Email",
};

// Configuration field definitions for each channel.
//
// We intentionally maintain a frontend field table instead of having the backend
// serve a schema: the backend is only responsible for validation (required/format),
// while the UI cares about layout and control types — two different concerns.
// The only coupling point is secret_keys — which fields render as password inputs
// is specified by the backend, since only the channel implementation knows which
// values are credentials (in WeCom the entire Webhook URL is a credential, whereas
// in DingTalk only the signing secret is). If a new channel is missing an entry here,
// the form simply renders empty rather than failing silently (the page shows a hint).
export type FieldKind = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export interface FieldDef {
  key: string;
  label: string;
  kind: FieldKind;
  placeholder?: string;
  help?: string;
  options?: { value: string; label: string }[];
}
export const CHANNEL_FIELDS: Record<string, FieldDef[]> = {
  dingtalk: [
    {
      key: "webhook",
      label: "Webhook URL",
      kind: "text",
      placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...",
    },
    {
      key: "secret",
      label: "Signing Secret",
      kind: "password",
      help: "Fill in when bot security is set to 'Signature'; leave empty if using 'Custom Keywords' or no security settings",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook URL",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "Signature Verification Secret", kind: "password", help: "Fill in when 'Signature Verification' is enabled on the bot; otherwise leave empty" },
  ],
  wecom: [
    {
      key: "webhook",
      label: "Webhook URL",
      kind: "text",
      placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...",
    },
  ],
  webhook: [
    { key: "url", label: "Target URL", kind: "text", placeholder: "https://your-endpoint.example.com/hook" },
    {
      key: "method",
      label: "HTTP Method",
      kind: "select",
      options: [
        { value: "POST", label: "POST (with body)" },
        { value: "PUT", label: "PUT (with body)" },
        { value: "PATCH", label: "PATCH (with body)" },
        { value: "GET", label: "GET (without body)" },
      ],
    },
    { key: "headers", label: "Custom Headers", kind: "kv", help: "One KEY=VALUE per line, e.g. Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "Request Body Template",
      kind: "textarea",
      help:
        "Leave empty to use the built-in default template. Variables: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "and under range .Items: .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel. " +
        "Use {{json .Xxx}} instead of {{.Xxx}} when interpolating strings so quotes in titles don't break JSON.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API Base URL",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "Leave empty to use the official URL; fill in when using a self-hosted Bot API reverse proxy",
    },
  ],
  email: [
    { key: "host", label: "SMTP Server", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "Port",
      kind: "number",
      placeholder: "587",
      help: "Port 587 uses STARTTLS; for port 465, enable 'Implicit TLS'",
    },
    { key: "username", label: "Username", kind: "text" },
    { key: "password", label: "Password / App Token", kind: "password" },
    { key: "from", label: "Sender (From)", kind: "text", placeholder: "vmt-23@example.com" },
    { key: "to", label: "Recipients (To)", kind: "list", help: "Separate multiple addresses with commas" },
    { key: "tls", label: "Implicit TLS", kind: "switch", help: "Enable for port 465; keep off for port 587 (uses STARTTLS automatically)" },
  ],
};

export const SEVERITY_OPTIONS = [
  { value: "", label: "Any" },
  { value: "low", label: "Low and above" },
  { value: "medium", label: "Medium and above" },
  { value: "high", label: "High and above" },
  { value: "critical", label: "Critical only" },
];

export type ChannelForm = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};

export const emptyForm = (kind: string): ChannelForm => ({
  name: "",
  kind,
  mode: "realtime",
  enabled: true,
  ratePerMin: "",
  config: {},
  minSeverity: "",
  includeText: "",
  excludeText: "",
  taskIDsText: "",
  assetIDsText: "",
  onStatusChange: false,
});

// parseKV parses a "one KEY=VALUE per line" textarea.
export function parseKV(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i > 0) out[t.slice(0, i).trim()] = t.slice(i + 1).trim();
  }
  return out;
}
// parseIDs parses a comma/whitespace-separated list of numeric IDs.
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords parses a newline/comma-separated list of keywords (vulnerability class names may contain spaces, so split only on newlines or commas).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
