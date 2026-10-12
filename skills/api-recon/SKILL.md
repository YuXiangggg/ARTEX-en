---
name: api-recon
description: Invoke this skill when collecting a website's API endpoints.
---

# API Recon (Frontend Endpoint Reconnaissance)

Given **authorization**, discover as completely as possible: the **backend API** (paths, methods, parameters, response bodies), the **frontend routes**, and **UI feature triggers** (Tabs, dialogs, table actions, etc.).

---

## Boundaries and Prohibitions (Agent must read · violating is out of scope)

This skill **only does API / parameter-surface reconnaissance**; it is not the vulnerability-hunting or penetration-exploitation phase.

### Task Boundaries

| Scope | Allowed | Prohibited |
|---|---|---|
| **Target** | Enumerate path, method, parameters, routes, UI triggers | SQLi/XSS/privilege-escalation/brute-force/fuzz vulnerabilities, packet-tampering attacks, destructive operations |
| **Auth** | Hook + stub/mock to bypass the **client-side** login gate | Asking the user for or guessing credentials; attempting real login-form submission |
| **Runtime** | Hook endpoints without credentials, using mock responses to get the SPA into the post-login shell | Flows that require a real backend session to continue |

### Credential-free Dynamic Analysis (Phase 3 default)

1. Use `preload.js` / `runtime_harvest.js` to **intercept and stub** bootstrap endpoints such as login, permissions, and menus;
2. Return a mock body with **correct structure, successful business code, and possibly empty data** for business query endpoints;
3. Let the frontend still render the post-login pages in an environment with no backend or a 401, thereby triggering more XHR/fetch/WebSocket;
4. **Empty data, blank tables, and placeholder UI are all expected** -- do not switch to real login or vulnerability testing because of this.

**In one sentence**: use mocks to prop up the frontend routes and component mounting, and **record only outbound requests**; what the backend returns does not matter, what matters is **which other endpoints the frontend will still call**.

### Hard Process Prohibitions

| Prohibited | Alternative |
|---|---|
| grep/curl/Read the main entry `index-*.js` to extract API paths before Phase 1 is done | Run `OUTDIR/harvest_static.py` |
| Hand-write scripts like `extract_apis.py` to replace harvest | Edit `OUTDIR/harvest_static.py` and rerun |
| Repeating the same grep/command after it fails >=2 times | Change strategy: read tool_logs, edit harvest, check reference |
| Skipping gates A/B and running the original `scripts/` directly | Copy to OUTDIR and adapt for the target |
| Real username/password, OTP, OAuth and other auth | stub/mock (see above) |
| Skipping stubs under the pretext of "getting real data" to do privilege-escalation/injection testing | Record outbound only, which is the recon boundary |
| Irreversible operations such as deleting, exporting sensitive data, bulk writes | Same applies to coverage clicks |
| Claiming all pages and endpoints are obtained without completing runtime + dynamic enumeration | See "Definition of Done" or note the limitation |
| Claiming all parameters are mastered without completing the parameter-trigger matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required/optional from a single runtime sample | Multi-sample diff or deriving from validation rules/errors |

---

## Two-Layer Model + Run Modes

| Layer | Output | Upper bound |
|---|---|---|
| **Static** (JS bundle) | Full endpoint paths, route draft, request-packing field candidates | No HTTP method; parameters need Phase 1b; misses runtime-concatenated URLs |
| **Runtime** (live session) | Method + body + response + dynamic URL + WS/SSE; multi-sample diff completes parameters | A page only sends requests when actually rendered; a single sample is insufficient to decide required/optional |

| Run mode | Engine | Use for |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventory, METHOD/params/response body, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Click Tabs/dialogs/tables for deeper feature coverage |
| **both** | depth first, then coverage | Most complete, longest running |

**Parameter methodology** (no generic script): paths via harvest/regex; parameters via **anchor window expansion + UI binding chain + multi-sample diff + error derivation** (grep recipes in section J of [reference.md](reference.md)).

---

## Definition of Done

All of these must be satisfied before claiming recon is complete:

