# Changelog

Notable changes to this project are recorded in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

## [0.3.15] - 2026-10-07

### Interception

#### Added

- **New built-in interception rule "delete-style API paths"**: previously the built-in HTTP destructive rule only recognized the DELETE **method**, yet most applications' delete endpoints can be triggered via GET/POST, and calls like `curl 'http://t/api/user/delete?id=1'` would actually delete target data. A `deny` rule now covers `/delete /del /remove /unlink /erase /destroy` (the verb must be followed by a separator, so `/delivery`, `/details`, `/delta` are not falsely intercepted). Existing instances get it after upgrading too, and it can be disabled or deleted under "System -> Command Interception".
- **Token consumption of LLM fallback approval can be viewed separately**: approval calls previously had no dedicated metering attribution, so the cost could only be guessed. They are now metered separately, and a usage card is added below the "LLM fallback approval" switch in "System -> Command Interception" - cumulative stats for call count, input, output, cache read and cache write, plus a mini bar chart of daily consumption over the last 30 days.

### Vulnerability Notifications

#### Added

- **Findings can be pushed to IM**: a new "System -> Notifications" page can push scan findings to six channel types: **DingTalk, Feishu, WeCom, generic Webhook, Telegram, and email**. Any number of instances can be configured per type, each with its own enable/disable, rate limit and filter rules.
- **Two push timing modes: real-time and digest**: real-time mode sends each match individually; digest mode merges them into one message per global period (default 30 minutes), starting with the count of new findings and the severity distribution. For "critical in real time, the rest as digest", create two channels configured separately.
- **Filter rules support four dimensions**: minimum severity, restriction to tasks / asset scope, vulnerability type keyword include and exclude (exclude takes priority), and whether to receive triage status changes (off by default). Messages carry a "View details" button whose target is set by the global "Backlink URL" config; leave it empty for no button.
- **Delivery history and manual resend**: lists each delivery's status, attempt count, failure reason and channel, filterable by channel and status; failed items can be resent in one click (resending resets the retry count).
- **Masked echo of channel credentials**: Webhook URLs, signing secrets, Bot Tokens, SMTP passwords, etc. are only echoed back as masked values with a trailing hint; submitting the value unchanged means "do not modify", and clearing the input deletes the field.

#### Fixed

> This section is the conclusion of a security audit round after the initial version of the feature was complete; each item was first reproduced, then fixed, then covered by a regression test.

- **Fixed mask bypass via "change target URL, keep credentials" (critical)**: config merging always kept the stored value for "keys not mentioned", so by changing only the URL and staying silent on credentials, one could make the server send the real stored credentials to an arbitrary address, completely silently. Now, once the target URL changes, every credential field must be explicitly stated - either a new value, or explicitly empty to indicate it is no longer needed.
- **Fixed zero escaping of untrusted content in markdown-based channels**: finding titles and summaries come from LLM output and asset URLs come from scan results; a crafted title would render as a clickable external link in DingTalk / WeCom / Feishu, and image syntax would be fetched by the client on render, leaking the reader's IP. Content is now uniformly collapsed to a single line + markdown metacharacter-escaped, and escaping happens at each channel's render exit rather than in the shared title function.
- **Fixed the SSRF surface of delivery addresses**: previously loopback, internal and cloud metadata addresses were all deliverable, and the first 200 bytes of failure response bodies went into `last_error` and were echoed by the delivery history, forming a semi-blind read primitive. Defenses are now applied at the **dial stage**, covering DNS rebinding, and cross-host redirects are rejected; loopback and link-local addresses require explicitly setting `ARTEX_NOTIFY_ALLOW_LOCAL=1`, while RFC1918 private networks are still allowed (self-hosted internal SMTP relays are common).
- **Fixed push credentials leaking via error messages**: DingTalk's `access_token`, WeCom's `key` and Telegram's bot token are all in the URL, and on delivery failure the full URL flowed into four places: `last_error`, delivery history, server logs and frontend toasts. They are now uniformly redacted, keeping only `scheme://host` and the underlying cause.
- **Fixed silent loss caused by marking a whole batch delivered after a digest message was truncated**: truncated findings were neither in the message nor in the failure list, yet delivery history showed success. Packing is now done by **whole entry**, only the entries that fit into this message are marked, and the rest go back to the queue to continue in the next message without consuming retry attempts; the message header also states how many more will continue in the next message.
- **Fixed a typo in the minimum severity threshold silently disabling the filter**: with a typo like `hgih` for `min_severity`, the unknown severity's ordinal was 0 and the check degenerated to always-true; users thought they only received high-severity findings while actually all findings were flooding the group. Values are now validated on the write path, listing the allowed values.
- **Fixed `rate_per_min = 0` (unlimited) being unreachable**: the DB write layer silently changed an explicit 0 to the default, so operators thought they had lifted the rate limit while it was actually still throttled. The default is now filled in at the API layer when the field is omitted.
- **Fixed digest channels completely bypassing the token bucket**: `rate_per_min` previously had no effect in digest mode. The number of claimed entries is now bounded by both this round's quota and an in-memory upper bound.
- **Fixed failure handling judging by the batch's max attempt count, making old deliveries drag down new ones**: an old delivery already retried twice would drag brand-new deliveries in the same batch into failed, and new findings were lost permanently without a single retry. Decisions are now made per entry.
- **Fixed deliveries in a digest batch whose snapshot could not be parsed being silently marked successful**: such entries were skipped at render time yet counted as successful along with the whole batch. They are now explicitly failed with a reason.
- **Fixed the per-round delivery count of a single channel possibly exceeding the lease duration**: in multi-instance deployments, rows with expired leases would be reclaimed, sent twice, and have their attempt count incremented twice. The per-round cap is now derived from "lease / per-call timeout", with assertions pinning down the relationship among these constants.
- **Fixed Telegram truncation possibly cutting HTML entities**: truncation only avoided half tags, not entity fragments like `&amp`, which could make the parser reject the **entire** message. Both are now avoided.
- **Fixed the email channel treating transient SMTP failures as permanent**: 4xx codes like greylisting `450` are temporary rejections that should be retried later, but were previously all treated as fatal, so a server with greylisting enabled made every push fail after the first attempt. Now distinguished by the first digit of the reply code: 4xx is retryable, 5xx is permanent failure.
- **Fixed submitting nested structures actually writing the mask literal into the DB**: object fields like `webhook.headers` can only be masked or submitted as a whole; stuffing the mask sentinel inside the object would be stored as a real value, causing authentication to silently fail. Such submissions are now explicitly rejected.

#### Design Notes

- **The transaction that writes findings does only one blind INSERT**: `notification_events` and the finding are written in the same transaction, so commit guarantees both are atomically consistent. This INSERT deliberately does not read the channel table or run user filter rules, and is wrapped in a `SAVEPOINT` - otherwise a misconfigured filter condition could abort the transaction and keep a critical finding from being stored.
- **Delivery uses lease-based claiming rather than long transactions**: after claiming via `FOR UPDATE SKIP LOCKED`, the row is set to `sending` and `next_attempt_at` is pushed into the future as a lease, so no DB lock is held during delivery; rows left behind by a process crash are reclaimed after the lease expires, self-healing without infinite retries.
- **Rate limiting does not consume the retry budget**: this round's quota is computed from the token bucket first, then that many entries are claimed. The reverse order would charge an attempt to deliveries blocked by the rate limit, and the three-attempt budget would be exhausted by pure waiting, ending in failure.

### Test Infrastructure

#### Fixed

- **Fixed permanent asset residue caused by the cleanup order of the company ICP attribution test**: `defer d.Close()` runs first when the function returns and `t.Cleanup` runs after it, so all cleanup statements ran on a closed connection, and since errors were discarded, test assets and companies remained in the DB permanently, also causing other tests asserting on asset counts to fail mysteriously. Closing the connection now also goes through `t.Cleanup`, registered first, and cleanup failures are surfaced.

### Traffic

#### Added

- **Traffic list supports content search, path / status code / length filters and column sorting**: the filter row supports filtering by **response content** (via the full-text index), **path**, **status code** (exact value or buckets like `2xx`), and **response length range**; the table header can sort by time, status code and response length, with the sort preference remembered locally. Two new indexes are added and take effect automatically after upgrading existing instances; the Agent tool's query contract is unchanged.
- **Traffic list adds "Clear all"**: deletes all traffic records at once (ignoring the current filters), cleans up historical host directories no longer recorded by the index, does a full compaction after clearing, and reports how much space was actually freed. Traffic evidence bound to findings is stored in a separate evidence store and is not affected.

#### Fixed

- **Fixed disk space not being released after deleting traffic**: the index DB was created without `auto_vacuum`, so the file never shrank; and the full-text index is `contentless_delete`, where `DELETE` only writes tombstones without reclaiming the original postings - deleting traffic actually made the index larger (measured: capturing 6MB of bodies -> 16MB index, still 16MB after deleting everything). New index DBs now enable `auto_vacuum=incremental`, and after each delete a background job performs full-text index merging and incremental vacuum in chunks, gradually returning space to the system (the same scenario drops back to 104KB); the write lock is released between chunks, so traffic recording is not blocked.

