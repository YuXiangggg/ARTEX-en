# `/btw` Validation Record

Date: 2026-09-10. Branch: `codex/btw-side-question`. Baseline: `8dae851b9b622f2ff2631f332fde9719d0b16fba`.

Uses a standalone PostgreSQL test database and data directory; real model credentials are injected only into the standalone test environment, are not written into the code or this record, and the product's default model was not modified. Go 1.26.3, norma v0.3.6, Next.js 16.2.9.

The actual model conversations, returned objects, engineering assertions, and the raw Qwen review text are saved in [validation-2026-09-10.json](validation-2026-09-10.json), which contains no API credentials.

## Engineering Checks

| Scope | Result | Evidence |
| --- | --- | --- |
| Deep copy of structured messages and tool arguments | Pass | `TestCheckpointDeepCopyAndBoundaries` |
| Summary / compaction requests do not overwrite, full reply and terminal state published, half-finished reply excluded | Pass | `TestCheckpointDeepCopyAndBoundaries`, `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Actual model-pool member identity | Pass | `TestSnapshotExcludesPartialStreamAndSelectsPoolMember` |
| Tool pairing, 20-pair replay, budget trimming and overflow error | Pass | `TestBuildRequestCompactionToolPairingAndBudget` |
| Main-side concurrency, bidirectional cancellation isolation | Pass | Blocking provider, `TestMainSideConcurrencyAndIndependentCancellation` |
| No tool execution, streaming / non-streaming, usage already present on failure | Pass | `TestServiceNoToolsAndUsageOnFailure` |
| Real norma ChatAgent + local Read tool, main transcript / activity isolation | Pass | `TestSideActualChatCheckpointToolResultAndTranscriptIsolation`, streaming and non-streaming sub-cases |
| Persistence, pagination, idempotency, partial answer kept across restart | Pass | `TestSideHistoryIdempotencyPagingAndRecovery` |
| Clear vs. late-write race, parent-resource deletion, version comparison | Pass | `TestSideClearLateWritersAndDeletedParent` |
| MainAgent / Worker archiving and recovery, v1/v2/v3 | Pass | `TestSideTaskArchiveVersions` |
| Three parent interfaces, authentication, resource ownership, Worker logical deletion | Pass | `TestSideHTTPGlobalLimitTaskWorkerAndDeletion`, `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Busy main session can side-question, independent SSE reconnect / disconnect, cancel, clear | Pass | `TestSideHTTPBusyIsolationClearAndReconnect` |
| 1 per parent session / 4 global concurrency | Pass | Two `TestSideHTTP…` cases |
| Snapshot persisted before submission, resume question after restart, old session cannot forge a snapshot | Pass | `TestSideCheckpointPersistsBeforeAdmissionAndRestart` |
| Refuse to continue after the cached configuration is deleted or the model changes | Pass | `TestSideRejectsDeletedOrChangedCachedProfile` |
| Cancel before archiving and wait for the final answer and usage to persist | Pass | `TestSideTaskDrainPersistsBeforeArchive` |
| Usage recorded only once and side attribution when the streaming consumer cancels early | Pass | `TestSideUsageRecordedOnceOnConsumerCancellation` |
| Auto-recovered Worker / deadline runtime context after restart keeps publishing new snapshots | Pass | `TestSideRestoredWorkerRuntimePublishesNewCheckpoint` |
| Race check on related packages | Pass | Commands below |
| TypeScript and production build | Pass | `npx tsc --noEmit`, `npm run build` |
| Biome on new frontend modules | Pass | `biome check`, 3 new modules |

After setting `ARTEX_PG_DSN` to a separate disposable database, the automated checks can be reproduced (do not point it at a production database):

```sh
go test -race ./agent ./db ./server ./sidequestion ./llmrec ./llmpool \
  -run 'Test(Side|Checkpoint|Snapshot|BuildRequest|Service|MainSide|CaptureRun|TaskArchive|CompleteForwards|StopIntent|CancelIntent)' -count=1
cd web
npx tsc --noEmit
npx biome check src/lib/side-questions.ts src/hooks/use-side-questions.ts src/components/side-question-workspace.tsx
npm run build
```

The full Go regression is not all green: the `server` package has two pre-existing tests that fail during temp-directory cleanup, both reporting `TempDir RemoveAll … directory not empty`:

- `TestInheritedActivityDetailAndRelationDeletion`
- `TestTaskMetadataPatchReturnsRenameAndPin`

