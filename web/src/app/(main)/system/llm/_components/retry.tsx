"use client";

// Shared pieces for LLM retry config: "attempts + interval" for each of the five retry layers.
//
// The five layers, inner to outer: connect (SDK) → empty response (SDK) → same-provider safe window → rotation circuit breaker → intent rerun.
// The first three follow the endpoint, so each model profile can override the global defaults; the last two are process-wide with a single global copy.
//
// All inputs follow the same "blank = not configured" semantics, matching backend db.RetryRule:
//   attempts   blank/0 = built-in default | -1 = disable this layer | >0 = use this count
//   interval   blank/0 = this layer's native exponential backoff | >0 = use this fixed interval in ms

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** Where this retry layer happens and who performs it */
  where: string;
  /** Which errors reach this layer - down to status codes, so nobody has to guess */
  trigger: string;
  /** Look-alike errors that do NOT go through this layer, so a setting with no effect isn't mistaken for a bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Default when attempts is blank, used as placeholder */
  defAttempts: number;
  /** Default strategy when interval is blank, used as placeholder */
  defInterval: string;
  /** Meaning of attempts = -1 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connect retry",
    where: "SDK · before receiving 200",
    trigger:
      "Cannot connect or no 200 yet: network-level errors such as connection reset / read-write timeout / DNS failure, plus HTTP 408, 429, 500, 502, 503, 504.",
    skips: "Other status codes (400 / 401 / 403 / 404 / 413 / 422, etc.) are deterministic rejections; resending fails the same way, so they are raised immediately.",
    desc: "Resends the identical request. Once the stream has started (200 received), a mid-stream disconnect is no longer handled by this layer.",
    attemptsLabel: "Retry attempts",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = never retry; failures are raised immediately",
  },
  empty: {
    title: "Empty-response retry",
    where: "SDK · openai format only",
    trigger:
      "HTTP 200 with a normal stop finish_reason, but the response contains no content blocks at all - gateway empty frames, dropped thinking frames and sampling hiccups all look like this.",
    skips: "Empty output due to max_tokens truncation does not count (raise the output limit instead; resending would just hit it again).",
    desc: "Resends the entire prompt, so it is expensive on long contexts; keep the count small.",
    attemptsLabel: "Retry attempts",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = return empty responses as-is",
  },
  stream: {
    title: "Same-provider safe-window retry",
    where: "This project · before any output is delivered",
    trigger:
      "Problems after the stream is established (200 received): mid-stream disconnect, provider overloaded, in-stream 429 / 5xx error events - and not a single token has been delivered to the caller yet.",
    skips:
      "Quota exhausted (402 / insufficient_quota, handled by rotating profiles), context too long (413 / context length, handled by compaction), and deterministic 400 / 401 / 403 / 404 / 422 rejections are not retried.",
    desc: "Replays the same request on the same profile. Since no output has been delivered yet, replaying does not duplicate model output or tool execution.",
    attemptsLabel: "Retry attempts",
    defAttempts: 2,
    defInterval: "0.5s→1s exponential (capped at 4s)",
    offHint: "-1 = hand broken streams straight to the outer intent rerun",
  },
  breaker: {
    title: "Rotation circuit breaker",
    where: "This project · process-wide, single global copy",
    trigger:
      "Trips when consecutive transient failures (429, 5xx, network errors) reach the threshold; deterministic failures such as insufficient balance (402), invalid key (401 / 403) or model not found (404) ignore the threshold and trip on the first occurrence.",
    skips: "A single success resets the count, so an occasionally flaky profile won't slowly accumulate its way into tripping.",
    desc: "After tripping, the profile enters a cooldown during which rotation skips it. State is persisted to the DB and survives restarts.",
    attemptsLabel: "Consecutive failures to trip",
    defAttempts: 3,
    defInterval: "1min→5min→30min tiers",
    offHint: "-1 = never trip on transient failures (deterministic failures still trip)",
  },
  intent: {
    title: "Intent rerun",
    where: "This project · process-wide, single global copy",
    trigger:
      "None of the earlier layers caught it: the worker ended with model_error - all inner retries exhausted, or the stream broke after output had started being delivered (replay is unsafe then, so the whole thing must restart).",
    skips: "Quota exhaustion is already handled by profile rotation and is not rerun here; when the task is paused / terminated / wrapping up, it yields immediately without waiting out the backoff.",
    desc: "Reruns the whole intent from scratch. It is the outermost layer, so each rerun multiplies the attempts of all inner layers again.",
    attemptsLabel: "Rerun attempts",
    defAttempts: 2,
    defInterval: "fixed 3s",
    offHint: "-1 = no rerun; the intent is marked blocked directly",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Human-readable milliseconds, only echoed next to the input so nobody has to count zeros. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Controlled number input: empty string ↔ 0; intermediate states ("-", "1e") stay local without bothering the parent. */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // Follow along when the parent swaps in a whole new set of values (policy loaded, profile switched); typing
  // doesn't land here, because value then already equals the parsed local text.
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** The two knobs for one retry layer. idPrefix keeps label htmlFor unique when rendered multiple times on a page. */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = compact version for the profile drawer: drop the extended explanation, keep only the "which errors reach this layer" line */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* Which errors reach this layer, down to status codes - if a knob seems to have no effect, the error most likely never lands in this layer. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Trigger</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Not handled here</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            Interval ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Default backoff"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Blank = use default; {meta.offHint}.</p>}
    </div>
  );
}

/** The three overrides in the model profile drawer (the three layers that follow the endpoint). */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Retry overrides</Label>
        <p className="text-muted-foreground text-xs">
          Applies only to this profile, overriding the global defaults under "Retry & backoff". Blank field = follow global; attempts -1 = disable that layer;
          setting an interval replaces exponential backoff with a fixed interval. The circuit breaker and intent rerun are process-wide and can only be tuned on the global page.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** "Retry & backoff" tab: global defaults for all five layers. */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Failed to load retry policy: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // The backend clamps out-of-range values and returns them; refresh from the response so what you see is what's saved.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved and effective immediately (calls already in flight still use the old parameters)");
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> Loading retry policy…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        A failed model call passes through five retry layers in order, inner to outer:
        <span className="text-foreground"> connect → empty response → same-provider safe window → rotation circuit breaker → intent rerun</span>
        . The outer layer only kicks in once the inner one is exhausted, so attempts
        <span className="text-foreground"> multiply</span>
        - max out every layer and a single hiccup can burn dozens of requests.
        Leaving everything blank uses the current defaults, behaving exactly as if this page didn't exist. The first three layers can be overridden per model profile.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Save
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          Reset all to defaults
        </Button>
      </div>
    </div>
  );
}