- [ ] **Static**: Phase 1 harvest produces `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: at least one of depth or coverage; coverage/both must have **Hook in effect + dynamic enumeration loop**
- [ ] **Into the shell**: visiting a business path is not `/login` (watch for hash routing)
- [ ] **Parameters**: coverage/both completed the parameter-trigger matrix + `param_samples.json`; Phase 5 merges `params_merged.json`
- [ ] **Depth** (if a module page is blank): Phase 4 permission-tree restoration and rerun, until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 output complete (see Phase 5 output table); `insert_assets` writes service and endpoint assets

---

## Scripts and Gates

`scripts/` are reference templates only; running the originals directly and treating them as the final result is **prohibited**.

**Rule**: read first -> adapt for the target -> write to `OUTDIR` (e.g. `recon/`) -> record `CHANGES.md`; if it does not fit, rewrite per the methodology, borrowing only the structure.

| Gate | When | Reference script -> OUTDIR copy | Common required changes |
|---|---|---|---|
| **A (static)** | After Phase 0, before running harvest/spider for the **first time** | `harvest_static.py` / `spider_mpa.py` | **The default regex runs as-is on most sites**; only when the manifest/dialect does not match, change the endpoint regex, the webpack/Vite `publicPath`, the MPA exclude/cookie |
| **B (runtime)** | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success value, stubs, login regex, api prefix, hash/history |

**SPA mandatory order** (not interchangeable; Phase numbering takes precedence over "explore first, then script"):

| Step | Must | Prohibited |
|---|---|---|
| After Phase 0 is done | Next Bash = `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read the main entry `index-*.js` (usually >500KB) |
| Gate A | Copy the script -> make small changes as needed -> **run immediately** | Manually extract APIs first, then decide whether to harvest |
| Before Phase 1 is done | `wc -l` to verify output; on 404 adapt harvest and retry | Hand-write an extract script; repeatedly grep URLs not downloaded |
| From Phase 1b | grep only `OUTDIR/js/*.js` | Use the main bundle instead of harvest |

- OK: copy `harvest_static.py` -> (optional) change regex -> **run immediately**
- NO: curl the main bundle -> grep many times -> write a temporary extract -> harvest only at the end
- **MPA**: after Phase 0, the next Bash = `python3 OUTDIR/spider_mpa.py ...`

---

## Tool and Output Constraints

| Constraint | Note |
|---|---|
| Large files | `index-*.js` >100KB must **not** be Read/grepped into the context; batch-process with OUTDIR scripts |
| grep output | Must use `\| head -20` or `-m 5`; keep only path summaries in the conversation, do not paste bundle fragments |
| Verification | Use `wc -l`, `ls \| wc -l`; do not Read a whole directory |
| regex first look | Optional, <=1 time, only a <=50KB small chunk or HTML; the formal static source is harvest |
| reference | Recipes/templates/troubleshooting in [reference.md](reference.md); do not repeat the full text inline |

---

## Execution Roadmap

```
Phase 0 classify + OUTDIR
  -> Gate A -> Phase 1 harvest (★ run immediately ★)
  -> Phase 1b parameter reversal
  -> Phase 2 three auth gates -> config.json
  -> Gate B -> Phase 3 runtime + parameter matrix
  -> Phase 4 permission tree (when needed) -> rerun Phase 3
  -> Phase 5 merged report + insert_assets to bulk-insert all discovered service and endpoint API assets; on insert, never omit a discovered asset for any reason
```

Check off in order; **you must not enter the next Phase until the previous item is done**.

