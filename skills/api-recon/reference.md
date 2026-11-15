# api-recon — Reference Manual

Grep recipes, `config.json` templates, and troubleshooting guide. All grep operations are run against the `js/` directory. If bundles are minified onto a single line, you can format them with `js-beautify` or `sed 's/}/}\n/g'`, though raw grep with context windows is usually sufficient.

## Script Usage

All files in `scripts/` are **reference templates** and must be adapted for the target site prior to execution. Typical modification points:

| Script | Common Required Adjustments |
|---|---|
| `harvest_static.py` | Endpoint regex, webpack/Vite manifest parsing, micro-frontend publicPath, retry/concurrency settings |
| `runtime_harvest.js` | Neutralize field names and success values, stub matching rules and body schemas, routes source, WS recording, `waitUntil`/`routeTimeout`/`proxy` |
| `preload.js` | `loginPathRe`, L1 stubs, `neutralize.fields`, `apiPattern`, whether L3 is enabled, `recordDetail`, `observe.*`, `neutralizeVueRouter` |
| `spider_mpa.py` | `--exclude` destructive links, cookies, depth/max limits, same-origin filtering |
| `extract_route_map.py` | `routeMap` / `routeLink` regex, KEY naming patterns |
| `build_perm_tree.py` | `userRouteAuth` parsing, `ROOTS`/`PREFIX_PARENT` hierarchy heuristics, stub outer wrapper field names |
| `config.json` | Unified configuration entry for all site-specific parameters above |

Place modified files into the task working directory (e.g., `recon/`), and document exact differences relative to reference scripts in your final report.

---

## A. Reverse Engineering the Three Gates

### A1. Render Gate — "How is logged-in status checked?"

```bash
grep -rhoaE '.{0,40}(isLogin|isAuthenticated|loggedIn|hasLogin|requireAuth)\b.{0,80}' js | head
grep -rhoaE 'function (getUser|getToken|getAuth)[0-9]?\([^)]*\)\{.{0,200}' js | head
grep -rhoaE '(localStorage|sessionStorage)\.getItem\("[^"]+"\)' js | sort -u
grep -rhoaE '(Cookies?|cookie)\.(get|load)\("[^"]+"\)' js | sort -u
grep -rhoaE '\batob\(|JSON\.parse\(|jwt|decode' js | head
```

Trace the chain `isLogin = f(getUser())` → `getUser = decode(storage.read(KEY))`, and determine the **storage key**, **container** (Cookie vs. localStorage), and **encoding format**:

| Encoding | config Forgery Syntax |
|---|---|
| Plain string / `"1"` / token | `"value": "anything-truthy"` |
| `JSON.parse(x)` | `"value": "json:{\"id\":1,\"username\":\"admin\"}"` |
| `JSON.parse(atob(x))` | `"value": "b64json:{\"id\":1,\"username\":\"admin\"}"` |
| JWT | Unsigned / `alg:none` JWT, or signed with hardcoded bundle key |
| Encryption (SM2/AES/RSA) | Look for hardcoded keys; if render gate only needs decodable blob, forge it; otherwise rely on static recon |

→ Populate into `cookies` / `localStorage`.

### A2. Interceptor Gate — "What triggers redirection to /login?"

```bash
grep -rhoaE '.{0,60}(interceptors\.response|axios|request\.use).{0,120}' js | head
grep -rhoaE '.{0,40}(response_code|errcode|errno|\bcode\b|\bret\b|\bstatus\b)\s*[=!]==?\s*[\-0-9]{1,4}.{0,60}' js | head -20
grep -rhoaE '.{0,40}(unauthorized|login|expired|invalid|auth|token.{0,10}invalid).{0,40}' js | head
grep -rhoaE '.{0,30}(location\.href|router\.(push|replace)|navigate)\([^)]*login[^)]*\)' js | head
```

Identify: **field name**, **success value** (typically `0` or `200`), and **failure status triggering redirect**. Validate with junk session:

```bash
curl -sk -X POST -H 'Cookie: <fakekey>=junk' https://target/api/<protected> -d '{}' -H 'Content-Type: application/json'
```

