# api-recon — Reference Manual

Grep recipes, the `config.json` template, and troubleshooting. All greps run against the `js/` directory. When the bundle is on a single line, you can first `js-beautify` or `sed 's/}/}\n/g'`; usually a raw grep with a context window is enough.

## Script Notes

All files inside `scripts/` are **reference templates** and must be adjusted for the target site before running. Typical change points:

| Script | Common items to adjust |
|---|---|
| `harvest_static.py` | endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency |
| `runtime_harvest.js` | neutralize field names and success value, stub matching rules and body structure, routes source, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether to enable L3, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookie, depth/max, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming pattern |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer field names |
| `config.json` | the unified entry point for all of the site-specific parameters above |

It is recommended to put the adjusted files in the task working directory (e.g. `recon/`), and note in the report the specific changes relative to the reference scripts.

---

## A. Reversing the Three Gates

### A1. Render gate — "How is logged-in state determined?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Find the chain `isLogin = f(getUser())` -> `getUser = decode(storage.read(KEY))`, and determine the **storage key**, the **container** (Cookie vs localStorage), and the **encoding**:

| Encoding | How to forge in config |
|---|---|
| Plaintext string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | an unsigned / `alg:none` JWT, or a JWT signed with a key found in the bundle |
| Encrypted (SM2/AES/RSA) | find the hard-coded key; the render gate only needs a decodable blob, which can be forged; otherwise fall back to static |

-> write to `cookies` / `localStorage`.

### A2. Interceptor gate — "What triggers the redirect to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(not log(ged)? in|please log in|login required|session expired|unauthorized|unauthenticated|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Determine: the **field name**, the **success value** (usually `0` or `200`), and the **failure value that triggers the redirect**. Verify with a junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

-> write to `neutralize.fields` + `neutralize.success`.

### A3. Content gate — "Where do the menu/permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two-layer data** (common in enterprise back-office systems):

| API | Typical payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | route guard, button-level ACL |
| `.../permissions/all` | `tree[{ code, position, children }]` | sidebar menu rendering |
| `userRouteAuth` in the bundle | `{ CODE: { url, name? } }` | code -> frontend path |
| `routeMap` in the bundle | `{ KEY: { name, link } }` | alias resolution (webpack `o.DASHBOARD`) |

Read the consumer code to confirm: how `getResultTree(tree, permissions)` filters, and which field the `v-if` / `hasAuth(code)` check uses.

**Manual forge** (small sites): build a permissive payload -> `stubs`.

**Full permission-tree restoration** (large sites, sidebar/submodules still blank): see **section I**.

---

## B. config.json Template

```json
{
  "baseUrl": "https://target/",
  "runtimeMode": "both",
  "chromium": "/usr/bin/chromium",

  "cookies": [
    { "name": "auth", "value": "b64json:{\"id\":1,\"username\":\"admin\",\"role\":\"admin\",\"func\":{},\"permissions\":[\"*\"]}" }
  ],
  "localStorage": { "token": "faketoken", "isLogin": "1" },

  "neutralize": {
    "fields": ["response_code", "code", "errno", "ret", "status"],
    "success": 0,
    "flags": { "success": true, "message": "ok" }
  },
  "forward": true,
  "loginUrlPattern": "/login",
  "apiPattern": "/api/|/rest/|/graphql",

  "mockTier": "L1+L2",
  "recordDetail": true,
  "observe": {
    "storageReads": false,
    "cookieReads": false,
    "xhrHeaders": true
  },
  "neutralizeVueRouter": true,
  "stubs": [
    {
      "match": "permissions/all|/menu|role_permissions",
      "body": {
        "response_code": 0, "code": 0,
        "data": {
          "permissions": ["*"],
          "menus": [
            { "name": "dashboard", "path": "/dashboard", "show": true, "children": [] },
            { "name": "alert", "path": "/alert", "show": true, "children": [] }
          ]
        }
      }
    }
  ],

  "explore": {
    "clickTabs": true,
    "clickTables": true,
    "pushStateFallback": true,
    "maxMenuItems": 50
  },

  "routes": ["/dashboard", "/alert", "/asset", "/device", "/report", "/config", "/system"],
  "waitMs": 1500, "perRouteMs": 900, "headless": true,
  "waitUntil": "domcontentloaded",
  "routeTimeout": 12000,
  "proxy": "",

  "captureResponses": true, "recordWs": true, "respMax": 600
}
```