> Upgrade note: `auto_vacuum` can only be set at DB creation, so **the index DB of existing instances remains in the old mode**, and incremental vacuum is a no-op on it (a hint line is printed at startup). After upgrading, tombstone growth on such DBs has stopped, and the already occupied space can be reclaimed once with "Clear all" - that step converts the DB to incremental vacuum mode, after which routine deletes take effect on their own.

### LLM

#### Fixed

- **Fixed custom session headers not taking effect on the "one-shot LLM call" path**: goal decomposition (round 0) and cold node compression are one-shot calls without a transcript store attached, so the context had no session id and the gateway could not read the header - for endpoints like opencode zen that return 400 when the header is missing, this showed up as "round 0 fails, but subsequent planner rounds work fine". These two paths now explicitly attach a session id stable per exploration, which also lets token usage be correctly attributed to the corresponding exploration (previously not recorded at all).

### Tools

#### Fixed

- **Fixed output truncation not taking effect for MCP and custom tools, with large results flooding the context**: MCP tool returns previously were not truncated at all, and HTTP custom tools, because an empty tool context was passed in, always used a hardcoded 6000 characters instead of the session's configured limit. Both paths now go through `actool.Capture`: truncated at the session's `MaxOutputChars` (default 30000), and when `ToolOutputDir` is configured the full output is spilled to disk, leaving only the leading snippet and a file pointer in the message.
- **Upgraded norma to v0.4.3**: the harness layer uniformly runs `CaptureOnce`, so no tool (including ones added in the future) will overflow the context by missing truncation.

### Deployment

#### Fixed

- **Fixed the activity stream failing to connect behind a reverse proxy**: the SSE URL was previously unconditionally built as `${hostname}:8787`, so in deployments reverse-proxied to 443 the page loaded from `https://domain/` while SSE was pointed at port 8787, which is not exposed publicly, showing up as the activity stream repeatedly reconnecting. Now distinguished by `NODE_ENV`: production defaults to same-origin, and only `next dev` keeps `:8787` to bypass Next.js's buffering of SSE; the `NEXT_PUBLIC_SSE_BASE` override is retained. The README adds reverse proxy deployment notes.

### Findings

#### Fixed

- **Fixed the asset list in the "By asset" view having no scrollbar when it overflows**: the outer card only had a `max-height` without a definite height, so the scroll viewport's percentage height could not be resolved; with many assets the list either burst out of the card or was cut off and unscrollable. The height cap is now placed directly on the asset tree's native scroll container, adapting to the window height.

### Database

#### Fixed

- **Set an upper bound on the connection pool**: `database/sql` by default does not limit the number of connections the application itself can open; when there is no idle connection in the pool it unconditionally creates a new one, going all the way up to PostgreSQL's `max_connections` (default 100) before being rejected. It is now capped at 32, and excess queries wait for an idle connection, so under the same load things get slower rather than erroring; if `max_connections` has been lowered, lower this accordingly.
- **Improved connection reuse**: the default `MaxIdleConns` is 2, so connections beyond 2 concurrent ones were closed after use, and the next request had to redo the TCP and PostgreSQL authentication handshake. The idle cap is now raised to match the total cap, with a 30-minute connection lifetime and 5-minute idle eviction added, so the pool can naturally replace long-lived bad connections.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.14] - 2026-09-24

### Task List

#### Added

- **Task list adds a "Findings" column**: shows counts for Critical / High / Medium / Low by severity tier, with non-zero tiers colored by severity, so each task's finding volume and distribution is visible at a glance.

#### Changed

- **Narrowed the "Description / Target" columns**: overly long content is shown with ellipsis and the full text is visible on hover, reducing the horizontal space long text takes from the list.

### Exploration Graph and Planning Situation Overview

#### Changed

- **Slimmed down the planning situation overview (graph_overview) and set count caps for each list**: recent facts, finished intents, pending intents, cold zone summaries and confirmed finding details all switch to "latest window + count fallback", with anything not shown queryable on demand; this significantly reduces per-round LLM context size and avoids context bloat on long tasks (the cold zone of the related task overview is throttled as well).

#### Fixed

- **Eliminated reverse duplicate edges between digest and finding in the exploration graph after folding cold nodes**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.13] - 2026-09-19

### Asset Interception

#### Added

- **New global asset interception rules (blocklist)**: supports entering exact and fuzzy-match domains / IPs / URLs as well as CIDR ranges, with create/read/update/delete and enable/disable for rules; the management page is under "System -> Asset Interception". By default it ships with built-in fuzzy blocking of government (`.gov` / `.gov.cn`) and education (`.edu` / `.edu.cn`) sites.
- **Asset interception wired into the execution chain**: before dispatching an intent (`add_intent`) and inserting assets (`insert_assets`), the Agent first checks the target assets against interception - intents that hit interception are not dispatched, assets that hit interception are not inserted, and the asset info and interception reason are returned to the Agent.
- **New task-level intercept / allow (allowlist) rules**: independent of the global rules and effective only for this task, evaluated in "intercept first, then allow" order - a hit on intercept means forbidden; if no intercept hit but the task has allow rules configured and none match, then "testing not allowed"; with no allow rules configured, the allowlist is not enabled. They can be entered when creating a task, or created/edited/deleted and enabled/disabled in the task detail "Overview".
- **Task templates support a preset category and task-level intercept/allow rules**: a template can save a task category and a set of task-level rules, which are carried into the new task form when the template is applied.

### Operation Review

#### Fixed

- **Tightened the LLM judge's output protocol to reduce false approvals caused by truncation**: lowered the verdict comment limit from 500 Chinese characters to 120 and enforced "output JSON only, no preamble or code fences", preventing verdicts truncated by `MaxTokens` from failing to parse and then being approved under the model-failure policy (fail-open).

### Task Archiving

#### Fixed

- **Archiving skips symlinks instead of failing the whole package**: the archive format end-to-end only supports regular files and directories, and previously any symlink in the working directory caused the entire task archive to fail; the symlink is now skipped and logged, and the remaining files are archived normally (links are not followed and nothing escapes the directory tree).

### Account and Compliance

#### Added

- **New "Terms of Use and Disclaimer" dialog before login**: you must tick the agreement checkbox before you can log in.

### License and Dependencies

#### Changed

- **The project adopts the AGPL-3.0 open source license**, and the README's license and disclaimer sections are improved.
- **Upgraded norma to v0.4.1**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.12] - 2026-09-17

### Exploration Feed

#### Added

