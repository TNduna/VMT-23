"use client";

// Shared components for LLM retry configuration: "attempts + interval" for each of the five retry layers.
//
// Five layers from innermost to outermost: Connect (SDK) → Empty Response (SDK) → Same-Provider Safe Window → Pool Circuit Breaker → Intent Re-run.
// The first three layers follow the endpoint, so each model profile can override the global defaults;
// the outer two layers are process-wide and only have a single global configuration.
//
// All inputs follow the same "leave empty = unconfigured" semantics, matching backend db.RetryRule:
//   Attempts: empty/0 = use built-in default | -1 = disable this retry layer | >0 = use this count
//   Interval: empty/0 = use this layer's default exponential backoff | >0 = use this fixed millisecond interval

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
  /** Where this retry layer occurs and what executes it */
  where: string;
  /** What errors reach this layer — specific down to status codes so users don't have to guess */
  trigger: string;
  /** Similar-looking errors that do NOT reach this layer, preventing confusion when tweaking knobs */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** Default attempts when left empty, used as placeholder */
  defAttempts: number;
  /** Default interval strategy when left empty, used as placeholder */
  defInterval: string;
  /** Meaning of setting attempts to -1 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connection Retry",
    where: "SDK · Before HTTP 200",
    trigger:
      "Connection failure or before receiving HTTP 200: network-level errors (connection reset, read/write timeout, DNS failure) and HTTP 408, 429, 500, 502, 503, 504.",
    skips: "Other status codes (400 / 401 / 403 / 404 / 413 / 422, etc.) are deterministic rejections that would fail again on retry and are thrown immediately.",
    desc: "Resends the exact same request. Once the stream starts (HTTP 200 received), mid-stream disconnects are handled by outer layers.",
    attemptsLabel: "Max retries",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = no retries, fail immediately",
  },
  empty: {
    title: "Empty Response Retry",
    where: "SDK · OpenAI format only",
    trigger:
      "HTTP 200 with finish_reason 'stop', but the response contains zero content blocks — caused by gateway empty frames, dropped reasoning chunks, or sampling hiccups.",
    skips: "Responses empty due to max_tokens truncation are not retried here (increase max tokens instead; retrying would just hit the limit again).",
    desc: "Resends the entire prompt, which can be expensive on long contexts — keep the count low.",
    attemptsLabel: "Max retries",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (capped at 8s)",
    offHint: "-1 = return empty response as-is",
  },
  stream: {
    title: "Same-Provider Safe Window Retry",
    where: "Host · Before any output is delivered",
    trigger:
      "Failures after the stream is established (HTTP 200 received): mid-stream disconnect, provider overloaded, or in-stream 429 / 5xx error events — while zero tokens have been emitted to the caller.",
    skips:
      "Quota exhaustion (402 / insufficient_quota, handled by pool failover), context too long (413 / context length, handled by compaction), and deterministic 400 / 401 / 403 / 404 / 422 rejections are not retried.",
    desc: "Replays the same request on the same profile. Because no output has been delivered yet, replaying will not duplicate model output or tool executions.",
    attemptsLabel: "Max retries",
    defAttempts: 2,
    defInterval: "0.5s→1s exponential (capped at 4s)",
    offHint: "-1 = escalate mid-stream disconnects directly to Intent Re-run",
  },
  breaker: {
    title: "Pool Circuit Breaker",
    where: "Host · Process-wide, global",
    trigger:
      "Trips when consecutive transient failures (429, 5xx, network errors) reach the threshold; deterministic failures like insufficient quota (402), invalid key (401 / 403), or missing model (404) trip on the first occurrence regardless of threshold.",
    skips: "Resets to zero on a single success, so occasional hiccups won't accumulate into a trip.",
    desc: "Enters cooldown after tripping; the pool skips this profile during cooldown. State is persisted to the DB across restarts.",
    attemptsLabel: "Consecutive failures to trip",
    defAttempts: 3,
    defInterval: "1min→5min→30min stepped cooldown",
    offHint: "-1 = never trip on transient failures (deterministic failures still trip)",
  },
  intent: {
    title: "Intent Re-run",
    where: "Host · Process-wide, global",
    trigger:
      "When inner layers fail to recover: the worker terminates with model_error — either inner retries were exhausted, or the stream disconnected after output had already started (where replay is unsafe and the whole intent must restart).",
    skips: "Quota exhaustion is already handled by pool failover and is not re-run here; yields immediately without waiting for backoff if the task is paused, stopped, or finalizing.",
    desc: "Re-runs the entire intent from scratch. As the outermost layer, each re-run multiplies the retry counts of all inner layers.",
    attemptsLabel: "Max re-runs",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = do not re-run, mark intent as blocked",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** Human-readable milliseconds, echoed next to the input so users don't have to count zeros. */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** Controlled numeric input: empty string ↔ 0, keeping intermediate states ("-", "1e") local without disturbing the parent. */
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
  // Sync when the parent swaps in a new set of values (loaded policy or switched profile);
  // typing locally won't trigger this because `value` already matches the parsed local text.
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

/** Two knobs for a single retry layer. idPrefix preserves unique label htmlFor attributes when rendered multiple times on a page. */
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
  /** true = compact mode inside the profile drawer: omits extended descriptions and keeps only the trigger summary */
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
        {/* Which errors reach this layer, down to status codes — if tweaking a knob seems to have no effect, the error likely doesn't land in this layer. */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Triggers on</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Skipped on</span>: {meta.skips}
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
            Interval (ms)
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
      {!compact && <p className="text-muted-foreground text-xs">Leave empty = use default; {meta.offHint}.</p>}
    </div>
  );
}

/** Three-layer retry overrides inside the model profile drawer (the three endpoint-scoped layers). */
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
        <Label className="text-sm">Retry Overrides</Label>
        <p className="text-muted-foreground text-xs">
          Applies only to this profile, overriding the global defaults in &ldquo;Retry &amp; Backoff&rdquo;. Leave empty to inherit global settings; set attempts to -1 to disable a layer; set an interval to replace exponential backoff with a fixed delay. Circuit breaker and intent re-run are process-wide and configured on the global tab.
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

/** "Retry & Backoff" tab: global default settings for all five layers. */
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
      // Backend clamps out-of-range values and returns the saved policy; refresh directly from the response.
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved and applied immediately (in-flight calls keep previous parameters)");
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
        A failed model call passes through five retry layers from innermost to outermost:
        <span className="text-foreground"> Connection → Empty Response → Same-Provider Safe Window → Pool Circuit Breaker → Intent Re-run</span>
        . Outer layers only run after inner layers are exhausted, meaning retry counts
        <span className="text-foreground"> multiply</span>
        — maxing out every layer can burn dozens of requests on a single blip.
        Leaving all fields empty uses the built-in defaults. The first three layers can be overridden per model profile.
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
          Reset All to Default
        </Button>
      </div>
    </div>
  );
}
