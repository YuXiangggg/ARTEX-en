export const mentionKinds = [
  { kind: "finding", label: "Finding", alias: "finding" },
  { kind: "asset", label: "Asset", alias: "asset" },
  { kind: "company", label: "Company", alias: "company" },
  { kind: "endpoint", label: "Endpoint", alias: "api" },
  { kind: "ip", label: "IP", alias: "ip" },
  { kind: "app", label: "App", alias: "app" },
  { kind: "root_domain", label: "Domain", alias: "domain" },
  { kind: "subdomain", label: "Subdomain", alias: "subdomain" },
  { kind: "service", label: "Service", alias: "service" },
] as const;

export type MentionKind = (typeof mentionKinds)[number]["kind"];
export interface ChatMention {
  kind: MentionKind;
  id: number;
  label: string;
  description: string;
}

export function activeMention(value: string, caret: number) {
  const before = value.slice(0, caret);
  const start = before.lastIndexOf("@");
  if (start < 0 || (start > 0 && /[\w.+/-]/.test(before[start - 1]))) return null;
  const query = before.slice(start + 1);
  if (/[[\]\r\n@]/.test(query) || query.length > 220) return null;
  return { start, end: caret, query };
}

export function mentionSearch(query: string) {
  const text = query.trimStart().toLowerCase();
  for (const item of mentionKinds) {
    for (const alias of [item.label.toLowerCase(), item.alias]) {
      if (text === alias || text.startsWith(`${alias} `)) {
        return { kind: item.kind, query: query.trimStart().slice(alias.length).trim(), categories: [] };
      }
    }
  }
  const categories = mentionKinds.filter(
    (item) => item.label.toLowerCase().startsWith(text) || item.alias.startsWith(text),
  );
  return { kind: "" as const, query: query.trim(), categories };
}

export function mentionToken(item: ChatMention) {
  const kind = mentionKinds.find((entry) => entry.kind === item.kind)?.label ?? "Asset";
  const label = item.label
    .replace(/[[\]]/g, (char) => (char === "[" ? "(" : ")"))
    .replace(/\s+/g, " ")
    .slice(0, 100);
  return `@[${kind}#${item.id} ${label}]`;
}

export function selectedMentions(value: string) {
  return [
    ...value.matchAll(/@\[(Finding|Asset|Company|Endpoint|IP|App|Domain|Subdomain|Service)#([0-9]+)(?: ([^\]\r\n]*))?\]/g),
  ].map((match) => ({
    token: match[0],
    label: `${match[1]} #${match[2]}${match[3] ? ` · ${match[3]}` : ""}`,
    start: match.index,
  }));
}
