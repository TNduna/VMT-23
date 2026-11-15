# Side Question Long Conversations & Context Budget

Fixed on 2026-09-11. The original implementation treated request JSON character length directly as tokens and inherited the main task's 32K output reservation, prematurely rejecting valid side questions when large amounts of HTML, JS, or tool outputs were present.

## Open Source Implementations Review

- [Grok CLI Side Context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): Extracts snippets from recent user and assistant messages with an approximate 2000-character budget, capping each item at 400 characters. Does not maintain continuous side-question conversation history on this path.
- [Grok CLI Independent Request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): Independent cancellation signal; output capped at 2048 tokens where supported by models; provides no tools.
- [Grok CLI Main Session Compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): Estimates tokens, preserves recent content, folds new content into existing summaries, and handles cross-turn truncation.
- [OpenCode Session Compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): Full request estimation, output/buffer reservation, recent content plus rolling summary, tool-free summary requests; defaults to 8000 recent budget and 4096 tokens summary output cap.
- [OpenCode Overflow Recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): Attempts overflow recovery only before assistant output has begun; subsequent calls do not re-enter the recovery loop.

VMT-23 adapts the concepts of independent output budgets, recent content with rolling summaries, and bounded recovery. It retains norma v0.3.6 structured messages and tool pairing, avoiding Grok's plain text excerpts; OpenCode's main session compaction events are not written to the VMT-23 main transcript.

## Request Budget & Execution

- Messages follow norma's content-block UTF-8 byte estimation with a 4/3 buffer margin; system prompts, tool schemas, and message envelope overheads are accounted for. Estimations are approximations rather than exact model token counts.
- Side question output defaults to at most 8192 tokens, never exceeding the main profile's configured output limit. The service environment variable `VMT-23_BTW_MAX_OUTPUT_TOKENS` can configure limits between 256–32768 tokens; product default models or main task parameters are not modified.
- Input budget equals the context window minus output ceiling and safety margin; unknown context windows default to the platform fallback of 200K. Safety margins are 5% of the window, bounded between 128 and 8192 tokens.
- Successful Q&A pairs are loaded in batches of up to 20 by ascending ordinal. Up to 20 raw pairs are preserved, with token budget capped at 1/4 of input budget and no more than 16K.
- Surplus Q&A pairs are folded into a rolling summary. Summaries carry source history and context timestamps; historical assistant answers do not replace fresh tool evidence, with conflicts resolving in favor of the latest main snapshot.
- If the main context still exceeds budget, only older messages in the replica are summarized, retaining up to 8K tokens of recent content; truncation points never split tool calls from their results. Excessively large individual items enter the summary as whole units.
- Summary inputs are chunked in UTF-8 boundaries according to actual remaining window capacity, with an output ceiling of 2048 tokens; empty summaries, truncated outputs, tool calls, or budget overruns are excluded from cache writes. A single side question allows up to 12 summarization calls under the shared 120-second timeout; reaching this ceiling triggers explicit failure rather than infinite recursion.
- If the model returns a context window overflow on the initial invocation before any text or tool calls have emitted, it undergoes further reduction and retries once; if estimated size does not decrease, recovery terminates immediately. Other model errors and partial streaming outputs do not trigger recovery.
- All recorded usage metrics—including summarization, failed attempts, and cancellation usage—accumulate onto the same side-question request. When providers omit usage, zero is recorded rather than masking estimates as actual usage.

## Persistence & Interface

`side_question_sessions.memory` persists older Q&A summaries, covered ordinals, and main context summaries cached by snapshot identity. `side_question_requests.context_info` records preparation stages, actual replayed groups, summary utilization, and budget estimates.

Summaries are stored only if the parent request is still running and clear versions match; clearing wipes caches simultaneously, preventing delayed writes from restoring purged data. New snapshots do not reuse old snapshot summaries. Summary fields are serialized with v3 task archives; restoring legacy v3 archives missing new fields defaults to empty objects, while v1/v2 remain backward-compatible.

POST endpoints acknowledge and return requests immediately, with preparation and compaction executing in the background without holding admission locks or database transactions. SSE/history displays preparation, Q&A consolidation, replica compaction, and answering phases; compaction failures persist as terminal failed states for that side question. The frontend preserves error details and restores draft inputs for failed questions, avoiding floating toasts that obscure inputs; history polling no longer clears submission errors.

## Validation Records

- Replay of 19/20/21/50 pairs, retention of historical conclusions across 20 groups, and summary cache reuse across restarts: Automated tests passed.
- Long answers and code contexts, chunked request budgeting, tool pairing, snapshot immutability, and new snapshot cache invalidation: Automated tests passed.
- Summary failures, cancellations, truncation, length overruns, tool returns, clear race conditions, invocation ceilings, single overflow recovery, and non-retry on partial streams: Automated tests passed.
- Isolated PostgreSQL pagination, restarts, v1/v2/v3 archiving, summary cache and budget metadata archive restoration, legacy v3 missing fields, and 20 parent sessions sharing 4 concurrency slots: Passed.
- Go candidate service build, frontend TypeScript checks, Biome validation on modified components, and Next.js production builds in isolated directories: Passed.
- In-app browser validation using isolated UI fixtures at 1280×720 and 390×844 verifying preparation stages, summary scope badges, draft restoration upon failure, absence of floating error toasts, horizontal overflow checks, and clean console logs. Temporary fixtures removed.
- Read-only replay of existing local Worker snapshots passed updated budget checks; for example, Worker #3's 293,085-character snapshot is no longer misclassified and rejected by local character counts. No external model calls made for this item.
- Automated approval review rejected transmitting private Worker snapshots to Grok for live dialogue tests; this was not executed and is not marked as passed.
- 2026-09-11 00:37: Restarted local backend per user request, retaining original database, data directories, and credentials. Binary SHA-256 matched candidate build; backend and frontend proxy `/api/health` returned healthy.

Verification commands (using isolated test database only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

Frontend production build used an isolated copy to avoid overwriting current preview `.next` artifacts. The candidate service at `/private/tmp/artex-btw-budget-candidate` was copied to `/private/tmp/artex-btw-preview/artex` and launched; original binary backed up as `artex.before-context-budget` in the same directory.