After exporting the source from the unmodified baseline above and re-running the `server` package in the same isolated environment, these two cleanup failures also reproduce. The baseline run additionally shows a target-node-count assertion failure in `TestCoreTaskLifecyclePG`; the final modified `server` regression does not have that assertion failure. Other packages pass, and this change's side-question cases and race check pass. The baseline issues were not marked as passing acceptance for this change, and no existing assertion was modified to hide a problem.

The Next.js build outputs the existing multi-lockfile / workspace-root inference warnings; the build completes and all pages generate successfully.

## Browser Checks

Using the Codex In-app Browser, connected to a standalone local Go service and the Next.js dev server. The following manual-automation operations were completed on desktop and on a 390 x 844 narrow screen, checking screenshots and browser logs:

- Typing `/btw` while normal chat is running shows the main content and the side question at the same time; the desktop sidebar works correctly.
- Consecutive follow-ups; after stopping the side question the generated part is kept; the main flow continues.
- The request continues while the panel is closed, and the completed answer is restored after reopening; after a page refresh, an empty `/btw` restores history.
- Input, buttons, history, and close work in the narrow-screen Drawer, with no horizontal overflow.
- Clear uses a confirmation dialog; after clearing, history disappears while the main transcript and snapshot are preserved.
- The task MainAgent and two Workers each ask questions and switch between them, with the Agent labels and history not crossing over.
- A blocking local-model fixture keeps the Worker running; after submitting `/btw` from the Worker's main input box and stopping the side question, the Worker still shows live running and its own pause button, and the side question saves the partial answer.
- The browser error / warning log is empty.

A controllable fixture is used to precisely validate concurrency timing, without depending on the output speed of a real model. During debugging, two Worker-runtime checks did not form an effective concurrency window (the task had already ended / the answer finished early); after fixing the fixture they were redone and passed; these initial operations are not counted as valid passes.

## Real Model Conversations

Probed `grok-4.6` first, OpenAI-compatible endpoint `http://127.0.0.1:12580/tingly/openai`. The probe returned HTTP 200 with the model name `grok-4.6` and `READY`, taking 2.82 seconds. The preferred option is available, so the Tingly `glm` or Zhipu `glm-5.3` fallback chains were not enabled; these two fallback services were not validated this time.

| Scenario | Actual result |
| --- | --- |
| Ask about assets, goals, and markers while the main session is running | Returned `redhaze.top`, read and summarized the homepage goal, `BTW-REAL-0910`; side question completed, 16.97 seconds |
| Ask for the tool basis after the main session finished reading the homepage | Correctly cited WebFetch 200, curl redirect 301 → 302 → 200, and the page title; 7.24 seconds |
| Side question asking Bash to create a test file | Refused to execute, the target file was not created; 7.74 seconds |
| A side question after completion does not change the main context | The main transcript SHA-256 and the main activity log stay consistent; the side question's tool-execution count is 0 |
| Follow-up after actually stopping / restarting the Go service | Kept the previous 3 side-question history items and answered assets, markers, and title directly from the persisted snapshot, without re-running the main Agent |
| New session using the Grok non-streaming configuration | Correctly answered assets and `ATOMIC-0910`; returned and saved usage: input 11734, output 138, cache_read 11520 |

For the asset case, the main session used WebFetch and Bash/curl to read the public homepage; the landing page is `https://id.redhaze.top/home`, with the title "RedHaze Group · Global Conglomerate Portal". Bash staged the response in a local test file; no write was performed to the remote end. This fact is verified separately from "the side question executed no tools".

Main transcript checksum: `e7e61f135a4a120954b539f357e8c4205d7d5cd7460dcaf3dc0fd066463e1d00`.

**Usage limitation:** Tingly's Grok streaming response returned no usage. A separate direct request with `stream_options.include_usage=true` was used to verify: HTTP 200, 12 data frames, 0 usage frames. So the 0 in the streaming test means the endpoint did not provide usage, and must not be interpreted as no billing. Non-streaming usage, as well as the fixture's failure / cancellation usage, are all saved correctly.

## Qwen Review

The review model `qwen-flash`, OpenAI-compatible endpoint `https://dashscope.aliyuncs.com/compatible-mode/v1`, HTTP 200. It was given the first three real side-question conversations, the main session's tool basis, and the engineering assertions; it returned `verdict: accept`, `concerns: []`, considering the answers consistent with the asset, marker, and page-read evidence, and the side question's tool refusal compliant with constraints. Review usage: prompt 6625, completion 312, total 6937.

This Qwen review did not cover the later-added service restart and non-streaming tests. Qwen's generalization of "no writes" is too broad: the main session's curl did create a local temporary response file, as explicitly recorded above. Concurrency, zero tool execution, and transcript isolation are judged by the engineering assertions; the model review only assists in evaluating answer quality.
