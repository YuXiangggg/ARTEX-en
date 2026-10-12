// Channel field table and config value parsing helpers.
//
// Split from the page because this is **data**, not view: it describes which fields each
// channel has, which control each should use, and the two-way conversion between form
// text and config values (JSON). With it in its own file, adding a channel only touches
// this file; the page itself doesn't change.
// Display names and descriptions of channel types. Kept on the frontend because they only affect copy; the backend doesn't need them.
export const KIND_LABEL: Record<string, string> = {
  dingtalk: "DingTalk",
  feishu: "Feishu",
  wecom: "WeCom",
  webhook: "Generic Webhook",
  telegram: "Telegram",
  email: "Email",
};

// Config field definitions for each channel.
//
// A frontend field table is kept here on purpose instead of having the backend ship a schema:
// the backend only handles Validate (required/format), while the UI needs layout and control
// types - the two concerns are different.
// The only coupling point is secret_keys - the backend says which fields render as password
// boxes, since only the channel implementation knows which values are credentials (WeCom's
// entire Webhook is a credential, while DingTalk's is just one secret). A missing entry here
// for a new channel only makes the form blank; it doesn't fail silently (hasFields below flags it).
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
      label: "Signing secret",
      kind: "password",
      help: "Fill in when the bot security setting is \"Sign\"; leave empty for \"Custom keywords\" or when security settings are off",
    },
  ],
  feishu: [
    {
      key: "webhook",
      label: "Webhook URL",
      kind: "text",
      placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...",
    },
    { key: "secret", label: "Signature verification secret", kind: "password", help: "Fill in when the bot has \"Signature verification\" enabled; otherwise leave empty" },
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
      label: "Request method",
      kind: "select",
      options: [
        { value: "POST", label: "POST (with body)" },
        { value: "PUT", label: "PUT (with body)" },
        { value: "PATCH", label: "PATCH (with body)" },
        { value: "GET", label: "GET (no body)" },
      ],
    },
    { key: "headers", label: "Custom headers", kind: "kv", help: "One KEY=VALUE per line, e.g. Authorization=Bearer xxx" },
    {
      key: "body_template",
      label: "Body template",
      kind: "textarea",
      help:
        "Leave empty for the built-in default template. Variables: {{.Title}} {{.Batch}} {{.Count}} {{.HomeURL}} {{.SentAt}}, " +
        "plus .Name/.VulnClass/.Severity/.Summary/.Assets/.DetailURL/.StatusLabel under range .Items. " +
        "Insert strings with {{json .Xxx}} rather than {{.Xxx}}, or quotes in titles will break the JSON.",
    },
  ],
  telegram: [
    { key: "bot_token", label: "Bot Token", kind: "password", placeholder: "123456:ABC-DEF..." },
    { key: "chat_id", label: "Chat ID", kind: "text", placeholder: "-1001234567890" },
    {
      key: "base_url",
      label: "API URL",
      kind: "text",
      placeholder: "https://api.telegram.org",
      help: "Leave empty for the official URL; fill in when using a self-hosted Bot API reverse proxy",
    },
  ],
  email: [
    { key: "host", label: "SMTP server", kind: "text", placeholder: "smtp.example.com" },
    {
      key: "port",
      label: "Port",
      kind: "number",
      placeholder: "587",
      help: "587 uses STARTTLS; for 465, turn on \"Implicit TLS\"",
    },
    { key: "username", label: "Username", kind: "text" },
    { key: "password", label: "Password / app password", kind: "password" },
    { key: "from", label: "From", kind: "text", placeholder: "artex@example.com" },
    { key: "to", label: "To", kind: "list", help: "Separate multiple addresses with commas" },
    { key: "tls", label: "Implicit TLS", kind: "switch", help: "On for port 465; keep off for 587 (STARTTLS is automatic)" },
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
// parseIDs parses a comma/whitespace-separated id list (full-width commas accepted too).
export function parseIDs(text: string): number[] {
  return text
    .split(/[\s,\uFF0C]+/)
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => Number(s))
    .filter((n) => Number.isFinite(n) && n > 0);
}
// parseKeywords parses a newline/comma-separated keyword list (vuln class names may contain spaces, so split on newlines or commas; full-width commas accepted too).
export function parseKeywords(text: string): string[] {
  return text
    .split(/[\n,\uFF0C]+/)
    .map((s) => s.trim())
    .filter(Boolean);
}