1. [ ] **Phase 0**: first look at SPA/MPA; create `OUTDIR` -> [Phase 0](#phase-0--classify)
2. [ ] **Gate A + Phase 1**: copy script -> harvest **immediately** -> `wc -l` verify -> [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: anchor window expansion + binding layer -> `param_candidates.json` -> [Phase 1b](#phase-1b--parameter-reversal)
4. [ ] **Phase 2**: three auth gates -> `config.json` -> [Phase 2](#phase-2--three-auth-gates)
5. [ ] **Gate B**: adjust the runtime script -> [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm into the shell; parameter-trigger matrix -> `param_samples.json`
7. [ ] **Phase 4** (if needed): permission tree -> patch stubs -> rerun Phase 3 -> [Phase 4](#phase-4--permission-tree-restoration)
8. [ ] **Phase 5**: merge output + report + `insert_assets` -> [Phase 5](#phase-5--merge-and-report)

---

## Phase 0 — Classify

Fetch the entry HTML and **create `OUTDIR`** (do not modify the `scripts/` inside the skill):

- **SPA**: empty shell + `<div id=app>` + chunks -> Phase 1-5
- **MPA**: SSR + `<form>`, no endpoint bundle -> after Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Produces `forms.txt`, `links.txt`, `api_inline.txt`. For a SPA, if forms ≈ 0 -> switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts and Gates](#scripts-and-gates) · [Tool and Output Constraints](#tool-and-output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

harvest: parse HTML scripts -> webpack/Vite manifest -> download all lazy chunks -> produce `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- chunk count vs manifest: on 404, adapt harvest and retry; do not manually curl chunks one by one
- `api_static.txt` too small -> loosen the endpoint regex inside OUTDIR and rerun (see reference)

### Phase 1b — Parameter Reversal

Paths come from Phase 1; parameter fields must be recon'd separately. grep rules in [Tool and Output Constraints](#tool-and-output-constraints).

**Completion criterion**: for important endpoints you can answer -- field name, transport location, type inference, whether required, sample value, confidence.

#### 1b.0 — Transport Shape

| Shape | Where the parameters are | Static priority |
|---|---|---|
| REST JSON | body + query | `(params\|data\|body)\s*:\s*\{` near the path anchor |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Traditional form | urlencoded | `<form>`, `FormData` |
| File upload | multipart | `FormData.append` |
| Path params | `/user/:id` | route table + `useParams` / `$route.params` |
| Encrypted/signed | packed into `sign`/`data` | Hook the encryption function's arguments (reference section D) |

Output: annotate each endpoint with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor Window Expansion

Using a known path as the anchor, expand the window to find the request-packing object:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper layer | Parameter clue |
|---|---|
| axios instance | `data` / `params` |
| unified request | interceptor injects global fields |
| OpenAPI client | generated method signature |
| React Query / SWR | hook's second argument |
| Vue composable | composable arguments |

Type remnants: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger.

-> `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — Binding Layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding source | Technique |
|---|---|
| form submit | follow submit -> transform -> API |
| table search | `getFieldsValue()` -> `params` |
| route | `:id` / `?tab=` |
| interceptor | global `tenantId`, pagination, sign |
| enum select | `options` -> API enum value |

From the DevTools call stack, trace upward from `fetch`/`XHR.send` to the request-packing function.

#### 1b.3 — Three Packing Questions (≠ the Phase 2 three auth gates)

| Question | What to answer |
|---|---|
| **Assembly** | where the payload is built, transform traces |
| **Validation** | required, pattern, enum |
| **Transport** | path / query / body / multipart / header |

At the interceptor gate (Phase 2), also read the globally injected fields (Authorization, `X-Tenant-Id`, sign).

#### 1b.4 — Handoff to Phase 3

Candidate fields come from the static/binding layer; **required/optional/conditional dependency** must come from the Phase 3 parameter matrix + diff + Phase 5 error derivation.

---

## Phase 2 — Three Auth Gates

grep in `OUTDIR/js/` (with `head`), write to `config.json` (recipes in reference):

| Gate | Question | Keywords |
|---|---|---|
| **Render gate** | How is logged-in state determined? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor gate** | What triggers the redirect to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content gate** | Where do the menu/permissions come from? | `menu`, `permission`, `role`, `acl`, `routes` |

Do not treat a localStorage key name as a credential -- it must be confirmed from the chunk/request chain.

**Exit = Gate B**: conclusions land in `config.json`, and edit `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observation (optional)

Use `preload.js` inside OUTDIR to confirm the session key name, Authorization, and nested API URLs:

| Config | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | headers observation |
| `extractUrlsFromResponse: true` | sub-APIs inside responses |
| `observe.storageReads/cookieReads: true` | backfill config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Each coverage round exports: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Gate B must have been passed; follow [Boundaries and Prohibitions](#boundaries-and-prohibitions-agent-must-read--violating-is-out-of-scope) · the credential-free mock strategy.

In `config.json` set `"runtimeMode": "depth" | "coverage" | "both"` (template in reference).

### Hook and stub (shared by depth + coverage)

| Layer | Scope | Purpose |
|---|---|---|
| L1 precise | auth/permission/bootstrap stub | pass the first-screen auth |
| L2 negative correction | all JSON responses | not-logged-in code -> success |
| L3 fallback | `/api` etc. not matched by L1 | empty success body, prop up the UI |

- **depth**: fake auth + `forward` to change the business code + `stubs`; traverse `routes` (hash/history); produce `runtime_api.json`
- **coverage**: inject `preload.js` at **document-start** (CDP `addScriptToEvaluateOnNewDocument` or Userscript)

Verify: `window.__API_RECON_PRELOAD__` exists; business paths do not go back to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage dynamic enumeration (required)

1. Main navigation/sidebar -- click each item, wait 1-3s for the network
2. Tabs -- `role=tab`, `.ant-tabs-tab`
3. Tables -- view/edit/details on the first row
4. Toolbar -- export, filter, create (**avoid irreversible deletes**)
5. On entering each module -- merge APIs/routes
6. SPA -- controlled `pushState` for paths in `routes.txt` not covered (prohibited for MPA)

**Parameter-trigger matrix** (required): for each module, record once per operation type, and **diff multiple samples**:

| Operation | Parameters usually added |
|---|---|
| list first screen | pagination + default filter |
| click search | keyword, filter |
| advanced filter | more optionals |
| create/edit | full entity |
| bulk/export/sort | `ids[]`, `exportType`, `sortField` |

**Under stubs, the outbound body/headers are still real** -- take the request as authoritative. Record -> `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, then 3b coverage

---

## Phase 4 — Permission Tree Restoration

**Trigger**: module page is blank / each route only has bootstrap (e.g. locale) -> the content gate was not passed.

| Symptom | Meaning |
|---|---|
| Into the shell successfully | render gate + interceptor gate passed |
| Sidebar missing items / clicks go blank | stub shape or permission codes incomplete |
| Each route has the same and very few APIs | `v-if permission` not passing |
| `routes.txt` far fewer than the bundle | must be completed from the auth module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) -> `getResultTree` -> `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate output: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

stub check: the outer `response_code` matches the interceptor gate; the flat codes align with the tree; `routes` covers all links in `route_map`.

After updating `config.json`, **rerun Phase 3**. For a large SPA, you can tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference sections A3/I).

---

## Phase 5 — Merge and Report

### Output Table

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | static bundle and paths |
| `param_candidates.json` | 1b | static parameter field candidates |
| `config.json` | 2 | three gates + runtime config |
| `runtime_api.json` | 3a | depth detailed recording (incl. WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | multi-sample, click log, detail |
| `route_map.json` etc. | 4 | permission-tree intermediate files (if executed) |
| `params_merged.json` | 5 | merged parameter fields + confidence |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | routes, APIs, params, feature points, limitations |
| **insert_assets** | 5 | write all service and endpoint assets into the asset store |

### 5b — Parameter Merge

Diff from `param_samples.json`; **no generic merge script**. Confidence rules in reference J7 (high/medium/low/to-be-triggered).

### 5c — Error Derivation

Within the authorized scope, you may send incomplete requests to read 400s (**this is parameter recon, not vulnerability testing**): `field 'x' is required`, enum errors, etc. Watch for the `data` wrapper, `variables`, and the pre-encryption `bizData`.

The report must note: runtimeMode, static/runtime API counts, parameter confidence, uncovered modules, and a `CHANGES.md` summary relative to the reference scripts.

Suggested structure for `site_map.json`:

```json
{
  "site": "https://example.com",
  "runtimeMode": "both",
  "appType": "vue-spa",
  "routeGuardStrategy": ["nav-neutralize", "L1-auth", "L2-patch", "forward"],
  "apisFromStatic": [],
  "apisFromRuntime": [],
  "apis": [],
  "params": [{ "method": "POST", "path": "/api/user/list", "transport": "json", "fields": [] }],
  "frontendRoutes": [],
  "routesVerifiedByClick": [],
  "featuresTriggered": [],
  "limitations": ""
}
```

More fields and grep recipes in [reference.md](reference.md).

---

## General Notes

- **Framework-agnostic**: the method is the same for webpack/Vite/Angular lazy load
- **Transport**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope
- **SSR**: client-side fetch can be recorded; RSC/Server Actions are not fully enumerable
- **Blind spots**: JSVMP, WASM, strong HMAC/mTLS validation -> static + note the limitation
- **Parameter blind spots**: conditional linkage, hidden params, WASM packing -> "to-be-triggered" / "unreachable"
- **Static is the safety net**: when runtime is blocked, static can still enumerate endpoints

---

## Additional Resources

- Grep recipes, `config.json` template, troubleshooting, Hooks, parameter-reversal section J, site_map template: **[reference.md](reference.md)**
- Reference script paths in the [Scripts and Gates](#scripts-and-gates) table