- **Task detail page adds an "Exploration Feed"** (#144): presents the task's exploration nodes (origin/goal/intent/fact/finding/hint/digest) as a timeline stream, with type filters, keyword search, ascending/descending order, pagination and auto-refresh; when on "the first page, newest first" it acts as a live view refreshed every round, and when you leave that position it only accumulates an unread count without disturbing what you are reading, with "N new items · Back to latest" to return in one click. Grouped by day, so even when paging through long tasks you can tell "which day this happened".
- **Each feed row shows the node id** (#145), making it easy to cross-reference the exploration graph and locate specific nodes.
- **Expanding a feed node shows upstream/downstream and anchored assets** (#147): expanding an item shows the node's upstream (derived from) / downstream (produced) relations, and hovering a related entry pops up that node's card (type/status/source/time/summary/payload snippet); it also lists the assets anchored by the node (type tag + recognizable text). The related data is delivered with the feed page, so expanding sends no extra requests.
- **Feed search supports filtering by node id** (#150): beyond "content / source", the search box adds node id matching; enter a plain number or the "#41" form shown in the UI to pinpoint the corresponding node.

### Intent Management

#### Added

- **Intent deletion supports "soft delete / hard delete" modes** (#149): pending, running and paused intents can all be deleted, and deletion requires a reason; the mode is chosen in the confirmation dialog.
  - **Soft delete (default)**: the intent is set to "Deleted" with the reason recorded in a separate field, keeping the intent node and all its outputs and lineage.
  - **Hard delete**: physically removes the intent, along with exclusive descendant nodes "supported only by it" (cascading along output / intent chains down to the leaves), to avoid leaving orphaned data; the deleted intent's token metering is retained, archived by its original date; shared nodes (still referenced by other intents), goals and the task root fact are always kept, and the hard delete dialog shows the estimated number of nodes affected by the cascade.

  Both modes notify the planner that "this intent was deleted by the user + reason" and it replans accordingly.

### Operation Review

#### Added

- **Approval records can be filtered by status and verdict source** (#139): filter approval records by approval status and verdict source to quickly locate target records.

#### Fixed

- **Pending approval requests load independently of history pagination** (#133): pending requests are no longer affected by history list pagination and remain fully visible while browsing history.

### Agent

#### Changed

- **Worker role description changed to a generic cybersecurity platform wording** (#138).

#### Fixed

- **Fixed cold node compression never triggering**: wired the Compactor into the per-task planner; cold node compression (cold-digest) previously never ran because it was not wired in, and is now restored.

### Chat

#### Added

- **Chat supports @-references to multiple record types with scroll pagination** (#135): records of several types can be @-referenced in chat, and reference candidates load with scroll pagination.

#### Fixed

- **Long message bubbles are constrained within the conversation panel** (#137): overly long message bubbles no longer overflow the conversation area.
- **Fixed "missing upload file" when uploading before a new conversation was created**: the Composer's file picker now snapshots the `FileList` into an array before clearing the input and invoking the callback; previously, in draft state the conversation had to be created asynchronously first, and by the time execution resumed the `FileList` live-bound to the input had been cleared, so the upload lacked the file field and the backend returned 400.

### Traffic

#### Fixed

- **The traffic recording proxy listens only on 127.0.0.1 by default** (#129, #130): prevents the open proxy from being exposed on other network interfaces under the default config.

### Web

#### Fixed

- **Fixed the static export task list page losing the global header**.
- **Added the missing related-traffic mock for the demo finding detail page, fixing a blank screen**.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)
- [@dingpotian](https://github.com/dingpotian)

## [0.3.11] - 2026-09-15

### Operation Review

#### Added

- **Approval records support source navigation** (#125): clicking an approval record's "Source" jumps back to the tool execution that triggered that approval - opening the original full conversation, auto-paginating to the target position, expanding the command and result in place and highlighting it centered in the scroll area, with surrounding messages kept readable; auto-correction stops when the user scrolls manually. Across regular chats, task Workers, planners and main Agent segmented sessions, location is precise via the persisted `tool_use_id` and task mapping (with a new scoped call ID index); when missing, duplicated or ambiguous, an explicit message is shown instead of jumping to another execution. A clear message is also shown when the conversation was deleted or the task was archived / the record is missing.
- **Approval records support pagination** (#115).

#### Changed

- **Slimmed down LLM review input** (#124, #125): the LLM review input (the LLM judge after no rule matches) is narrowed to a versioned JSON of "the current complete tool call + explicitly selected brief background + the local working directory". Background is taken only from real user messages (the current user message of chat / the task main Agent); Workers no longer attach an intent summary and also clear background inherited from the parent Agent; planners and auto-triggered sessions do not fabricate user messages. Task description, goal, operation constraints, global exploration situation, historical calls and the Worker's full intent are no longer attached - these are still used as before for Agent execution and independent session auditing, they just don't go into operation review. All verdicts must output JSON `decision`/`comment`, and the comment must include "the actual operation, the consequences if it succeeds, the rule matched"; instructions in arguments or background cannot change the review policy. The input snapshot actually sent to the model and its fingerprint are saved; old snapshots are kept and labeled with their version, and old inputs are not reconstructed from current data.

#### Fixed

- **LLM verdicts wrapped in code fences no longer silently pass** (#126): if the verdict JSON returned by the model was wrapped in a ``` code fence, parsing previously failed and it was silently approved via the model-failure policy; code fences are now stripped before parsing, and only if parsing still fails is the configured failure policy applied.

### Agent

#### Added

- **New experimental noa context compression** (norma upgraded to v0.4.0): an experimental feature that can be enabled in system settings, off by default. When enabled, noa (model-driven context compression) takes over context compression for the four Agent types - main Agent, planner, Worker and chat - replacing the built-in compression; the compressed originals are persisted and archived centrally under `<workDir>/noa/<session ID>/` (not scattered across task directories), in globally unique per-session-ID directories. On integration failure it automatically falls back to built-in compression without interrupting real tasks; the switch is read once per run, so toggling only affects runs started afterwards.

#### Fixed

- **Exploration graph tools reject instead of nil-crashing without task context**: calling exploration graph tools outside a task context returns a clear error instead of crashing on a nil store dereference.

### MCP

#### Added

- **Support for legacy SSE MCP servers** (#117): compatible with MCP servers that only provide the legacy SSE transport.

### Traffic

#### Fixed

- **Traffic search matches recorded hosts port-aware** (#114): `traffic_search` host matching includes the port, preventing records of the same host on different ports from mixing.
- **An error migrating the `traffic_search` description no longer interrupts subsequent reporter migrations**: a single migration failure is isolated and does not affect subsequent migrations.

### Web

#### Changed

- **Added dividers to task retest finding options** (#122): separators between retest finding options for visual clarity.

#### Fixed

- **Fixed the demo task detail page crashing entirely**: in mock mode the task detail "Conversations" tab fetches `GET /api/tasks/<id>/side-questions` (side question history), but the mock handler had no such route, and the read fallback returned `[]` by the rule "a path ending in s is a collection", so `data.items` was `undefined` and the side question hook's `merge()` threw `TypeError: t is not iterable` when iterating it; that exception occurred inside the `setItems` updater and was deferred by React to rethrow in the render phase, where the caller's `catch` could not catch it, so the whole page was taken over by the error boundary showing "This page couldn't load". The mock handler now explicitly returns an empty side question history, and `sideAPI.history` also normalizes the return value (`items` that is not an array becomes `[]`) as defense in depth.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.10] - 2026-09-13

### Network

#### Added

- **Web search adds DeepSeek official as a source**: directly reuses the currently active LLM config. It differs in nature from the other three sources - DeepSeek has no directly callable search API; search only exists inside its Anthropic-compatible API (the `web_search_20250305` server tool) and is executed by DeepSeek's server. Therefore it **only supports the official DeepSeek endpoint + anthropic protocol** (OpenAI protocol endpoints reject server tools outright), and each search **costs one extra model call**, requests **do not go through the search egress proxy**, are **not recorded in traffic traces**, and results contain **only titles and links** (no snippets; use WebFetch when the body is needed). The settings page explains these limitations but **does not enforce validation**; whether they are met is for the user to confirm, and the "Test search" button can run it once to verify.

### Agent

#### Added

- **Workers gain cross-work lookback**: new `search_all_worker_traces` (no need to know the intent_id first; globally searches matching steps across the execution of all works in this task by keyword) and `get_worker_trace` (after locking onto a work, lists its step stream, searches in place by keyword, and fetches full content by step_id), for reusing observations other works saw but didn't write into facts, avoiding duplicate effort.
- **`node_detail` made available to workers**: together with the lookback tools above, once a worker has an intent_id / node id it can directly query the node's full details.
- **Planning rounds triggered by `add_hint` are explicitly announced**: previously a new hint was only folded into the situation overview for the planner to discover on its own; now an `add_hint` records a trigger for the planner saying "a human added N strategic hints: ..." (a batch counts as one, not one per hint), and the planner is explicitly told "this round was triggered by new hints" and sees the hint content directly.

#### Changed

- **Global situation hints switched to a "loosened" tone**: encourage divergent exploration and prompt reporting of cross-intent leads, rather than converging too early.
- **Streamlined worker boundary wording**: an initial block doesn't mean the avenue is exhausted; focus on "exhaust the bypass techniques within this intent before concluding".
- `insert_assets` removes the per-asset `related` parameter: its only role was deciding whether to add the asset to this task's scope, yet the value was not persisted (it was voided when the same asset was registered again, and the UI didn't show which ones were judged unrelated), so in practice it just made the model make one more judgment that couldn't be kept.
- **Test scope (`task_scope`) is no longer affected by the asset coverage switch**: `insert_assets`' automatic scope entry (`source='auto'`) runs regardless of whether coverage is enabled, and `add_task_scope` is always provided to the planner / in-task main Agent / goal decomposition Agent. Scope is the task's authorization boundary and the filter basis for asset queries; the coverage switch only decides whether to use it as the denominator for metrics, not whether to accumulate scope itself. Previously, turning off coverage disabled both the `auto` and `agent` write paths, leaving `task_scope` with only rows added manually in the UI. `list_untested_assets` is still hidden when coverage is off (it is purely a coverage view).

#### Fixed

- **Fixed assets leaking between tasks** (#59): `list_assets` previously hardcoded the task id to 0 and queried the entire shared asset store raw, and the model had no way to write a scope filter, so it treated other tasks' assets (especially IPs) as this task's targets, steering testing in the wrong direction. It now only returns assets within the test scope (`task_scope`) of [this task and directly related tasks], matched by **ownership** rather than literal value: if the scope contains a root domain, all subdomains/services/endpoints under it can be found; if it contains a network range, hosts and services within it can be found; fetching out-of-scope assets by id likewise returns nothing. Non-task contexts (Auto/pentest) have no scope to rely on and still fall back to the whole store. Also fixed the ownership blind spot where direct IP hosts (e.g. `http://1.2.3.4/api`) could not be matched by a network range scope. The UI's "Test assets" view (filtered by task producer) is unaffected.
- **Fixed worker cross-work lookback tools being wrongly removed**: after `search_all_worker_traces` / `get_worker_trace` (and `node_detail`) were added to the worker default toolset, an old migration that "narrows the worker tool surface" unbound them again at startup, so workers didn't actually get these tools. They have been removed from that unbind list, and for DBs that already ran the old migration they are rebound to workers once.
- **Fixed occasional DB write failures of `/btw` side questions**: NUL (`\u0000`) escapes, which JSONB does not support, are stripped before persisting side_question checkpoints, avoiding errors when writing content containing that character.

### Triggers

#### Fixed

- **Merged trigger sessions deduplicate task description/goal per task**: when multiple tasks are merged into one trigger, the same task's description/goal is no longer repeatedly concatenated into the message, avoiding long goals piling up and blowing up the context.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.9] - 2026-09-11

### Agent

#### Added

- The in-task main Agent supports **multiple sessions**: you can create, switch, and independently reset context, and every session is interactive.
- Supports persisted `/btw` **side questions**: ask follow-ups without interrupting the main line; Q&A is persisted and survives restarts.
- `spawn_task` adds `source_task_ids`, letting a new task **inherit read-only** the assets and findings of source tasks.

#### Changed

- Disabled workers' cross-engagement memory and narrowed their default tool surface: reading context and cross-work review are planning responsibilities; workers only handle executing and writing back a single intent.
- Streamlined the default worker prompt; `get_worker_output` removes the `terminated` and `worker_name` fields; streamlined the descriptions of `insert_assets` / `list_assets`.

#### Fixed

- Fixed works being cut short by **idle turns**: the model sometimes spends a whole turn outputting only thinking, with neither text nor tool calls, and the harness sees this as a natural end (`end_turn` with no `tool_use`), wrapping up directly with `completed` + an empty summary - an intent not yet finished is broken off halfway, showing up as "the task ended normally but without any text summary". None of the five LLM retry layers can reach it: it is not an error; the same-provider safe-window retry is entered on "stream failure", the circuit breaker treats `err == nil` as success, and intent rerun only recognizes `model_error`; the SDK's empty response retry can't reach it either, since that layer judges emptiness by "whether any event was yielded", and thinking deltas are themselves events. Now the work's Stop hook recognizes such turns and injects a continue instruction, letting the model carry on to the next step with the thinking it has already produced - "resend as-is" was deliberately not chosen, because such idling is usually determined by the shape of the prompt and context, a stable behavior rather than a random glitch, and resending would just make the model think it over again.
- The count reuses the **empty response retry count** from the LLM page's "Retry and backoff" (both address "the model finished but produced nothing substantive", just with different criteria and means), default 2, and `-1` turns it off, reverting to the old "idle means wrap up" behavior. It is a **total** cap per intent rather than a consecutive count: the harness itself already limits consecutive idle turns to one nudge, and the quota only refreshes when a real tool turn occurs, so this number guards against loops like "tool -> idle -> nudge -> tool -> idle" exhausting the intent's budget. When triggered, the log records `[work <worker> · #<intent>] idle turn... injecting continue instruction (n/N)`. See `docs/LLM-retry-design.md` §1.1 for the design.
- Fixed `/btw` context budget and input layout in long conversations; in non-secure contexts (non-HTTPS access) side request IDs are generated via a fallback.

### Traffic

#### Added

- Findings support **linking multiple traffic evidence items**, which can be ordered, annotated, and distinguished by role (request/response/supporting).
- The report Agent **automatically links** relevant traffic evidence before writing the report.
- New Agent **traffic binding switch**, and completed the handoff of evidence between Agents.

### Tasks

#### Added

- Supports **session-level finding retest**: retests run in an independent Agent session, and the list and details show run status.

### Interception

#### Added

- Approval records add **details and execution audit**: view the tool request context, LLM/rule initial verdict, execution output and argument fingerprint.

### Assets

#### Added

- Task test assets support **DSL search**.

### LLM

#### Fixed

- Fixed the connection test not sending the custom session header, causing opencode zen to return 400.

### Network

#### Added

- MCP's HTTP transport supports **skipping TLS certificate verification**, making it easier to connect to services with self-signed certificates.

### UI

#### Added

- The conversation list is grouped by Agent, with expand/collapse and independent pinning; conversations can be filtered by Agent.
- The attack chain graph renders compression (digest) nodes and collapses their members.

#### Fixed

- Fixed a blank screen caused by out-of-sync login credentials.
- Interception messages now clearly label "platform control", to avoid being misread as target-side defense.

### Deployment and Updates

#### Added

- Supports **one-click update from the web UI**: the system config page adds a "Version and updates" card, and the top bar shows a notice when a new version is available. The flow is: download the release package -> verify `SHA256SUMS` -> smoke test -> stage -> exit and be relaunched by the supervisor script to complete the swap, with the page refreshing automatically.
- New supervisor launch scripts `start.sh` / `start.bat` as the official entry point (included in both release packages and the Docker image; `install.sh` is unchanged), which decide whether to relaunch based on the exit code and forward SIGTERM to artex. Verification and swap logic are all in Go, keeping the scripts dead simple.
- Automatic failure fallback: if verification or the smoke test fails, the update is discarded and the current version keeps running; if the new version fails to start 3 times in a row, it rolls back to the previous version. The settings page also offers manual rollback (note that the database schema is not rolled back).
- Updates only trust GitHub domains and enforce HTTPS, and the release source is not configurable; one-click update is disabled for development builds. GitHub query results are cached for 30 minutes to avoid the top bar notice exhausting the API quota.

#### Known Limitations

- Under Docker only the program is swapped, not the image: the toolchain won't be upgraded along with it, and recreating the container reverts to the image's bundled version; use `docker compose pull artex` when needed.
- `skills/` in the release package is not synced, so built-in skills added in new versions won't take effect automatically.
- Updating means restarting, which interrupts running tasks.

### Dependencies

#### Changed

- Upgraded norma to v0.3.7.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@RuoJi6](https://github.com/RuoJi6)

## [0.3.8] - 2026-09-09

### LLM

#### Added

- The LLM page adds a "Retry and backoff" tab: the **count** and **interval** of all five retry layers are configurable. A model call failure passes from inner to outer through five layers: connection retry (SDK, connection reset / timeout / 429 / 5xx before the stream starts), empty response retry (SDK, finished normally but without any content, openai format only), same-provider safe-window retry (replay on stream break before any output was delivered to the caller), round-robin circuit breaker (cool down and skip after consecutive failures reach a threshold), and intent rerun (rerun the whole intent after a worker ends with model_error); the outer layer only gets its turn once the inner one is exhausted. Each layer has two knobs with uniform semantics: empty = use the original default count and exponential backoff; a count uses that count; an interval replaces exponential backoff with a fixed interval; -1 = disable this retry layer. The first three layers follow the endpoint and can be overridden field by field in each model config over the global defaults (one that only pins the interval still inherits the global count); circuit breaker and intent rerun have process-level semantics, with only one global setting. Takes effect hot after saving, no restart needed. All empty means current behavior, and old DBs are byte-for-byte unchanged after upgrade (new columns default to 0, and missing settings keys mean all defaults). The connection test deliberately does not use these parameters - it has a hard 30s timeout, and stacking user-configured retries on it would only make a working endpoint fail with a timeout. See `docs/LLM-retry-design.md` for the design.
- Each LLM config supports a custom **session header** (`session_header_key`): when non-empty, every LLM request carries that HTTP header, whose value is the current run's session id (`conv-<id>` for chat sessions, `exp<x>-worker-i<intent>` for workers, etc.), for gateways that do prompt caching / sticky routing by a session-id header. Implementation reads the session id from the request context and injects it, with no changes to norma, so the same shared provider can also send different header values per session. Old DBs get a migration, and the frontend LLM config dialog adds an input field.

#### Fixed

- Fixed saving an LLM config erroring with `23502` when the session header field was empty: the column is `NOT NULL DEFAULT ''`, and previously `NULLIF($n,'')` was wrongly applied, writing an unfilled session header as NULL and triggering the not-null constraint; it is now passed directly with empty-string semantics.

### Agent

#### Added

- Wall-clock timeout now **wraps up in place**: relying on norma v0.3.6, when `MaxDuration` is reached it interrupts the running tool and continues on a live ctx for the wrap-up rounds (writing back recognized content + summary, final state timeout), instead of relying on worker/planner's own `maxDur+90s` external hard ctx that killed stuck runs outright as `aborted_tools`. chat goes through the same harness and gets the same behavior automatically; mainagent has no `MaxDuration` and is unaffected.
- Stall fallback: when the planner is woken by a heartbeat / no-change wakeup and the whole graph has no open or running intents, the opening message becomes a stall alert, explicitly stating that no worker is running and no directions are queued, and that this round must produce one or more non-duplicate new intents (producing 0 intents is not allowed).

#### Changed

- Worker prompt refactor: the intent / launch instruction / anchored asset raw JSON move into the system prompt, reassembled every round and never compacted away, and continuation no longer depends on the transcript's first message surviving; the launch user message is slimmed down to only the global situation overview (degradable, tolerates staleness). The cost is per-intent data mixed into the system prompt, losing cross-intent cache reuse - a deliberate trade-off for "never lose the intent".
- Streamlined the default planner / worker body text and fixed several real-world issues: the planner distinguishes `recent_done` by status (for blocked/exhausted check the trace before deciding - neither treat it as a dead end nor blindly rerun), negative conclusions become "observation / doubtful, not definitive" with evidence checked before accepting them, it must produce intents when the goal is unmet and there are no open/running intents (hard floor), and depth takes priority over coverage; workers only write negative conclusions as "observation + tentative reading", with judgment belonging to the planner, and cross-intent leads go into the fact's summary for the planner rather than being chased by the worker. Reseed appends the new defaults as a new version and switches to it, while user customizations / old versions remain in history and can be rolled back.
- Narrowed the work agent's default toolset and asset write-back hints: workers are only responsible for executing and writing back a single intent, and reading context / cross-work review are planning responsibilities, so `list_facts` / `node_detail` / `list_companies` and cross-work search (`search_all_worker_traces` / `list_worker_traces` / `get_worker_trace`) are removed from their defaults, leaving only `list_findings` (dedupe before reporting a finding) + `add_finding` / `record_fact` + `insert_assets` / `list_assets`; the prompt drops stale field descriptions that conflicted with the `insert_assets` schema (`type=tech`/`on_url`/`props`). Old DBs get a one-time migration stripping the corresponding bindings; planner/main bindings of the same name are untouched.

### Exploration Graph

#### Added

- **Exploration graph cold node compression (cold-digest)**: folds old, long-inactive intents / facts into digest nodes shown in `graph_overview`; the original nodes are kept permanently and can be fully restored by id (lossless storage, only presentation is compressed, folding is reversible). Hot/cold is determined by reverse reachability + any live branch means hot, with R=6 round debouncing and connected component grouping; background compression (minor folds uncovered cold blocks, major goes back to the source to recompress and merge fragments) includes an activity recheck and cooldown mutual exclusion, never touches the hot path and never overwrites revived nodes. The overview side provides `cold_digests` and an asset-indexed `cold_index`, and `expand_digest` / `expand_index` handle restoration. Overviews of related / inherited tasks likewise reuse their own folded views, and `expand_digest` supports cross-task read-only restoration. `expand_digest` / `expand_index` are given only to the planner and main agent, not workers. All include migrations for old DBs.
- `graph_overview` outputs the full `finding_list`: findings are a task's highest-value output and usually few per task, so they are now included in full in the overview (unlike facts, which only get a recent window), letting planner/worker see all confirmed findings at a glance every round without calling `list_findings`. Each is slimmed down to `{id, summary, evidence?, from_intent?, assets?}`, where affected assets are given as readable content (url / domain / ip:port) rather than bare ids.

#### Changed

- `graph_overview` no longer lists `hosts` flat: the coverage block drops the host list (in large-scope tasks each round carried up to 500 host strings, of limited value for planning decisions), keeping only `host_count`; specific hosts are queried via `list_assets` on demand. Adds top-level `done_intents_total` (total finished intents), parallel to `recent_done_intents` which is truncated to <=15, so the planner knows whether truncation occurred when deduplicating.

### Tools

#### Changed

- `add_company_scope`'s default binding moves from worker to planner: defining company asset scope is the responsibility of planning / main control / Auto, while workers only execute exploration. New DB seeds default to mainagent/planner/auto bindings, and old DBs get a one-time migration.
- The planner is bound to `list_assets` by default: it now has both `list_assets` (DSL search across the whole store) and `list_untested_assets` (untested within scope); old DBs get a one-time backfill that doesn't override user unbindings.

### Tasks

#### Added

- The task list adds a "Running Workers" column: counts intent nodes with `state='running'` under the task, defined exactly the same as "Running Workers" in the task detail overview, showing 0 when no Workers are running.

### Network

#### Added

- New **global egress proxy** config: all target traffic can go out through a unified global proxy (http/https/socks5, with `user:pass` support). When traffic capture is on it serves as the upstream of the MITM recording proxy (traffic is recorded then sent out via the proxy; both the intercept and passthrough paths use the upstream, without leaking the source IP); when capture is off it is injected directly into the agent's bash environment and WebFetch (`proxyEnv` adds `ALL_PROXY` for socks5 support). The config is stored in the settings KV table (no migration needed), and the frontend system config page adds a "Global proxy" card, independent of the web search proxy and LLM proxy.

### UI

#### Fixed

- Corrected semantic errors in intent status labels: `exhausted` "Exhausted" -> "Budget exhausted" (it actually means it was cut off midway on reaching the step / time budget and only wrote back partial results, not that the direction was fully explored), `blocked` "Blocked" -> "Execution error" (it actually means model / API / network failure retries were exhausted and the intent was barely explored, not blocked by the target / WAF), and added the previously missing `stopped` "Stopped" (work stopped manually by the user).

### Dependencies

#### Changed

- Upgraded norma to v0.3.4: MCP tool output gains truncation and spill-to-disk (see `651b961`); the later v0.3.6 supports wall-clock in-place wrap-up.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.7] - 2026-08-31

### LLM

#### Added

- Model config adds "Output limit", with a choice of which request field name the limit uses: the limit caps how many tokens a single reply can generate and is sent with each request; 0 (default) = don't send the field and let the server default decide. It is different from "Context window" - the latter is the model's total capacity, used only locally to compute the compression threshold, and does not appear in requests. The field name switch only matters for the `openai` (Chat Completions) format: empty (default) sends `max_tokens`, which the vast majority of compatible gateways only recognize; OpenAI's official reasoning models (o series / GPT-5) conversely only recognize `max_completion_tokens` and return `unsupported_parameter` on receiving `max_tokens`, so such endpoints must be switched to the new field manually. The two other formats each fix their field name (`max_tokens` for Anthropic, `max_output_tokens` for the Responses API), so for non-openai formats the switch is grayed out and cleared on save. Also connected a link that was never wired before: none of the five places - planner / worker / chat / main Agent / goal decomposition - had ever set an output limit, so the openai family never sent it and Anthropic used the SDK's 8192 fallback; just adding the config item without wiring this would mean filling in a number had no effect. The value is now resolved per round from the config like "Streaming output", so it takes effect on the next round after a failover switch. Behavior of old DBs after upgrade is completely unchanged (new column defaults to 0 and empty field name).
- When clicking "Test" in the admin UI, the status code and the gateway's raw response body for each HTTP attempt are written to the server log (response body truncated to 4K), making it easier to diagnose 401s, quota messages, empty frames, HTML pages returned, etc., instead of only seeing the collapsed ok / err in the UI. This log does not depend on the "LLM logging" switch.

#### Changed

- A task's LLM config chain can now be modified in any state, no longer restricted to "running / paused / config chain exhausted": after a task ends (done / failed / timeout) main Agent conversations still use this chain, and when a model on the chain had problems you previously could neither change it nor continue interacting. Both terminal-state guards in the backend HTTP and DB transaction are removed, and the frontend dialog also opens editing and saving for terminal-state tasks. Saving a terminal-state task no longer reopens quota-blocked intents (they would be moved to open, with no worker to execute them, and would no longer meet the "rerun intent" conditions); to keep running, still use rerun intent / add goal, which bring the task back to running state.

### Agent

#### Changed

- "Send a message to a running Worker to adjust direction" now reuses the existing pause / resume + transcript resume mechanism, behaving the same as main Agent conversations: previously it used a home-grown intervention persistence protocol that touched the scheduling barrier, recovery flow and over a dozen activity query filters. Now the message is injected via the next round's input, and the intent runs directly in a dedicated goroutine, not limited by the 3-slot worker pool, running as soon as sent; the frontend keeps the message box and restores the "Continue directly" button, with sending carried over SSE. No DB schema changes. Trade-off: messages are kept in memory only with no crash recovery, and when a message is sent the task may momentarily exceed its concurrency by one worker (a low-frequency scenario, acceptable).

### Tasks

#### Fixed

- Fixed out-of-memory (OOM) crashes when archiving large tasks: archiving now writes the snapshot in a streaming fashion instead of reading the whole task into memory at once; also filled three recovery gaps on the cold archive path - in modern installs traffic lives only in SQLite, and previously a crash between the PostgreSQL commit and the SQLite commit left an archived task's traffic stranded in hot storage with no way to recover; now a staging journal is written regardless of whether a history directory exists, and a missing `journal.json` is treated as discardable.
- Fixed system sluggishness with many tasks: loading conversations and loading the UI had noticeable delays. Queries for the task list, task context and exploration records are optimized together, and the frontend dashboard, conversation page and task detail page also reduce duplicate requests.

### Assets

#### Changed

- Removed the "at most 256 rules" limit on company asset scope: companies entering scope IP by IP / domain by domain easily hit this cap, after which they could only split into multiple companies, while scope itself isn't slower because of more rules. The cap is removed in the frontend, backend and demo mock; a single rule is still limited to 1024 characters, and the 2 MiB request body limit is kept as a fallback (roughly forty to fifty thousand rules).

### Skills

#### Fixed

- Fixed uploading a skill archive failing with "Upload failed: zip: unsupported compression": the Go standard library only ships Store / Deflate decompressors, so bzip2 and Zstandard archives written by compression software at non-default levels could not be extracted. These two decompressors are now added (pure Go implementations, no new external dependencies); for methods that truly can't be extracted, such as Deflate64 / LZMA / XZ / PPMd, as well as encrypted archives, a localized message is now reported before extraction, naming exactly which file used which compression method and how to repackage, instead of throwing the underlying English error at the user.
- Fixed in-skill filename validation treating Chinese filenames as illegal: path validation was originally an ASCII allowlist of `[A-Za-z0-9-_./]`, so if an archive contained even one Chinese-named file (something like `参考/说明.md`), the whole upload failed with "Archive contains illegal paths". Changed to a Unicode denylist: filenames in any language and spaces are allowed, while control characters, invalid UTF-8, zero-width and bidirectional control characters (RLO filename spoofing), `\ % # ? * : " < > |` and `..` / absolute paths / empty path segments are still rejected; directory traversal protection is unchanged. Skill names are loosened likewise - ASCII is still limited to lowercase letters, digits and hyphens (agentskills.io spec), non-ASCII letters such as Chinese can be used directly in skill names, but spaces, dots and path separators are not accepted.
- Fixed Chinese archives made by Windows compression software having garbled filenames after extraction or being rejected entirely: such zips don't set the UTF-8 flag and write filenames in GBK; they are now decoded with GBK as a fallback before path validation. Also, quoted frontmatter like `name: "中文技能"` now correctly yields the skill name.

### UI

#### Added

- The findings page adds an "All findings" flat view, set as the default, switchable with the original "Grouped by task" view via header tabs: the grouped view requires expanding tasks one by one to see findings, which is a detour when you just want to scan the list across tasks. The flat view is one big cross-task table (10/20/50/100 per page), behaving exactly like the grouped view - select to export, expand inline to see evidence and the detailed report, edit name/category/severity inline, change triage status, dive deeper, delete. Both views share the stat cards and filter bar, switching keeps filters, and the current view and filters are remembered locally; polling only hits the current view, and inline changes are written to both caches, so switching over won't show stale data.
- The findings page adds a "By asset" view: an asset tree on the left (company -> root domain / IP -> subdomain -> service -> endpoint; the company level only appears when assets actually have an owner), and on the right the findings under the selected node's entire subtree, sharing the same table and the same filters as the other two views. The tree only includes assets with findings, and ancestor chains are filled in as needed (when a finding hangs only on the deepest endpoint, the levels above are still assembled), with node counts aggregated over the subtree and deduplicated by finding; findings whose asset was deleted or that never had a related asset go into an "Unlinked assets" bucket. Unlike the other two views, the asset view doesn't poll - it queries only on entering the view, changing filters, adding/deleting/editing findings in the page, or clicking the refresh button on the tree; the left tree is a navigation structure and there's no need to recompute it every 5 seconds. Each tree level only shows the increment relative to the level above (subdomains drop the root domain suffix, services show `https :443`, endpoints show only the path), with the full value in the hover tooltip and breadcrumb; when there are too many nodes, the endpoint/service levels are dropped entirely with a notice, and counts still roll up to the level above.

#### Fixed

- Fixed the conversation record being squeezed and partially hidden in conversation details on mobile: on narrow screens the conversation list collapses, giving the width to the record itself.

### Dependencies

#### Changed

- Upgraded norma v0.3.2 -> v0.3.3: adds `Config.MaxTokensField`, letting the OpenAI Chat Completions output limit be sent as `max_completion_tokens` instead (reasoning models only recognize this key). The two keys are mutually exclusive and only one is sent; the default is still `max_tokens`.
- Upgraded `golang.org/x/mod` v0.37.0 -> v0.40.0, fixing two dependency security alerts.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)
- [@begininvoke](https://github.com/begininvoke)

## [0.3.6] - 2026-08-27

### LLM

#### Fixed

- Fixed the "Non-streaming" switch reverting to "Streaming" after saving and reopening (#69): the config list API's DTO was missing the `streaming` field, so the response never returned it, and the frontend fell back to the default streaming whenever it read `undefined`; the actual write and DB storage were fine, it just couldn't be read back. The field is added back to the DTO (without `omitempty`, since `false` must also appear in the response).
- The connection test now verifies the model actually replies, and tests with the config's real send/receive mode (#65): previously it only checked whether HTTP succeeded, so when the request went through but the model replied with nothing (thinking burned the budget / body swallowed by a safety policy / compatibility layer dropped `content`) it still reported "Connection successful", disconnected from the "no reply" behavior in conversations; and the test always used streaming, so for non-streaming configs it was actually testing a different channel. Now an empty reply is failed outright, a success shows the model's reply in the message, and the current streaming switch is carried into the test, so "test passed" stays consistent with "conversations work".

### Agent

#### Changed

- `list_facts` switches to pagination + keyword filtering, to avoid a single call blowing up the context when there are many facts (#74): returns the latest 20 by default, supports `limit` (max 100) / `before` cursor / `q` summary keyword, and returns `{facts, total, has_more, next_before}`; an overly long single summary is truncated by character count (full text still via `node_detail`). The worker / planner prompts are updated to the pagination semantics. Old DBs get a one-time migration that refreshes the new parameter schema into the tool catalog table (`SeedTool` only inserts the first time; otherwise the tool management page would show "No parameters").

### Tools

#### Added

- The "Tool executions" page adds tool call count statistics (#72): the "Stats" button in the toolbar opens a dialog showing call count, share and failure count per tool (sorted by count descending); it reuses the list's task / keyword filters, computes stats over the whole result set rather than the current page, and fetches only when the dialog opens.

### Dependencies

#### Changed

- Upgraded norma v0.3.1 -> v0.3.2: fixes reasoning/refusal being silently dropped in OpenAI-family responses (Responses adds `reasoning_text`), dedupes the three names of the reasoning field, bounded retry for empty responses (zero content blocks), and gateway 400s caused by compaction boundaries cutting tool pairs (also fixing existing orphans already baked into transcripts).

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)

## [0.3.5] - 2026-08-25

### LLM

#### Added

- Each LLM config supports switching between streaming and non-streaming (default streaming): on uses streaming SSE; off uses true non-streaming (`stream:false`, returning the complete JSON at once), which can bypass some gateways' bad SSE implementations (empty frames, dropped thinking fields), at the cost of losing real-time progress and real-time token counts while running. worker / planner / mainagent / chat / goals all read the value dynamically from the currently active config; the three Provider wrapping layers `llmpool` / `llmrec` / task runtime all support non-streaming; `llm_profiles` adds a `streaming` column with an `ALTER` for old DBs (default `true`, transparent to old configs).
- Supports LLM configs in the OpenAI Responses API format: each config gains a third format, `openai-responses` (hitting `POST /v1/responses`), alongside Chat Completions / Anthropic; `BaseURL` normalization, default model `gpt-5`; the `format` constraint of `llm_profiles` adds `openai-responses` with an idempotent migration for old DBs; the frontend format dropdown adds "OpenAI (Responses API)". Dependency upgraded to norma v0.3.1 (includes a `reasoning_content` passback fix).
- Records raw HTTP LLM requests/responses: the real wire body is captured at the HTTP transport layer, preserving the tool schemas, `tool_use` blocks and raw SSE frames not visible in the normalized view; multiple attempts from norma's internal retries are kept one by one; `llm_records` adds `raw_request` / `raw_response` columns with an `ALTER` for old DBs; the records page detail panel adds a "Raw" view toggle and request/response copy buttons (with an `execCommand` fallback for non-secure contexts).

### Conversations

#### Changed

- Conversation list multi-select becomes a "Select" mode toggle: the default list no longer shows a checkbox on every row (a cleaner look), and the header becomes "N total + Select" button; clicking "Select" enters selection mode (checkboxes, select all, bulk delete), "Done" exits and clears the selection, and after a fully successful bulk delete it exits automatically (if any failed it stays in selection mode for retry); single-item rename / pin / delete still go through each row's ⋯ menu.

### UI

#### Changed

- Improved task management, conversation actions and traffic viewing (#57): task list column sort preferences are persisted, inline rename is triggered directly via an icon (no longer through a menu), and drawer (sheet) interactions and traffic viewing details are refined.

#### Fixed

- Worker asset tags only show domains and IPs, and empty tags are handled correctly.

### Agent

#### Added

- The planner adds a "quantitative acceptance check" when judging goals: when a goal contains quantifiable acceptance criteria (asset test coverage reaching X%, capturing N flags, gaining some privilege), before `prove_goal` it must check the measured values in `graph_overview` (`coverage.pct`, finding counts, etc.); if the measured value falls short, `prove_goal` is forbidden and it must dispatch intents to close the gap instead, and may not mark it met early on the grounds that "the main part is done / it's mostly achieved". Fixes a goal requiring 100% coverage being judged complete with only 40% measured.

#### Changed

- Rewrote the criteria for "0 intents this round" in the planner prompt: the original text described 0 intents as "the most common and most important principle", which made the planner stop too early while the goal was unmet and untested surface remained in scope. It now should produce 0 intents only in two specific cases - (1) all directions it can think of are already covered by open/running/recent_done intents; (2) the next step depends on output from a currently running work that hasn't appeared yet (it should wait for that to finish and plan on the next wakeup after the graph updates). A reverse constraint is added: when there really is a new direction that isn't covered and doesn't depend on running work, or the goal is unmet and untested surface remains, don't stop just because "0 intents is common".
- Strengthened the "evidence bar for negative conclusions" in the worker prompt: for negative conclusions like "not injectable / port closed / no login entry" that could make the planner abandon an entire direction, it must exhaust reasonable techniques within the intent (changing encoding/parameters/paths/methods) before concluding; if techniques weren't exhausted or evidence is weak, it must mark `confidence=inferred`, to avoid welding shut a whole route with a careless `observed` negative (wrong negatives early in a task especially steer the direction astray and rarely self-heal).
- The above planner / worker default prompt changes are appended as new versions and switched to as current via one-time migrations (`reseedPlannerPrompt` / `reseedWorkerPrompt`, each guarded by a settings flag); old versions remain in version history, and users who customized prompts can restore them from the version records.

### Operations

#### Added

- New `reset-password.sh` to reset the admin password (username fixed as `ARTEX`): supports both local and docker deployments, with connection info specified explicitly or read automatically from `--dsn`/`$ARTEX_PG_DSN`/`config.json`; it uses `pgcrypto` inside the DB to generate a bcrypt hash compatible with backend login and writes it back to `settings.auth.password_hash`, with no service restart needed after reset. The password is passed via an environment variable, never entering the process argv, and is escaped to prevent injection.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

## [0.3.4] - 2026-08-24

### UI

#### Added

- After selecting multiple tasks in the task list you can bulk change their category, and the target category supports "Uncategorized" to move tasks out of the current category; the whole batch is written in one transaction, and the only failed items can be tasks deleted after being selected.

#### Changed

- The "Task category" for new tasks changes from a dropdown to a searchable input: typing filters existing categories, a name not in the DB is created and selected instantly on Enter (or by clicking "Create" in the dropdown), and the selection is shown as a removable tag; still limited to a single category.

### Conversations

#### Fixed

- Fixed sending a message being blocked with "LLM not configured, cannot chat" when the conversation had an LLM config selected but there was no globally active config. The pre-send check originally only looked at the globally active config, while actual execution prefers the conversation's selected config, so the two resolution logics were inconsistent; they are now unified into one (conversation/Agent binding first, global as fallback).
- The message shown when chatting isn't possible is now broken down by state: with no config at all it says "Add a config", with configs but none active it says "Activate one or assign a config to this conversation", instead of always showing "Not configured", making it easier to pinpoint.

### Agent

#### Added

- For tasks whose goals are all met, intents dispatched by the main Agent via `add_intent` can be claimed and executed directly by Workers and stop when done: in this case the Planner no longer runs (a task with no open goals doesn't enter planning), preventing it from re-judging the goal as met and canceling the just-dispatched intent; after the frontier drains, the task returns to completed. Before dispatching an intent, the main Agent asks the user, based on the intent's content, whether to register it as a formal goal; if registered, the task resumes regular autonomous planning.

#### Changed

- When `step_ids` for `get_worker_trace` / `get_task_worker_trace` exceeds the per-call limit (5), it no longer errors outright; instead it returns the full content of the first 5 steps, and uses `returned_step_ids` / `omitted_step_ids` / `notice` in the result to say which were fetched this time and which weren't, with a hint "if this is enough, no need to fetch more". Duplicate and invalid ids passed in are deduplicated and removed before counting.

#### Fixed

- The task list is now sorted by task id descending (newest first), replacing the previous sort by creation time. Multiple tasks created at the same moment had identical timestamps and unstable ordering, which, combined with polling every 10 seconds and random iteration of an in-memory map, made them frequently swap positions in the list.

### Assets

#### Fixed

- Fixed company ownership recomputation being broken by a single dirty row: when `assets.ip` stored a hostname (written by the Agent or the asset API), `a.ip::inet` threw `22P02`, causing adding/editing company asset scope and deleting companies to all fail and roll back. Switched to the safe conversion `try_inet()`, which skips invalid values rather than aborting the whole statement.
- Unparseable `ip` values are no longer silently skipped: after saving a company scope, a clear message reports how many assets have an `ip` that isn't a valid IP (with the specific ids and values), warning that these assets won't be attributed by IP/CIDR rules; the same information is also written to the server log, covering paths with no frontend response (deleting a company, scopesentry sync, Agent writes).
- IP/CIDR matching for task test scope likewise switches to `try_inet()`, replacing the previous regex guard that only checked the character set (hostnames made entirely of hex letters, like `abc.def`, previously still slipped through and triggered the same error).

#### Changed

- The `ip` field of `ip`, `service` and `endpoint` assets no longer accepts hostnames: `insert_assets` and the asset API return a per-item error with an `index`, directly giving the fix (use `type=subdomain` and fill in `domain`, or resolve the A/AAAA record first), so the Agent can correct itself and reinsert. Other assets in the same batch are unaffected and stored as usual.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.3] - 2026-08-23

### Worker

#### Added

- Running Workers support individual pause, resume and cancel; pausing keeps intents, facts and findings, while canceling transactionally cleans up the current intent and its direct outputs after execution exits.
- Added the `paused` intent state, an execution fence and named termination reasons, preventing late blackboard writes after pause, cancel or task deletion.
- Added an optional task concurrency limit; new, resumed, finding deep-dive and queue backfill all go through a unified persisted FIFO admission path.

#### Changed

- The default Worker per-run wall-clock duration is adjusted from 600 seconds to 1200 seconds; timed-out tasks restart the clock on the next real execution after revival.
- The real execution state of Workers, the Planner and the main Agent uniformly drives the task status badge, fixing it still showing "Idle" while Workers were running.
- Worker sessions keep individual controls and the current session token summary, and the model name moves to the right of the current session title.

#### Removed

- Removed the Worker multi-select, select all and bulk pause/resume UI, along with the Worker bulk control API and Mock contract.
- Removed the token badge and tooltip from Worker list rows; the underlying full token ledger and task aggregation APIs remain.

### LLM

#### Added

- Tasks support an ordered LLM config chain; when a provider quota shortage is explicitly identified it automatically switches to the next config, and persists the current config, exhaustion state and error summary.
- Running and paused tasks support editing the full config chain, reordering, and manually switching the current config, effective from the next LLM call.
- Automatic switches, manual switches and full-chain exhaustion are written as structured system activities and pop up as deduplicated alerts via the task activity stream.
- Added a task role model resolution API that uniformly resolves the config and model used by the next call of the Main Agent, Planner and Worker.
- Added an optional global LLM Pool, supporting configurable call order, designated model failure fallback, health status, cooldown recovery and manual reset.
- Added an always-on LLM usage ledger that aggregates input, output and cache tokens by task, conversation, model and config.

#### Changed

- Goal decomposition, Planner, Worker and the main Agent share a task-level LLM runtime (resolution priority below).
- Failover only responds to explicit quota, balance or billing errors; ordinary rate limiting, authentication, network, server and context errors don't trigger a wrong switch.
- The LLM config page changes to a list of config cards with a right-side drawer for editing, and provides Pool round-robin order, priority, exclusions, health status and recovery actions.
- Config chain exhaustion info wraps on narrow screens; the current model changes from an icon in the conversation list to a text label next to the current conversation title with a full tooltip.
- The order in which each in-task role resolves its LLM is adjusted to "Agent binding -> task config chain -> global": a role explicitly bound to a model always runs on that model, unbound roles fall to the task chain, and to global when the task chain is empty.
- An empty outbound proxy means direct connection, no longer falling back to the `HTTP_PROXY`/`HTTPS_PROXY` environment variables (an explicit `ARTEX_LLM_PROXY` is unaffected); proxy input supports `socks5://user:pass@host:port` with credentials.

#### Fixed

- Transient streaming failures before commit (before any output is produced) are safely retried with backoff on the same provider, significantly reducing cases of "only ran one or two tools then broke off, no summary, final state `model_error`"; quota exhaustion, context too long and deterministic 4xx errors are not retried, and are still handed to failover and compaction recovery respectively.

#### Removed

- Removed the icon-style LLM marker in the conversation list, to avoid confusion with Worker status icons.

### UI

#### Added

- Tasks support creating, renaming, deleting and filtering global categories; new tasks can pick a category directly.
- Added global task template CRUD and a right-side management drawer; new tasks can load a preset description and goals, and the current content can be saved as a template.
- New tasks support linking multiple source tasks and multiple company asset scopes; company scope automatically recognizes domains, URLs, IPs, CIDRs, ICP and company keywords from a single multi-line text box.
- Task test assets support real-time adding and deleting, recording sources such as manual, company, inherited or discovered by the Agent; the Worker session title shows a summary of the current test assets and their sources.
- The findings page groups by task with independent pagination within each group, and supports writing a description for a finding and creating a high-priority deep-dive intent.
- Conversations support rename, pin, unpin and delete; the task list supports bulk pause and resume on the current page.
- Traffic details and tool execution details move to a right-side drawer; HTTP messages fill in `Host` and highlight the request line, status code, headers, JSON and markup bodies.
- The task list action column adds "Details" and "Pause/Resume" buttons; the conversation send key can be configured in system settings (`Enter` / `Cmd+Enter`, etc.), and the web search section adds a proxy input.
- The badge to the right of the conversation title now shows the LLM config name, with the model ID moved to a hover tooltip; tasks can have an optional name, and the task list adds a name column and search (falling back to the description when empty).
- The skills page adds usage count, last used time and missing dependency stats, making it easier to locate skills that aren't taking effect or aren't installed.

#### Changed

- Task titles become focusable detail links; task assets, company assets, findings task groups and in-group findings use real server-side pagination, stable sorting and exact totals.
- Adding a company now uses a right-side drawer, with scope entry unified into multi-line text providing real-time recognition, validation and type preview.
- The main Agent input supports auto-grow, `Enter` to send, `Shift+Enter` for a newline, and avoids accidental sends during IME (e.g. Chinese input method) composition.
- Agent preview, task reports and related details uniformly use shared Markdown rendering; delete confirmations, long errors and mobile drawer widths are fixed uniformly.
- The task template picker displays and searches by template name and submits the template ID; the system's original font size is restored, and the app version is uniformly shown as `0.3.3`.
- Task totals and current session tokens only highlight the input, cache read and output values; the send button uses a simpler up-arrow icon.
- The task overview's test scope shows company names instead of IDs.

#### Fixed

- Plain text containing dots is no longer misidentified as an ICP filing number.
- Fixed name collisions in the variable catalog with global runtime variables (such as `{{.Now}}`), which caused duplicate keys to render in the Agent editor's variable list.

#### Removed

- Removed the standalone "Enter" button on task cards; clicking the task title now uniformly opens the details.
- Removed the dashboard "New task" button, the Logo URL input when adding a company, and the finding count badges at the top of the findings page and in task groups.
- Reverted the style that enlarged the global font by 10%, and removed the Worker checkboxes, model icons and per-row token stats in the conversation list.

### Agent

#### Added

- New tasks can directly link multiple existing tasks, inheriting in real time and read-only the goals, facts, findings, completed intents, asset scope and blackboard context of the direct source tasks.
- Blackboard read tools support querying source task nodes, facts, findings and execution traces on demand; inherited nodes carry a source marker and all write tools refuse to modify them.
- Company scope tools support domains, URLs, IPs, CIDRs, ICP and company keywords; keywords serve only as scope hints for the Agent and don't participate in automatic asset attribution.
- The finding deep-dive action creates a high-priority manual Worker intent in the original task with an asset anchor and a `derived_from` edge.
- The overview adds a "Goal management" card, supporting manually viewing, adding, editing and deleting goals; adding or editing notifies the Planner and revives the task, while deleting hard-deletes the goal node (cascading to edges and anchors) without reviving.
- The main Agent gets the `steer_work` tool by default, letting it inject course-correction instructions into running Workers in real time without interrupting them or losing progress (still verifying the intent belongs to this task).

#### Changed

- Source task intents don't enter the new task's frontier; the new task continues to use its own exploration, execution queue, working directory and conversation history.
- Deleting a running intent no longer destroys data: it now stops at `stopped`, requires a deletion reason, attaches the reason as a fact to the intent and writes it into the payload, and the planner perceives it via a `cancelled` trigger (keeping the intent content and reason).
- Main Agent conversations are now driven by the server-side activity stream, and input can be recovered on send failure; opening a conversation auto-scrolls to the bottom and keeps the last reply visible after details lazy-load.
- Pausing a task terminates the current Main Agent, Planner and Worker calls, but doesn't prevent the user from actively starting new main Agent orchestration sessions while paused.
- Cancel, close and stream interruption uniformly preserve the real termination reason, generated content, rounds run, duration, tokens and unreturned tool calls.

#### Removed

- Removed the main Agent client's optimistic message echo, to avoid duplicate messages and cross-conversation stream mixing under pause, failure or concurrent activity.

### Constraints

#### Added

- The goal decomposition phase first extracts operation constraints (allow/deny) before decomposing goals, and the main Agent can add more at runtime; constraints are injected at the highest priority into the Planner and Worker system prompts, and after debiasing, diversity and breadth exploration explicitly obey constraints.
- The overview adds a "Constraint management" card and CRUD APIs; constraint injection scope can be toggled separately for Planner / Worker (both on by default, read every round, effective immediately). The new table `task_constraints` uses `CREATE TABLE IF NOT EXISTS`, so old DBs create it automatically.

### Interception

#### Added

- Command interception adds LLM fallback approval beyond regex/string rules: when no rule matches, the model makes an `ALLOW`/`ASK`/`DENY` semantic verdict, with configurable failure fallback action and approval timeout action; the interception page is split into two tabs, "Interception rules / Model config", and model verdicts are labeled with a `[model]` prefix and reason.

#### Fixed

- Hardened judge output parsing so that a correct `DENY` verdict isn't mistaken for an approval.

### Traffic

#### Changed

- Traffic recording now stores each whole exchange in SQLite (large bodies spill into a blob bucket deduplicated by hash), with a trigram full-text index on text bodies supporting arbitrary substring and Chinese search; deletion degrades to a single SQL transaction, dropping from hours to milliseconds without stalling recording in the meantime. Adds streaming download of large bodies and a full-text parameter for `traffic_search`; old file-tree data needs no migration and can still be read, searched and deleted.

### Tasks and Assets

#### Added

- Task creation adds an "Asset coverage" switch (on by default): when off, coverage is neither computed nor shown, the situation graph shows only assets, `task_scope` is not accumulated automatically, and related tools are removed from the Planner / main Agent; company linking is unaffected by this switch.
- `insert_assets` adds a per-asset `related` flag (default `true`): it only takes effect when coverage is on, and `false` only stores the asset in the shared asset store without counting it toward this task's coverage (e.g. incidentally discovered sibling sites or unrelated assets).
- Company scope and task test assets uniformly support recognizing domains, URLs, IPs, CIDRs, ICP and keyword text; domains/IPs can create or reuse global assets, while CIDR/ICP/keywords are kept as task scope context.

### Build

- `build.sh --release` supports building Linux amd64/arm64, macOS amd64/arm64 and Windows amd64 in one go, generating zip release packages with `skills/`, a config example and the README.
- Uses the Go linker to strip debug info and generates zip release packages; UPX becomes explicitly optional, to avoid its self-extracting ELF segfaulting at startup in some Linux environments.
- The Release Workflow runs a startup smoke test on the Linux amd64 binary and ships `SHA256SUMS` with the release packages.

### Contributors

- [@neouks](https://github.com/neouks)

## [0.3.2] - 2026-08-20

### Added

- New tasks support linking multiple existing tasks, inheriting in real time and read-only the facts, findings, completed intents, asset scope and blackboard context of the direct source tasks; the new task still uses its own execution queue, working directory and conversation history.
- Tasks support an ordered LLM config chain. When a provider quota shortage is explicitly identified it automatically switches to the next config, persisting the current config, exhaustion state and structured audit activities.
- Running and paused tasks support editing the LLM config chain, reordering and manually switching the current config; automatic switches, manual switches and full-chain exhaustion are surfaced on the task detail page.
- Running Workers support pause, resume and cancel. Pausing keeps blackboard data, and canceling transactionally cleans up the intent and its directly produced facts, findings and execution records after the Worker stops writing.
- Deleting a task can optionally also clean up related assets, traffic, findings and task files on disk, with a deletion barrier, concurrency protection and auditable deletion stats added.
- Added an optional task concurrency limit; new tasks beyond the limit queue FIFO and start automatically once a run slot is freed.
- Added task asset pagination, company asset pagination, task category stats and a paginated intent Mock contract.
- Added the `build.sh` single-binary build script, supporting static frontend export, resource embedding, cross-platform targets and build version injection.

### Changed

- Task-level explicit LLM config chains coexist with the existing global LLM Pool; explicit chains keep strict quota failover semantics, and without an explicit chain the Agent binding and global config rules still apply.
- The main Agent input supports auto-growing multi-line input, `Enter` to send, `Shift+Enter` for a newline, and avoids accidental sends during IME (e.g. Chinese input method) composition.
- Planner, Worker and the main Agent share a task-level LLM runtime; the task chain uses the smallest context window among candidate models as the safe compaction threshold.
- The task detail page shows running, idle and paused states based on real LLM call status; source task facts, findings, intents, asset references and graph nodes are uniformly marked with their source and kept read-only.
- Agent preview, task reports and related detail views uniformly use a shared Markdown rendering component.
- LLM model config uses card and drawer interactions, and the model list scrolls independently within the drawer.
- Pausing a task no longer blocks main Agent conversations: main Agent orchestration sessions are independent of task pause, and new messages can still be sent while paused (pause only terminates the round currently in progress).
- The default wall-clock duration of a single Worker run is adjusted from 600 seconds to 1200 seconds.

### Fixed

- Removed the optimistic echo in the main Agent console in favor of pure server-side data rendering, fixing messages getting jumbled or mixing in content from other conversations in scenarios like pause or send failure.
- Opening a main Agent conversation scrolls to the bottom by default and fully shows the last reply: after the last reply's full content lazy-loads and expands, it auto-sticks to the bottom instead of being pushed off screen.
- Fixed the main Agent conversation continuing to run after pausing a task, and the orchestration Agent not stopping the main Agent in sync when it paused a task.
- Fixed the task status badge and action buttons being out of sync with the actual running state when a task completes or the Planner, Worker or main Agent is running.
- Fixed possible late blackboard writes or leftover files between Worker cancel, task deletion and concurrent writes.
- Fixed broken Markdown preview, long text overflowing delete confirmations, mobile widths, and some misaligned task detail buttons.
- Added named termination reasons for all Agent cancel paths; activity details can show who canceled, final state, rounds run, duration, token usage and unreturned tool calls.
- Fixed a race where the parent context could preempt and overwrite the named `shutdown` reason during backend shutdown, and garbled text caused by truncating Chinese activity summaries by bytes.
- Fixed cancel events with partial streaming output losing the real termination reason, while preserving content generated before the cancel.

### Contributors

- [@Autumn-27](https://github.com/Autumn-27)
- [@neouks](https://github.com/neouks)

[Unreleased]: https://github.com/Autumn-27/ARTEX/compare/v0.3.10...HEAD
[0.3.10]: https://github.com/Autumn-27/ARTEX/compare/v0.3.9...v0.3.10
[0.3.9]: https://github.com/Autumn-27/ARTEX/compare/v0.3.8...v0.3.9
[0.3.8]: https://github.com/Autumn-27/ARTEX/compare/v0.3.7...v0.3.8
[0.3.7]: https://github.com/Autumn-27/ARTEX/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/Autumn-27/ARTEX/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/Autumn-27/ARTEX/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/Autumn-27/ARTEX/compare/v0.3.3...v0.3.4
[0.3.3]: https://github.com/Autumn-27/ARTEX/compare/v0.3.2...v0.3.3
[0.3.2]: https://github.com/Autumn-27/ARTEX/compare/v0.3.1...v0.3.2
