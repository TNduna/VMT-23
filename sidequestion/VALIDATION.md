# `/btw` Validation Record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

Conducted with isolated PostgreSQL test database and data directories; live model credentials injected into isolated test environments only, never committed to source or this document, and default product models were not modified. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

Actual model dialogues, returned payloads, engineering assertions, and raw Qwen review texts are preserved in [validation-2026-09-10.json](validation-2026-09-10.json), containing no API credentials.

## Engineering Checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Structured messages, tool parameters deep copy | Passed | `TestCheckpointDeepCopyAndBoundaries` |
| Summarization / compaction requests non-overwriting, complete replies and terminal states published, partial replies excluded | Passed | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Actual model pool member identity | Passed | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-group replay, budget trimming and overrun errors | Passed | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main and side concurrency, bidirectional cancellation isolation | Passed | Blocking Provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| Zero tool execution, streaming / non-streaming, recorded usage on failure | Passed | `TestServiceNoToolsAndUsageOnFailure` |
| Genuine norma ChatAgent + local Read tool, main transcript / activity isolation | Passed | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming subcases |
| Persistence, pagination, idempotency, restart preserves partial answers | Passed | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear vs delayed writer race, parent resource deletion, version comparison | Passed | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker archive and restoration, v1/v2/v3 | Passed | `TestSideTaskArchiveVersions` |
| Three parent endpoints, auth, resource tenancy, Worker soft deletion | Passed | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Busy main session allows side questions, independent SSE reconnect / disconnect, cancel, clear | Passed | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 1 per parent session / 4 global concurrency limit | Passed | Two `TestSideHTTP…` test cases |
| Snapshot persisted before admission, restart continuation, legacy sessions cannot forge snapshots | Passed | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Cached profiles deleted or model changed rejects continuation | Passed | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Drain cancels and awaits final answer and usage persistence prior to archiving | Passed | `TestSideTaskDrainPersistsBeforeArchive` |
| Streaming consumer early cancellation records usage once with side attribution | Passed | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Restart auto-recovered Worker / deadline execution context continues publishing snapshots | Passed | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Race detector across related packages | Passed | Commands below |
| TypeScript and production builds | Passed | `npx tsc --noEmit`, `npm run build` |
| Biome check on modified frontend modules | Passed | `biome check`, 3 modified modules |

Automated checks can be reproduced after configuring `VMT-23_PG_DSN` in an isolated disposable database (do not point to production):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

Full Go regression suite is not completely green: two existing tests in package `server` fail during temporary directory cleanup, both reporting `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

Exporting source code from the unmodified baseline above and rerunning package `server` in the same isolated environment reproduced both cleanup failures. The baseline run also produced a target node count assertion failure in `TestCoreTaskLifecyclePG`; the final modified `server` regression did not have this assertion failure. Other packages passed, with side-question test cases and race checks fully green. Baseline issues were not marked as passed deliverables, nor were existing assertions modified to mask underlying behavior.

Next.js build produced existing multi-lockfile / workspace root inference warnings; build finished with all pages rendered successfully.

## Browser Checks

Conducted via Codex In-app Browser connected to isolated local Go service and Next.js dev server. Desktop and 390 × 844 narrow screens completed manual automated operations, verifying screenshots and browser logs:

- Entering `/btw` during normal chat runs displays main content and side questions concurrently; desktop sidebar functioning properly.
- Consecutive follow-up queries; stopping side questions preserves generated content; main execution continues.
- Closing panel allows requests to continue, reopening restores completed answers; refreshing page with empty `/btw` restores history.
- Narrow screen Drawer inputs, buttons, history, and close actions functioning without horizontal overflow.
- Clear action uses confirmation dialog; clearing purges history while preserving main transcript and snapshots.
- Submitting `/btw` across task MainAgent and two Workers separately; agent labels and histories do not bleed across contexts.
- Blocking local model fixtures held Worker active; submitting `/btw` from Worker input and stopping side question left Worker displaying live progress and pause button, with side question persisting partial response.
- Browser error and warning logs clean.

Controllable fixtures were used to verify concurrency timing independent of live model output speed. During debugging, two Worker runtime checks did not form valid concurrency windows (tasks ended or answers completed prematurely); retested and passed after correcting fixtures. Initial runs were not counted as passing validations.

## Live Model Dialogues

Probed `grok-4.6` first via OpenAI-compatible endpoint `http://127.0.0.1:12580/tingly/openai`. Probe returned HTTP 200 with model name `grok-4.6` and `READY` in 2.82 seconds. Primary model was available, so Tingly `glm` and Zhipu `glm-5.3` fallbacks were not activated; fallback chains were not tested in this cycle.

| Scenario | Actual Result |
| --- | --- |
| Querying assets, targets, tags while main session is active | Returned `redhaze.top`, home page read, summary targets, and `BTW-REAL-0910`; side question completed in 16.97s |
| Querying tool evidence after main session completed home page read | Correctly cited WebFetch 200, curl redirects 301 → 302 → 200, and page title; 7.24s |
| Side question prompting Bash to create test files | Execution rejected; target file was not created; 7.74s |
| Completed side question leaves main context untouched | Main transcript SHA-256 matches main activity record; side tool execution count is 0 |
| Follow-up queries after stopping / restarting Go service | Preserved prior 3 side question entries, answered assets, tags, and title directly from persisted snapshot without rerunning main Agent |
| New session using Grok non-streaming configuration | Correctly answered assets and `ATOMIC-0910`; returned and saved usage: input 11734, output 138, cache_read 11520 |

The main session for the asset scenario used WebFetch and Bash/curl to read the public landing page `https://id.redhaze.top/home`, titled "RedHaze Group · Global Comprehensive Enterprise Portal". Bash cached responses in a local test file without issuing remote writes. This fact was verified independently from "side questions executed no tools".

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage Constraints:** Tingly Grok streaming responses did not return usage metrics. Independent verification with `stream_options.include_usage=true` returned HTTP 200, 12 data frames, and 0 usage frames. Therefore, 0 in streaming tests denotes the endpoint omitted usage, not that the call was unbilled. Non-streaming usage and fixture failure/cancellation metrics were accurately persisted.

## Qwen Review

Review model `qwen-flash`, OpenAI-compatible endpoint `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. Provided with initial three live side-question dialogues, main session tool evidence, and engineering assertions; returned `verdict: accept`, `concerns: []`, noting answers aligned with assets, tags, and page fetch evidence, and tool execution rejections conformed to boundaries. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review cycle did not cover subsequent service restart and non-streaming tests. Qwen's characterization of "zero writes" was overly broad: the main session curl call did create local temporary response files, as documented above. Concurrency, zero tool execution, and transcript isolation were validated via engineering assertions; model reviews served only to assist in evaluating response quality.
