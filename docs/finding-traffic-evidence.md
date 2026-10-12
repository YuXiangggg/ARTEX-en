# Multi-Traffic Evidence for Findings

The "Related Traffic" section of a finding's detail view supports cross-page multi-select, role and note, ordering, and unbinding. The traffic page also lets you select multiple records and link them to an existing finding in one go. Finding evidence inherited by a task is read-only; to modify it, go to the source task.

The "Agent auto-bind traffic" option in system settings is off by default; it is read and updated through `agent_traffic_binding` on `/api/settings`. Turning it on increases token consumption from reviewing requests/responses, tool calls, and prompts; the setting takes effect on the Agent's next turn. When it is off, the auto-binding parameters and the backfill-binding tool are hidden, no auto-binding guidance is injected, and new auto-binding submissions from already-running sessions are rejected. Manual binding, traffic capture, reading saved evidence, and export are unaffected.

When enabled, the default flow is "finding recorded -> report Agent triggered automatically -> verify and bind traffic -> write the report against the latest evidence version". The reporter keeps the verification command, key output, any existing real traffic IDs, and their roles in `evidence`; the report Agent combines the finding details and execution records, verifies them with `traffic_search` / `traffic_get`, calls `bind_finding_traffic`, then reads the latest `version` and saves the report. When the switch is off, the report Agent gets no extra raw-traffic search/read tools and does not auto-bind; it can still read snapshots bound manually and generate the report.

Compatible with existing callers: `traffic_refs` / `evidence_hint_id` on `report_finding` can still be used for explicit immediate binding. Binding is optional: for non-HTTP findings such as TCP, or when nothing was captured or the exact record cannot be found, the references can be omitted and reporting and report writing still work normally; keep other verifiable evidence such as command output and logs, and preferably explain why nothing was bound. No new required fields are added. All submitted IDs must be valid with complete bodies. Any single failure rolls back the whole binding operation; if explicit binding during reporting fails, the entire report is rolled back. Appending the same snapshot again does not add a binding and does not overwrite the note.

## Agent IDs and Report Versions

`report_finding` gains an optional parameter; the array order is the initial evidence order:

```json
{
  "traffic_refs": [
    {"traffic_id": "real-traffic-ID", "role": "baseline", "note": "normal account request"},
    {"traffic_id": "another-real-traffic-ID", "role": "proof", "note": "reproduction request"}
  ]
}
```

Roles are `baseline` (normal control), `proof` (vulnerability proof), `verification` (additional verification), and `supporting` (supporting evidence, the default). Verify real records with `traffic_search` / `traffic_get` first; domain and time are only used to filter candidates and do not imply task ownership.