Field notes:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), `both`
- `cookies[].value` prefixes: `b64json:` -> base64(JSON); `json:` -> raw JSON; no prefix -> literal
- `forward: true` forwards the real request and rewrites the code field; `false` is fully offline stubbing
- `mockTier`: the tiers the coverage-mode preload enables, e.g. `L1+L2`, `L1+L2+L3`
- `routes` comes from `routes.txt`; after forging the menu, the harness automatically appends `<a href>`
- `captureResponses` / `recordWs` take effect only in depth mode
- `waitUntil`: use `domcontentloaded` for large SPAs to avoid `networkidle2` hanging
- `routeTimeout`: the per-route `page.goto` timeout (ms)
- `proxy`: Puppeteer `--proxy-server`; you can also set `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Dual-stub Template (role_permissions + permissions/all)

```json
"stubs": [
  {
    "match": "role_permissions",
    "body": {
      "response_code": 0,
      "data": {
        "permissions": ["MONITOR", "MONITOR_ALERT", "THREAT", "ASSETS_RISK"],
        "role_type": "SUPER_ADMIN"
      }
    }
  },
  {
    "match": "permissions/all",
    "body": {
      "response_code": 0,
      "data": [
        {
          "code": "MONITOR",
          "position": 1,
          "children": [
            { "code": "MONITOR_ALERT", "position": 1, "children": [] }
          ]
        }
      ]
    }
  }
]
```

The outer field names (`response_code` / `code` / `data`) must match the A2 interceptor gate; `permissions` must cover all leaf codes in the tree.

---

## C. coverage mode: preload configuration

Edit the `CONFIG` object at the top of `scripts/preload.js`, or replace it before injecting via CDP:

```javascript
const CONFIG = {
  loginPathRe: /\/(login|signin)(\/|$|\?)/i,
  mockTier: 'L1+L2',
  forward: true,
  recordDetail: true,
  extractUrlsFromResponse: true,
  neutralizeVueRouter: true,
  observe: { storageReads: false, cookieReads: false, xhrHeaders: true },
  neutralize: { fields: ['response_code', 'code'], success: 0 },
  stubs: [ /* same as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verify: `window.__API_RECON_PRELOAD__ === true` and the pathname is stable.

Export the recording:

```javascript
JSON.stringify({
  apis: [...window.__API_RECON_LOG__],
  detail: window.__API_RECON_DETAIL__,
  routes: [...(window.__API_RECON_ROUTES__ || [])],
  observe: window.__API_RECON_OBSERVE__,
}, null, 2)
```

---

## D. preload / runtime Hook Capabilities

The built-in browser Hook capabilities of preload (coverage) and runtime_harvest (depth) and their coverage:

| Hook capability | Value for API discovery | Coverage |
|---|---|---|
| Hook fetch / XHR.open | record request URL/method | ✅ `recordDetail` + `__API_RECON_LOG__` |
| Hook XHR.setRequestHeader | discover headers such as Authorization | ✅ `observe.xhrHeaders` |
| Hook localStorage/cookie reads | confirm the session key name | ⚠️ optional `observe.storageReads/cookieReads` |
| Vue get routes | complete frontendRoutes | ✅ `__API_RECON_ROUTES__` (loaded routes) |
| Vue route-guard neutralization / login-redirect blocking | prop up modules to trigger APIs | ✅ `neutralizeVueRouter` + native navigation neutralization |
| React get routes | complete routes | ⚠️ static + clicks; no dedicated Hook |
| Page navigation blocking (login path) | stay on the page to analyze | ⚠️ only block the login path, to avoid blocking business navigation |
| Hook the crypto library (CryptoJS/SM, etc.) | encrypted params -> plaintext API body | ❌ must manually Hook the crypto function's arguments; write the conclusion to config |
| Anti-debugging bypass | otherwise runtime cannot record APIs | ❌ must handle manually; static is still usable |

---

## E. Endpoint Extraction Regex (when static is too sparse)

Loosen `extract_endpoints` in `harvest_static.py`, or manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting

| Symptom | Cause -> fix |
|---|---|
| Very few static APIs | endpoint dialect does not match -> loosen the regex (section D) |
| chunk count ≪ manifest | CSS-only or undeployed chunks; 404 already retried |
| runtime still shows the login page | wrong render gate -> recheck A1: key name, container, encoding, domain |
| into the shell but modules blank | content gate -> forge the menu (A3); the `routes` paths may be wrong |
| each route only has bootstrap/locale | incomplete permission codes -> section I permission-tree restoration; check the `role_permissions` + `permissions/all` dual stub |
| sidebar has items but subpage is blank | the tree is missing intermediate nodes, or the code is inconsistent with `userRouteAuth` |
| every API redirects to login | interceptor gate -> confirm `neutralize`; nested fields need extended walk logic |
| WS frames are 0 | subscribe only after user interaction; increase `perRouteMs` |
| empty response body | there is a real response only with `forward: true` |
| Chromium missing | install chromium or set `config.chromium` / `CHROMIUM` |
| many mocks but still redirects to login | Hook too late or missing the `location.href` setter -> document-start + preload |
| list entirely empty | an empty array from L3 is normal; keep clicking Tabs/settings/details |
| mistaking a Redux action for a route | filter out internal paths containing get/set/change/clear/toggle/upload |
| Vue still redirects to login | preload is not at document-start -> change the injection timing; or when `neutralizeVueRouter: false`, clear the guards manually |
| a URL is in the response but not in the log | enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| don't know the Authorization header name | enable `observe.xhrHeaders` or view the request headers in DevTools |
| runtime very slow / timing out | set `waitUntil: domcontentloaded`; lower `routeTimeout`; do not use `networkidle2` |
| proxy connection fails | check `proxy` / env vars; keep the Puppeteer and curl proxy ports consistent |

---

## G. Hardened Targets

When the server progressively validates the session (an unforgeable signed cookie, a server-rendered menu that cannot be stubbed), runtime will get stuck at the shell. Expected behavior:

- **Static is enough for endpoint enumeration** -- the module paths are in the code
- If authorization allows, run the same harness with a **real session**: `forward: true`, no neutralize needed, capturing the real methods/params/responses

---

## H. Single-Task Checklist

1. Confirm the authorization scope
2. **Read** `scripts/harvest_static.py` -> adjust for the target -> run -> review `api_static.txt`, `routes.txt`
3. **Phase 1b**: path anchor window expansion + binding layer -> `param_candidates.json` (section J)
4. Reverse A1/A2/A3 -> write a site-specific `config.json`
5. **Read and adjust** `runtime_harvest.js` / `preload.js` before executing
6. `runtimeMode=depth`: `npm install` -> run the adjusted harvest script
7. `runtimeMode=coverage/both`: document-start inject the adjusted preload -> browser MCP dynamic enumeration + **parameter-trigger matrix**
8. Module does not render -> **section I permission-tree restoration** -> patch stubs -> rerun
9. Parameter multi-sample diff + error derivation -> `params_merged.json`
10. Merge -> `site_map.json` + `api_merged.txt`, honestly noting coverage, gaps, and script change points

---

## I. Permission-Tree Restoration (Phase 4 deep dive)

Use when forging a simple `menus: [{ path, show: true }]` is ineffective and submodules still do not mount.

### I1. Locate the auth module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: the **permission API path**, the **response field names**, and the **consumer chunk filename**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# produces recon/route_map.json
```

If `[!] no routeMap pattern found`: loosen the regex in `extract_route_map.py`, or grep manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build the permission tree + stub

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including the webpack alias `He=o.DASHBOARD`)
2. Use `route_map.json` to resolve alias -> real path
3. Infer the parent from the code prefix (`MONITOR_ALERT` -> `MONITOR`)
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`
5. With `--config`, automatically write the `stubs` and extended `routes` into `config.json`

**Adjust for the target** (at the top of the script):
- `DEFAULT_ROOTS`: the list of top-level module codes
- `DEFAULT_PREFIX_PARENT`: the `PREFIX_` -> parent mapping
- `DEFAULT_EXTRA_PARENT`: orphan nodes with a non-prefix relationship

### I4. Verify stub consistency

```bash
# the permissions count should ≈ the number of userRouteAuth entries
wc -l recon/perm_codes_all.txt
# routes should cover all links in route_map
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun runtime and compare

```bash
node recon/runtime_harvest.js recon/config.json
# compare the runtime_api.json counts before and after forging; check whether module APIs such as /attack, /asset appear
```

| Before forge | After forge (success) |
|---|---|
| the same 3-5 bootstrap items per route | different routes trigger different module APIs |
| only `/api/locale/language` | `/api/web/...` module endpoints appear |
| single-digit routes in `routes.txt` | 80-110+ `routes` from route_map |

### I6. When it still fails

- **coverage mode**: click the sidebar + Tabs; permission gating may only request after interaction
- **stub fields**: compare the real API (curl + real session) with the stub's nesting
- **extra guards**: grep for button-level checks such as `hasPermission|checkRole|func.`, and extend `role_permissions.permissions`
- **static fallback**: the module API paths are still in `api_static.txt`, runtime only completes METHOD/body; parameters keep `param_candidates.json` + the recorded samples

---

## J. Parameter Reversal (Phase 1b / 5b / 5c)

**A methodology, not a generic script.** Find paths with regex; find parameters with anchor window expansion + UI binding chain + multi-sample diff + error derivation.

### J1. Anchor window expansion — find the packing object from the path

```bash
# anchor on a path known from Phase 1
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper layer and transport shape

```bash
# axios / unified request
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart
grep -rhoaE 'FormData|\.append\(' js | head

# path parameters
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation gate — required / format / enum

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["\'][a-zA-Z_]+["\']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding layer — form -> API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

runtime backfill: DevTools -> Network -> request -> **Initiator** (call stack), trace upward from `fetch`/`send` to the packing function.

### J5. Encrypted parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Do not guess fields on the ciphertext** -- Hook the crypto function's **arguments**, recording the plaintext payload before encryption; write the conclusion to `config.json` / `param_candidates.json`.

### J6. Parameter-trigger matrix (required in Phase 3)

For each module, record once per operation, and diff the request body/query:

| Operation | Focus |
|---|---|
| list first screen | pagination defaults |
| search | keyword, filters |
| advanced filter | optional fields |
| create/edit | full entity |
| bulk/export | `ids[]`, `exportType` |
| sort/paginate | `sortField`, `order` |

Produce `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence rules

| Confidence | Condition |
|---|---|
| **High** | static callsite + runtime >=2 consistent samples |
| **Medium** | static only, or only 1 runtime sample |
| **Low** | derived from response/error, not re-verified |
| **To-be-triggered** | field known from static, UI/permission not reached |

### J8. Scenario quick-config

| Scenario | Order |
|---|---|
| REST list page | J1 packing object -> J6 four diffs -> J3 rules |
| create/edit form | J3 Form name -> J4 submit chain -> runtime submit + intentionally leave blank to see the 400 |
| GraphQL | J2 variables declaration -> runtime record variables for each operation |
| encrypted body | J5 Hook the arguments -> the pre-encryption fields are the real params |

### J9. Mapping to api-recon phases

| api-recon | Parameter recon |
|---|---|
| Phase 1 static | J1 anchor window expansion |
| Phase 2 A2 interceptor | globally injected fields (tenantId, sign) |
| Phase 3 runtime | J6 trigger matrix + `param_samples.json` |
| Phase 4 permission tree | different modules have different forms -> full fields are triggered only with enough permission |
| Phase 5 merge | `params_merged.json` + confidence; do not decide required from a single sample |

### J10. Troubleshooting

| Symptom | Fix |
|---|---|
| static has a field name but runtime never shows it | mark "to-be-triggered"; complete the permission tree / click advanced filter / each linked select option |
| same path, different body shapes | normal -- record separately by `action`, do not force-merge the schema |
| stub response is fake but you want to see params | **look at the outbound request** body/headers, do not derive from the stub response |
| 400 reports a nested field | watch for the outer wrapper `data`/`bizData`/`variables` |
| GraphQL only shows the operation name | expand the `variables` JSON; find `$var: Type` in static |

---
