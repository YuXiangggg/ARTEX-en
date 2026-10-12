// Centralised status → color/label semantics, reused across the whole app.
// Spec §8.3: intent / coverage / task / severity each have a consistent color set.

export type Tone = "neutral" | "blue" | "green" | "amber" | "red" | "rose" | "violet" | "slate";

export const toneClasses: Record<Tone, string> = {
  neutral: "bg-muted text-muted-foreground border-transparent",
  blue: "bg-blue-500/15 text-blue-600 dark:text-blue-400 border-blue-500/20",
  green: "bg-emerald-500/15 text-emerald-600 dark:text-emerald-400 border-emerald-500/20",
  amber: "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20",
  red: "bg-red-500/15 text-red-600 dark:text-red-400 border-red-500/20",
  // rose is used for "critical" -- solid, high-emphasis, visually clearly above the soft red outline of "high".
  rose: "bg-rose-600 text-white border-rose-600 dark:bg-rose-600 dark:text-white",
  violet: "bg-violet-500/15 text-violet-600 dark:text-violet-400 border-violet-500/20",
  slate: "bg-slate-500/15 text-slate-600 dark:text-slate-400 border-slate-500/20",
};

export const toneDot: Record<Tone, string> = {
  neutral: "bg-muted-foreground",
  blue: "bg-blue-500",
  green: "bg-emerald-500",
  amber: "bg-amber-500",
  red: "bg-red-500",
  rose: "bg-white",
  violet: "bg-violet-500",
  slate: "bg-slate-500",
};

interface StatusMeta {
  label: string;
  tone: Tone;
}

const intent: Record<string, StatusMeta> = {
  open: { label: "Unclaimed", tone: "slate" },
  running: { label: "Running", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  done: { label: "Done", tone: "green" },
  // blocked = model/API/network failures exhausted retries; this intent was essentially never truly explored (not an on-target intercept).
  blocked: { label: "Error", tone: "red" },
  // exhausted = cut off midway on reaching the step/time budget, with only partial results written back (not that the direction was fully explored).
  exhausted: { label: "Budget exhausted", tone: "violet" },
  // stopped = legacy soft-deleted state (retained, historical data).
  stopped: { label: "Stopped", tone: "slate" },
  // deleted = the user soft-deleted this intent (node and lineage retained; see the delete_reason field for the reason).
  deleted: { label: "Deleted", tone: "slate" },
};

const task: Record<string, StatusMeta> = {
  created: { label: "Created", tone: "slate" },
  queued: { label: "Queued", tone: "amber" },
  running: { label: "Running", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  done: { label: "Done", tone: "green" },
  failed: { label: "Failed", tone: "red" },
  timeout: { label: "Timed out", tone: "amber" },
};

const severity: Record<string, StatusMeta> = {
  critical: { label: "Critical", tone: "rose" },
  high: { label: "High", tone: "red" },
  medium: { label: "Medium", tone: "amber" },
  low: { label: "Low", tone: "slate" },
};

const finding: Record<string, StatusMeta> = {
  pending: { label: "Pending", tone: "amber" },
  in_progress: { label: "In progress", tone: "blue" },
  confirmed: { label: "Confirmed", tone: "red" },
  resolved: { label: "Resolved", tone: "green" },
  fixed: { label: "Fixed", tone: "green" },
  false_positive: { label: "False positive", tone: "slate" },
  ignored: { label: "Ignored", tone: "neutral" },
  duplicate: { label: "Duplicate", tone: "neutral" },
  risk_accepted: { label: "Risk accepted", tone: "violet" },
};

const engine: Record<string, StatusMeta> = {
  exploring: { label: "Exploring", tone: "blue" },
  paused: { label: "Paused", tone: "amber" },
  stalled: { label: "Stalled", tone: "red" },
  idle: { label: "Idle", tone: "neutral" },
};

const goal: Record<string, StatusMeta> = {
  open: { label: "Open", tone: "blue" },
  met: { label: "Met", tone: "green" },
  abandoned: { label: "Abandoned", tone: "slate" },
};

const audit: Record<string, StatusMeta> = {
  allow: { label: "Allow", tone: "green" },
  block: { label: "Block", tone: "red" },
};

const node: Record<string, StatusMeta> = {
  observed: { label: "Observed", tone: "slate" },
  confirmed: { label: "Confirmed", tone: "green" },
  tombstoned: { label: "Tombstoned", tone: "neutral" },
};

// Push delivery status. sending uses blue rather than amber: it is not "something is wrong",
// but "claimed and being sent", which must be distinguishable from pending's waiting semantics.
const delivery: Record<string, StatusMeta> = {
  pending: { label: "Pending", tone: "amber" },
  sending: { label: "Sending", tone: "blue" },
  sent: { label: "Delivered", tone: "green" },
  failed: { label: "Failed", tone: "red" },
  skipped: { label: "Skipped", tone: "neutral" },
};

const maps = {
  intent,
  task,
  severity,
  finding,
  engine,
  goal,
  audit,
  node,
  delivery,
} as const;

export type StatusDomain = keyof typeof maps;

export function statusMeta(domain: StatusDomain, key: string): StatusMeta {
  return maps[domain][key] ?? { label: key, tone: "neutral" };
}