The prompt draws on [CyberStrikeAI's vulnerability reporting tool guidance](https://github.com/RuoJi6/CyberStrikeAI/blob/54d56774b8bd285817d16d48b70a4a5e6e0963f7/internal/app/vulnerability_tools.go), combined with this project's optional-binding convention, without introducing a mandatory "reason for no packet" check. IDs must not be guessed, and probing should not be repeated solely to obtain packets.

The first line of the return value is still `finding recorded: <exploration node ID>`; the JSON that follows provides the standalone finding record `finding_id`, the exploration node `finding_node_id`, and a binding summary.

- `get_finding_traffic(finding_id)` uses the **standalone finding record ID** and returns the ordered list plus `version`; pass `binding_id`, `side=request|response`, `offset`, and `length` to read in segments, at most 8192 bytes per segment.
- The `finding_id` of `update_finding_report` **still uses the exploration node ID**. The new `evidence_version` holds the version actually read; if the evidence changes during generation, writes with the old version are rejected and the evidence must be re-read and the report regenerated.
- Legacy report calls that do not pass a version do not claim to cover existing traffic evidence. After a binding, note, role, or order changes, existing reports are flagged as needing an update.

With auto-binding enabled, `add_hint` / `add_task_hint` can store `traffic_refs` on a single hint or on each element of a batch `hints`. When the Planner reports on another's behalf it can pass `evidence_hint_id` to explicitly pick the references from the corresponding hint of this task; inherited hints cannot be referenced. The system never guesses bindings from domain, time, or browsing history. A failed submission does not create a partial finding or trigger a report prematurely.

When an existing finding is missing bindings, use `bind_finding_traffic(finding_id, traffic_refs)` to backfill them without re-registering. `list_findings` / `list_task_findings` / `node_detail` / `get_task_node_detail` return explicit `finding_id` and `finding_node_id`; the legacy `id` keeps its exploration node meaning.

At startup, only optional properties are added to legacy tool schemas, and the original default traffic tool bindings are extended to the report Agent; the backfill-binding tool is given to the report Agent by default. Custom binding lists, prompts, descriptions, and enabled states are preserved. Guidance is added uniformly after final tool assembly: the reporting role is responsible for handing over existing evidence, and the report Agent is responsible for verification, binding, and writing the report. When platform chat lacks task context, it should hand over to the task Agent via a structured hint instead of reporting directly. Existing evidence should be handed over before a task is judged complete; there is no forced wait when no packet exists. A failed `report_finding` does not trigger the report Agent.

## API

Base path: `/api/exploration/findings/{finding_id}/traffic`, using the standalone finding ID. Existing authentication applies; `context_task` checks task visibility and inherited read-only status.

| Method / relative path | Request / response |
| --- | --- |
| `GET` | Ordered summary, evidence version, version used by the report |
| `POST` | `{"traffic_refs":[...]}` append as a whole batch |
| `PATCH /{binding_id}` | `{"version":1,"role":"proof","note":"note"}` |
| `DELETE /{binding_id}` | `{"version":1}` |
| `PUT /order` | `{"version":1,"binding_ids":["2","1"]}`, must be the complete list |
| `GET /{binding_id}` | Snapshot metadata and a bounded body preview |
| `GET /{binding_id}/body` | `side`, `offset`, `length`; `download=1` downloads the full original bytes |

Version/order-set conflicts and writes during archiving return `409`; writes to inherited evidence return `403`; bindings that do not exist or do not belong to the finding return `404`; traffic/attachment read and verification failures return explicit errors.

## Storage and Migration

An idempotent PostgreSQL migration runs at startup: it adds `traffic_evidence_snapshots`, `finding_traffic_bindings`, and `findings.evidence_version` / `report_evidence_version` (default 0). No backfill bindings are guessed from historical text.

A snapshot stores the original traffic ID, capture time, URL, method, status, request/response headers, body length, and SHA-256. Bodies are stored by hash at `<data>/evidence/blobs/<first-two-chars>/<hash>.bin`, separate from the cleanable `data/traffic`; multiple findings can share snapshots/bodies. Snapshots have no content-update API; reads and exports fail on verification mismatch.

Full bodies are read under the original traffic write lock, including large body blobs and legacy directory records. Files are persisted and verified first, then the exploration node, intent relation, finding, snapshots, and bindings are written in a single PostgreSQL transaction; the planner is notified only after commit. A failure may leave unreferenced files behind, but never produces partial business records.

PostgreSQL advisory lock `7337741004` coordinates evidence files and SQL references; the task row lock forbids evidence modification once archiving is queued. Restore holds the evidence lock throughout, from body installation to metadata commit. Deleting a finding cascades to remove its bindings.

The cleaner runs hourly and only reclaims content that is unreferenced and not part of an in-progress operation, with a delay of at least 24 hours. Regular traffic cleanup does not touch the evidence directory. When backing up hot data, back up both PostgreSQL and `data/evidence`.

## Export and Archiving

Markdown includes the ordered evidence list and version; JSON includes metadata; CSV adds the count and binding IDs. `md-zip` keeps the finding Markdown and provides:

```text
evidence/<finding_id>/<binding_id>/
  manifest.json
  request.http
  response.http
  request.bin
  response.bin
```

Markdown references messages via relative links. Before the download is sent, attachment copying, hash verification, compression, disk sync, and CRC read-back verification of every ZIP entry are completed; anything missing/corrupted fails the entire download. Full attachments preserve the original binary bytes.

Archive v3 collects snapshots and bodies by finding binding relations, independent of the original traffic or domain. Hot data is cleaned only after the package has been verified; shared evidence is retained. Restore verifies and installs bodies first, then restores metadata and bindings transactionally, and supports retry on failure. v1/v2 remain restorable, with missing new fields explicitly set to 0.

## Verification and Limitations

Set up an independent, freshly created PostgreSQL test database for each test package, specified via `ARTEX_PG_DSN`, to avoid leftover task/model fixtures triggering background runs. Run the full tests for the relevant packages and confirm there are no skips caused by missing configuration:

```sh
# Before running each package, set ARTEX_PG_DSN to its own independent test database; explicit configuration failures must error out.
go test ./<package> -count=1
go test -race -p 1 ./evidence ./db ./agent ./server -run 'TestEvidence|TestFindingTraffic|TestFindingEvidence|TestReportFindingAtomicContract|TestTaskArchive'
```

Frontend verification includes `npx tsc --noEmit`, Biome checks on affected files, a Webpack build, and a `NEXT_EXPORT=1` static export. Use a separate cache directory to avoid clobbering a running dev server.

Local end-to-end acceptance uses a separate port, controlled HTTP / domain HTTPS targets, and a temporary data directory, covering both binding entry points, cross-page selection, ordering/notes, error messages, inherited read-only, download, and, after deleting the original traffic, export, archiving, reclaiming hot bodies, restore, and hash verification.

The first version uses a global evidence coordination lock; during large batch binding/export or slow attachment downloads, other evidence operations may wait. Evidence cannot be fabricated when recorded traffic or a full body is missing. This feature does not change the capture switch, nor does it handle certificate issues for direct HTTPS access by IP.
