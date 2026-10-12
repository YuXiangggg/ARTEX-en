# Side-Question Long Conversations and Context Budget

Fixed 2026-09-11. The original implementation treated the character count of the request JSON directly as tokens, and inherited the main task's 32K output reservation, so when there was a lot of HTML, JS, and tool results it would reject legitimate side questions prematurely.

## Open-Source Implementation Review

- [Grok CLI side context](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/agent.ts#L739): extracts snippets from the most recent user and assistant text, with a character budget of about 2000 and at most 400 characters per item. It does not maintain a continuous side-question Q&A history on this path.
- [Grok CLI standalone request](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/utils/side-question.ts): independent cancellation signal; output capped at 2048 tokens when the model supports it; no tools provided.
- [Grok CLI main-session compaction](https://github.com/superagent-ai/grok-cli/blob/fb97af83f06dca873281d60168430f06c8de6324/src/agent/compaction.ts): estimates tokens, keeps recent content, merges new content into the old summary, and handles cross-turn truncation.
- [OpenCode session compaction](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/compaction.ts): full-request estimation, output/buffer reservation, recent content plus a rolling summary, and a tool-free summarization request; this implementation defaults to a recent budget of 8000 and a summary output cap of 4096 tokens.
- [OpenCode overflow recovery](https://github.com/anomalyco/opencode/blob/b3f1a96c6dd7adeb28b36dd11add1998fc84d67b/packages/core/src/session/runner/llm.ts): attempts overflow recovery only before assistant output has begun, and a recovered call no longer re-enters the same overflow-recovery path.

ARTEX borrows the ideas of a standalone output budget, recent content plus a rolling summary, and limited recovery. It keeps norma v0.3.6's structured messages and tool pairing rather than copying Grok's text extraction; it does not write OpenCode's main-session compaction events into the ARTEX main transcript.

## Request Budget and Execution

- Messages reuse norma's per-content-block UTF-8 byte estimate with a 4/3 margin; they additionally account for the system prompt, tool schemas, and message-envelope overhead. The estimate is not an exact model token count.
- Side-question output is capped at 8192 tokens by default, and never exceeds the output limit already set in the main configuration. The service environment variable `ARTEX_BTW_MAX_OUTPUT_TOKENS` can set a cap of 256-32768; it does not modify the product's default model or main-task parameters.
- The input budget is the context window minus the output cap and a safety margin; for an unknown window it uses the platform default of 200K. The safety margin is 5% of the window, with a minimum of 128 and a maximum of 8192 tokens.
- Successful Q&A pairs are loaded in batches of at most 20 in increasing ordinal. At most 20 pairs are retained verbatim, with a token budget of at most 1/4 of the input budget and no more than 16K.
- Q&A beyond the limit is merged into the rolling summary. The summary carries its historical source and context time; a historical assistant answer is not equivalent to new tool evidence, and on conflict the latest main snapshot takes priority.
- When the main context is still too long, only the old messages in the summary copy are summarized, keeping at most 8K tokens of recent content; the cut point does not split a tool call from its result. An oversized single pair goes into the summary as a whole.
- Summary input is UTF-8-safely chunked to fit the actual remaining window, with an output cap of 2048 tokens; empty summaries, truncation, tool calls, or exceeding the summary budget are never written to cache. A single side question makes at most 12 summarization calls, subject to the same 120-second timeout; reaching the limit fails explicitly rather than looping forever.
- If the model first returns a context-overflow error before any text or tool call has been output, it retries at most once after further reduction; if the estimated size does not drop, recovery stops immediately. Other model errors and partial streamed output do not trigger this recovery.
- All obtained usage, including summarization, failed attempts, and usage at cancellation time, is accumulated into the same side-question request. When the provider returns no usage, only zero can still be recorded; an estimate must not be disguised as actual usage.

## Persistence and UI

`side_question_sessions.memory` stores the old Q&A summary, the covered ordinals, and the main-context summary cached by snapshot identity. `side_question_requests.context_info` stores the preparation stage, the actual number of replayed pairs, summary usage, and the budget estimate.

The summary is saved only while the original request is still running and the cleanup version matches; clearing also clears the cache, and a late write will not restore cleaned data. A new snapshot does not reuse an old snapshot's summary. The summary fields are saved along with the v3 task archive; when restoring an old v3 that lacks fields, empty objects are filled in, and v1/v2 remain compatible.

POST accepts and returns the request first, while preparation and compaction run in the background without holding an admission lock or a database transaction. SSE/history shows the preparation, Q&A organization, copy compaction, and answering stages; a compaction failure is saved as the failing terminal state of that side-question request. The frontend keeps the error and restores the draft of this failed question, without covering the input box with a floating notification; history polling no longer clears the submission error.

## Validation Record

- 19/20/21/50-pair replay, retention of old conclusions across 20 pairs, and summary-cache reuse after restart: automated tests pass.
- Very long Chinese answers and code context, chunked request budgets, tool pairing, snapshot invariance, and new-snapshot cache invalidation: automated tests pass.
- Summary failure/cancellation/truncation/overlength/tool return, clear races, call limit, single overflow recovery, and partial-stream no-retry: automated tests pass.
- Pagination on standalone PostgreSQL, restart, v1/v2/v3 archives, summary-cache and budget-metadata archive restoration, old v3 missing new fields, and 20 parent sessions sharing four concurrent slots: pass.
- Go candidate service build, frontend TypeScript check, Biome check on modified components, and the Next.js production build in a standalone directory: pass.
- The built-in browser uses a standalone UI fixture, validating the organization stage, summary-scope hint, draft recovery after failure, absence of floating error notifications, no horizontal overflow, and no console errors at 1280x720 and 390x844. The temporary fixture has been removed.
- Read-only replay of this machine's existing Worker snapshots passes the new budget check; for example, Worker #3's 293085-character snapshot is no longer wrongly rejected by local character counting. This item involves no external model calls.
- The real-conversation test that would have sent a private Worker snapshot to Grok this time was rejected by the automated approval review and not executed, and does not count as a passing item.
- At 2026-09-11 00:37, per the user's request, the local backend was restarted, reusing the original database, data directory, and login configuration. The running files and candidate binary have matching SHA-256; `/api/health` returns healthy for both the backend and the frontend proxy.

Validation commands (using the standalone test database only):

```sh
go test -race ./sidequestion ./db ./server -run 'TestSide|TestCheckpoint|TestSnapshot|TestBuildRequest|TestService|TestMainSide|TestTaskArchive' -count=1
go build ./cmd/artex
npx tsc --noEmit
npm run build -- --webpack
```

The frontend production build uses a standalone copy to avoid overwriting the currently previewed `.next`. The candidate service is located at `/private/tmp/artex-btw-budget-candidate`, has been copied to `/private/tmp/artex-btw-preview/artex` and started; the original binary is backed up as `artex.before-context-budget` in the same directory.