→ Populate into `neutralize.fields` + `neutralize.success`.

### A3. Content Gate — "Where do menus and permissions come from?"

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl|resource|nav)[^"]*"' js | sort -u
grep -rhoaE '.{0,30}(menus|permissions|menuList|routeList|authList|role_permissions)\b.{0,120}' js | head
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|hasPermission|checkAuth' js | head
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head
```

**Two-layer data structures** (common in enterprise admin consoles):

| API | Typical Payload | Consumer |
|---|---|---|
| `.../role_permissions` | `{ permissions: string[], role_type }` | Route guards, button-level ACLs |
| `.../permissions/all` | `tree[{ code, position, children }]` | Sidebar menu rendering |
| In-bundle `userRouteAuth` | `{ CODE: { url, name? } }` | code → frontend path |
| In-bundle `routeMap` | `{ KEY: { name, link } }` | Alias resolution (webpack `o.DASHBOARD`) |

Read consumer code to verify: how `getResultTree(tree, permissions)` filters items, and which field `v-if` / `hasAuth(code)` evaluates.

**Manual forgery** (small applications): Construct permissive payload → `stubs`.

**Full permission tree recovery** (large applications with blank sidebars/submodules): See **Section I**.

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

Field Reference:
- `runtimeMode`: `depth` (Puppeteer), `coverage` (browser MCP), or `both`
- `cookies[].value` prefix: `b64json:` → base64(JSON); `json:` → raw JSON; no prefix → literal string
- `forward: true` forwards real requests and rewrites status code fields; `false` runs fully offline stubs
- `mockTier`: Coverage mode preload enablement tier, e.g. `L1+L2`, `L1+L2+L3`
- `routes` loaded from `routes.txt`; test harness appends `<a href>` tags automatically after forging menu
- `captureResponses` / `recordWs` are effective only in depth mode
- `waitUntil`: For large SPAs, use `domcontentloaded` to prevent `networkidle2` hangs
- `routeTimeout`: Navigation timeout per route for `page.goto` (ms)
- `proxy`: Puppeteer `--proxy-server`; or configure via `HTTP_PROXY` / `HTTPS_PROXY`

### B1. Dual Stub Template (role_permissions + permissions/all)

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

Outer wrapper field names (`response_code` / `code` / `data`) must match Interceptor Gate A2 expectations; `permissions` must encompass all leaf codes in the tree.

---

## C. coverage Mode: preload Configuration

Edit the top-level `CONFIG` object in `scripts/preload.js`, or substitute before CDP injection:

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
  stubs: [ /* Same schema as config.json stubs */ ],
  apiPattern: /\/(api|apis|v\d+|dev|internal|graphql)\//i,
};
```

Verification: `window.__API_RECON_PRELOAD__ === true` and pathname remains stable.

Exporting captured records:

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

Built-in browser hooking capabilities in preload (coverage) and runtime_harvest (depth):

| Hook Capability | Value for API Discovery | Coverage Status |
|---|---|---|
| Hook fetch / XHR.open | Log request URLs and HTTP methods | Full (`recordDetail` + `__API_RECON_LOG__`) |
| Hook XHR.setRequestHeader | Identify Authorization and custom headers | Full (`observe.xhrHeaders`) |
| Hook localStorage/cookie reads | Confirm active session key names | Optional (`observe.storageReads/cookieReads`) |
| Extract Vue routes | Complete frontendRoutes inventory | Full (`__API_RECON_ROUTES__` for loaded routes) |
| Neutralize Vue router guards / block login redirects | Unfold modules to trigger API requests | Full (`neutralizeVueRouter` + native navigation neutralization) |
| Extract React routes | Complete routes inventory | Partial (static + click exploration; no universal hook) |
| Navigation blocker (login paths) | Retain current page for analysis | Partial (blocks only login paths to prevent blocking business navigation) |
| Hook encryption libraries (CryptoJS/SM/etc.) | Encrypted parameters → plaintext API payloads | Manual (must hook encryption function inputs; record in config) |
| Anti-debugging bypass | Prevent runtime execution lockups | Manual (must handle manually; static recon remains usable) |

---

## E. Endpoint Extraction Regular Expressions (When Static Recon Yields Sparse Results)

Loosen `extract_endpoints` in `harvest_static.py` or inspect manually:

```bash
grep -rhoaE '"/[a-z][A-Za-z0-9_/\-]{3,}"' js | sort -u
grep -rhoaE '/api/[a-zA-Z0-9_./-]+' js | sort -u
```

---

## F. Troubleshooting Guide

| Symptom | Cause & Recommended Fix |
|---|---|
| Very few static APIs discovered | Endpoint dialect mismatch → Loosen extraction regex (Section E) |
| Discovered chunks $\ll$ manifest | CSS-only or unreferenced bundles; 404 retries completed |
| Runtime still displays login page | Render gate failure → Re-check Section A1: key names, container, encoding, domain |
| Reached shell but modules are blank | Content gate failure → Forge menu (Section A3); check `routes` path formatting |
| Every route only calls bootstrap/locale | Incomplete permission codes → Reconstruct permission tree (Section I); check dual stubs |
| Sidebar items exist but subpages blank | Missing intermediate tree nodes or code discrepancy with `userRouteAuth` |
| Every API triggers login redirection | Interceptor gate issue → Verify `neutralize`; extend traversal for nested fields |
| WebSocket frames recorded = 0 | Subscriptions require user interaction; increase `perRouteMs` |
| Empty response bodies | Genuine responses captured only when `forward: true` is active |
| Chromium missing | Install chromium or specify via `config.chromium` / `CHROMIUM` environment variable |
| Excessive mock calls still return to login | Hook timing too late or missing `location.href` setter → inject preload at document-start |
| All lists are empty | Empty arrays from L3 are normal; continue clicking tabs/settings/details |
| Redux actions mistaken for routes | Filter out internal paths containing get/set/change/clear/toggle/upload |
| Vue router still bounces to login | Preload not at document-start → adjust injection timing; or clear guards manually if `neutralizeVueRouter: false` |
| URLs appear in responses but not logs | Enable `extractUrlsFromResponse`; or extract manually from `__API_RECON_DETAIL__` |
| Authorization header name unknown | Enable `observe.xhrHeaders` or inspect request headers in DevTools |
| Runtime execution extremely slow / timing out | Set `waitUntil: domcontentloaded`; reduce `routeTimeout`; avoid `networkidle2` |
| Proxy connection failed | Check `proxy` setting / environment variables; ensure Puppeteer and curl use identical proxy port |

---

## G. Hardened Targets

When the server strictly validates sessions on every step (non-forgeable signed cookies, server-rendered menus that cannot be client-stubbed), runtime will halt at the shell. Expected behavior:

- **Static analysis is sufficient for endpoint enumeration** — module endpoints remain in source code.
- If authorized, execute the same harness with an **authenticated session**: set `forward: true`, disable neutralization, and record genuine methods, parameters, and responses.

---

## H. Single-Task Workflow Checklist

1. Verify authorized scope.
2. **Review** `scripts/harvest_static.py` → adapt for target → run → inspect `api_static.txt` and `routes.txt`.
3. **Phase 1b**: Path anchor expansion + UI binding layer → `param_candidates.json` (Section J).
4. Reverse engineer Gates A1/A2/A3 → write site-specific `config.json`.
5. **Review and adapt** `runtime_harvest.js` / `preload.js` before execution.
6. `runtimeMode=depth`: `npm install` → run adapted harvest script.
7. `runtimeMode=coverage/both`: Inject adapted preload at document-start → dynamic enumeration + **parameter trigger matrix** via browser MCP.
8. Modules fail to render → **reconstruct permission tree (Section I)** → patch stubs → rerun.
9. Multi-sample diff + error reversal → `params_merged.json`.
10. Merge findings → `site_map.json` + `api_merged.txt`, documenting coverage, gaps, and script adjustments.

---

## I. Permission Tree Reconstruction (Phase 4 Deep Recon)

Used when simple menu forgery (`menus: [{ path, show: true }]`) is ineffective and submodules fail to mount.

### I1. Locate Authentication Module

```bash
grep -l 'userRouteAuth' js/*.js
grep -l 'routeMap\|routeLink' js/*.js
grep -rhoaE 'getResultTree|role_permissions|permissions/all' js | head
```

Record: **permission API paths**, **response field names**, and **consuming chunk filenames**.

### I2. Extract routeMap

```bash
python3 scripts/extract_route_map.py recon/js recon/
# Produces recon/route_map.json
```

If `[!] no routeMap pattern found`: loosen regex in `extract_route_map.py`, or grep manually:

```bash
grep -rhoaE '([A-Z_][A-Z0-9_]*):\{name:"[^"]*",link:"/[^"]+"\}' js | head -20
```

### I3. Build Permission Tree and Stubs

```bash
python3 scripts/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Script logic:
1. Parse `userRouteAuth={MONITOR:{url:...},...}` (including webpack aliases like `He=o.DASHBOARD`).
2. Resolve aliases to actual paths using `route_map.json`.
3. Infer parent nodes by code prefixes (`MONITOR_ALERT` → `MONITOR`).
4. Output `permissions_tree.json`, `permissions_all_stub.json`, `role_permissions_stub.json`.
5. When `--config` is supplied, automatically update `stubs` and extend `routes` in `config.json`.

**Target-specific tuning** (at script top):
- `DEFAULT_ROOTS`: List of top-level module codes.
- `DEFAULT_PREFIX_PARENT`: `PREFIX_` → parent mapping.
- `DEFAULT_EXTRA_PARENT`: Orphan nodes without prefix relationships.

### I4. Verify Stub Consistency

```bash
# Total permissions count should approximately equal userRouteAuth entries:
wc -l recon/perm_codes_all.txt
# routes should cover all links in route_map:
python3 -c "import json; m=json.load(open('recon/route_map.json')); r=set(json.load(open('recon/config.json'))['routes']); print('missing', [v['link'] for v in m.values() if v['link'] not in r])"
```

### I5. Rerun Runtime and Compare

```bash
node recon/runtime_harvest.js recon/config.json
# Compare runtime_api.json entry counts before and after forgery; verify if /attack, /asset, etc., expose module APIs
```

| Before Forgery | After Forgery (Success) |
|---|---|
| Same 3–5 bootstrap calls per route | Different routes trigger distinct module APIs |
| Only `/api/locale/language` | Module endpoints such as `/api/web/...` emerge |
| Single-digit entries in `routes.txt` | 80–110+ routes sourced from route_map |

### I6. Fallback When Still Unsuccessful

- **coverage Mode**: Click sidebar and tabs; permission gates may fire only upon interaction.
- **Stub Fields**: Compare real API nesting (curl + authenticated session) with stub structure.
- **Additional Guards**: Grep for `hasPermission|checkRole|func.` button checks and expand `role_permissions.permissions`.
- **Static Safety Net**: Module API paths remain in `api_static.txt`; runtime simply adds HTTP methods/payloads; parameters retain `param_candidates.json` + recorded samples.

---

## J. Parameter Reverse Engineering (Phase 1b / 5b / 5c)

**Methodology, not a generic one-liner script.** Paths via regex; parameters via anchor expansion + UI binding chain + multi-sample diff + error reversal.

### J1. Anchor Window Expansion — Locating Request Assemblers

```bash
# Anchor on known Phase 1 paths:
grep -n '"/api/user/list"' js/*.js
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' js | head
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' js | head
grep -rhoaE '(get|post|put|delete|patch)\([^,]+,\s*\{' js | head
```

### J2. Wrapper Layers & Transport Encodings

```bash
# Axios / unified request clients:
grep -rhoaE '(axios|request)\.(get|post|put|delete|patch)\(' js | head
grep -rhoaE 'interceptors\.(request|response)' js | head

# GraphQL:
grep -rhoaE '(query|mutation)\s+\w+|gql`|graphql\(' js | head
grep -rhoaE '\$[a-zA-Z_]+\s*:\s*(Int|String|Boolean|\[)' js | head

# FormData / multipart:
grep -rhoaE 'FormData|\.append\(' js | head

# Path parameters:
grep -rhoaE 'path:\s*"/[^"]*:[^"]+"' js | head
grep -rhoaE 'useParams|route\.params|\$route\.params' js | head
```

### J3. Validation Gate — Required Fields / Formats / Enums

```bash
grep -rhoaE '(required|message|pattern|enum|validator)\s*:' js | head
grep -rhoaE 'yup\.|zod\.|async-validator|Form\.Item|a-form-item|el-form-item' js | head
grep -rhoaE 'rules\s*:\s*\[|name:\s*["'][a-zA-Z_]+["']' js | head
grep -rhoaE 'label.*value|options\s*:\s*\[' js | head
```

### J4. Binding Layer — Form to API

```bash
grep -rhoaE 'onFinish|handleSubmit|getFieldsValue|validateFields' js | head
grep -rhoaE '(pick|omit|transform|dayjs|moment)\(' js | head
```

Runtime assistance: DevTools → Network → Request → **Initiator** (call stack) to trace assembly functions upward from `fetch`/`send`.

### J5. Encrypted Parameters

```bash
grep -rhoaE 'encrypt|decrypt|sign|CryptoJS|sm2|sm3|sm4|RSA|AES' js | head
```

**Never guess fields on ciphertext** — hook the encryption function's **input arguments** to record plaintext payloads before encryption; write conclusions to `config.json` / `param_candidates.json`.

### J6. Parameter Trigger Matrix (Mandatory for Phase 3)

Record each operation type per module and diff request bodies/queries:

| Action | Primary Inspection Area |
|---|---|
| Initial list view | Default pagination values |
| Search click | keywords, filters |
| Advanced filtering | optional query parameters |
| Create / Edit | Complete entity structure |
| Batch / Export | `ids[]`, `exportType` |
| Sort / Paging | `sortField`, `order` |

Output `param_samples.json`: `[{ "path", "method", "action": "search", "body", "query", "headers" }]`

### J7. Confidence Scoring Rules

| Confidence | Evaluation Criteria |
|---|---|
| **High** | Static callsite + $\ge$ 2 matching runtime samples |
| **Medium** | Static callsite only, or single runtime sample |
| **Low** | Inferred from response/error reversal without secondary confirmation |
| **Pending Trigger** | Known static field where UI/permissions were not triggered |

### J8. Scenario Quick Reference

| Scenario | Execution Sequence |
|---|---|
| REST List Views | J1 Request Assembler → J6 Four-stage diff → J3 Validation rules |
| Create / Edit Forms | J3 Form names → J4 Submit chain → Runtime submission + intentionally omitting fields to observe 400 errors |
| GraphQL | J2 Variables declarations → Runtime variable capture across operations |
| Encrypted Bodies | J5 Hook function inputs → Pre-encryption payload reveals genuine parameters |

### J9. Mapping to api-recon Phases

| api-recon Phase | Parameter Recon Action |
|---|---|
| Phase 1 Static | J1 Anchor window expansion |
| Phase 2 A2 Interceptor | Global injected fields (`tenantId`, `sign`) |
| Phase 3 Runtime | J6 Trigger matrix + `param_samples.json` |
| Phase 4 Permission Tree | Distinct forms per module → full parameter surface requires appropriate permissions |
| Phase 5 Merging | `params_merged.json` + confidence scoring; avoid single-sample classification |

### J10. Troubleshooting

| Symptom | Resolution |
|---|---|
| Static fields never appear at runtime | Mark as "pending trigger"; reconstruct permission tree / click advanced filters / cycle through select options |
| Identical path with varying body schemas | Normal — record separately by `action`; do not force conflicting schemas together |
| Stub responses are fake but real parameters needed | **Inspect outbound requests** (body/headers); do not infer parameters from stub responses |
| 400 error reports nested field failure | Inspect outer wrappers like `data`, `bizData`, or `variables` |
| Only GraphQL operation name is visible | Inspect expanded `variables` JSON; search statically for `$var: Type` |

---
