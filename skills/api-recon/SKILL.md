---
name: api-recon
description: Call this skill when gathering and mapping website API endpoints.
---

# API Recon (Frontend Endpoint Reconnaissance)

Within **authorized scope**, discover as completely as possible: **backend APIs** (paths, HTTP methods, parameters, response bodies), **frontend routes**, and **UI interaction triggers** (tabs, dialogs, table operations, etc.).

---

## Boundaries & Prohibitions (Required Reading for Agent · Violations are Out of Scope)

This skill is **strictly for API and parameter surface reconnaissance**, not for vulnerability exploitation or penetration attack phases.

### Task Boundaries

| Scope | Permitted | Prohibited |
|---|---|---|
| **Objective** | Enumerate paths, methods, parameters, routes, UI triggers | SQLi/XSS/privilege escalation/brute-force/fuzzing, packet tampering, destructive actions |
| **Authentication** | Hook + stub/mock to bypass **client-side** login gates | Requesting or guessing credentials from users; submitting real login forms |
| **Runtime** | Hook APIs without credentials, using mock responses to let SPA reach post-login shell | Workflows strictly dependent on active backend sessions to proceed |

### Uncredentialed Dynamic Analysis (Default Phase 3 Strategy)

1. Intercept and **stub** login, permissions, menus, and other bootstrap endpoints via `preload.js` / `runtime_harvest.js`;
2. Return mock bodies with **valid schema, business success code, and empty data arrays** for business query APIs;
3. Enable the frontend to render post-login views even in a backendless or 401 environment, thereby triggering further XHR/fetch/WebSocket traffic;
4. **Empty data, blank tables, and placeholder UI elements are expected**—do not pivot to real credentialed login or vulnerability testing because of them.

**In brief**: Use mocks to unfold frontend routes and component mounting, **recording only outbound requests**. What the backend returns does not matter; what matters is **which endpoints the frontend attempts to call**.

### Hard Prohibitions

| Prohibited Action | Alternative Approach |
|---|---|
| Grepping/curling/Reading main entry `index-*.js` to extract API paths before Phase 1 is done | Run `OUTDIR/harvest_static.py` |
| Manually writing replacement scripts like `extract_apis.py` instead of harvest | Modify `OUTDIR/harvest_static.py` and rerun |
| Repeating a failed grep/command $\ge$ 2 times | Switch strategy: check tool_logs, tune harvest, consult reference |
| Bypassing Gate A/B and running original scripts from `scripts/` directly | Copy to OUTDIR and customize for target |
| Real username/password, OTP, OAuth authentication | Stub/mock (see above) |
| Skipping stubbing to execute auth-bypass/injection tests under the guise of "getting real data" | Record outbound only; stay within recon boundaries |
| Deletions, sensitive data exports, batch writes, or other irreversible actions | Applies equally to coverage clicking |
| Claiming all pages and endpoints are discovered before completing runtime + dynamic enumeration | See "Definition of Done" or document limitations |
| Claiming all parameters are identified before completing parameter matrix + diff | Phase 3b matrix + Phase 5 diff |
| Inferring required vs. optional fields from a single runtime sample | Multi-sample diff or validation rules / error reversal |

---

## Two-Layer Model + Execution Modes

| Layer | Output | Upper Bound |
|---|---|---|
| **Static** (JS bundles) | Complete endpoint paths, route drafts, packet field candidates | No HTTP methods; parameters require Phase 1b; misses runtime-concatenated URLs |
| **Runtime** (Live session) | Methods + bodies + responses + dynamic URLs + WS/SSE; multi-sample diff completes parameters | Pages must actually render to fire requests; single sample insufficient to determine required/optional |

| Execution Mode | Engine | Best For |
|---|---|---|
| **depth** | `runtime_harvest.js` (Puppeteer) | API inventories, METHOD/params/response bodies, WS/SSE, reproducible batch runs |
| **coverage** | browser + `preload.js` | Clicking tabs/modals/tables, deeper functional coverage |
| **both** | depth first, then coverage | Most comprehensive, longest duration |

**Parameter Methodology** (no universal one-liner script): Paths via harvest/regex; parameters via **anchor window expansion + UI binding chain + multi-sample diff + error reversal** (grep recipes in [reference.md](reference.md) Section J).

---

## Definition of Done

Reconnaissance is considered complete only when all criteria are satisfied:

- [ ] **Static**: Phase 1 harvest produces `api_static.txt`, `routes.txt`, `js/`
- [ ] **Runtime**: At least one of depth or coverage completed; coverage/both must have **Hooks active + dynamic enumeration loop**
- [ ] **Shell Reached**: Visiting business paths does not redirect to `/login` (mind hash routes)
- [ ] **Parameters**: coverage/both completes parameter trigger matrix + `param_samples.json`; Phase 5 merges `params_merged.json`
- [ ] **Depth** (if module pages are blank): Phase 4 permission tree reconstructed and rerun until **module-level APIs** appear (not just locale/bootstrap)
- [ ] **Delivery**: Phase 5 artifacts complete (see Phase 5 table); `insert_assets` bulk inserts all discovered service and endpoint assets

---

## Scripts & Gates

The files in `scripts/` are reference templates only; **running originals directly as final deliverables is strictly prohibited**.

**Rule**: Read first → adapt for target → write to `OUTDIR` (e.g. `recon/`) → record in `CHANGES.md`; if incompatible, rewrite following methodology using the structure as a blueprint.

| Gate | Timing | Reference Script → OUTDIR Copy | Common Required Modifications |
|---|---|---|---|
| **A (Static)** | After Phase 0, before running harvest/spider **for the first time** | `harvest_static.py` / `spider_mpa.py` | **Default regex works directly for most sites**; adjust only when manifest/dialect mismatches: endpoint regex, webpack/Vite `publicPath`, MPA exclude/cookie |
| **B (Runtime)** | After Phase 2, before running depth/coverage | `runtime_harvest.js` / `preload.js` + `config.json` | Cookie/localStorage keys, neutralize success values, stubs, login regex, api prefix, hash/history |

**Mandatory SPA Sequence** (cannot be swapped; phase ordering takes precedence over "ad-hoc exploration before scripts"):

| Step | Must Do | Prohibited |
|---|---|---|
| After Phase 0 completion | Next command: `python3 OUTDIR/harvest_static.py <URL> OUTDIR` | curl/grep/Read main entry `index-*.js` (typically >500KB) |
| Gate A | Copy script → minor adjustments as needed → **run immediately** | Manually extracting APIs before deciding whether to harvest |
| Before Phase 1 completion | `wc -l` validation of output; retry harvest on 404s | Handcrafting extract scripts; repeatedly grepping undownloaded URLs |
| From Phase 1b onward | grep restricted to `OUTDIR/js/*.js` | Grepping main bundle instead of harvest outputs |

- [x] Copy `harvest_static.py` → (optional) tune regex → **run immediately**
- [ ] curl main bundle → grep multiple times → write temporary extractor → run harvest at the end
- **MPA**: After Phase 0, the next bash command is `python3 OUTDIR/spider_mpa.py ...`

---

## Tooling & Output Constraints

| Constraint | Description |
|---|---|
| Large files | Files >100KB (`index-*.js`) must **never** be Read or grepped into context; process via OUTDIR batch scripts |
| grep output | Must append `| head -20` or `-m 5`; conversation should only contain path summaries, never raw bundle dumps |
| Verification | Use `wc -l`, `ls | wc -l`; do not Read entire directories |
| Regex exploration | Optional, $\le$ 1 time, only on small chunks $\le$ 50KB or HTML; formal static recon relies on harvest |
| Reference | Consult [reference.md](reference.md) for recipes, templates, and troubleshooting; do not dump reference inline |

---

## Execution Roadmap

```
Phase 0 Classification + OUTDIR
  → Gate A → Phase 1 harvest (★ RUN IMMEDIATELY ★)
  → Phase 1b Parameter Reverse Engineering
  → Phase 2 Three Authentication Gates → config.json
  → Gate B → Phase 3 Runtime + Parameter Matrix
  → Phase 4 Permission Tree (if needed) → Rerun Phase 3
  → Phase 5 Merged Report + insert_assets bulk insert of all discovered services and endpoints
```

Check off in order; **do not proceed to the next Phase until the current one is completed**.

1. [ ] **Phase 0**: Initial SPA/MPA survey; create `OUTDIR` → [Phase 0](#phase-0--classification)
2. [ ] **Gate A + Phase 1**: Copy scripts → **immediately** harvest → verify with `wc -l` → [Phase 1](#phase-1--static)
3. [ ] **Phase 1b**: Anchor expansion + binding layer → `param_candidates.json` → [Phase 1b](#phase-1b--parameter-reverse-engineering)
4. [ ] **Phase 2**: Three authentication gates → `config.json` → [Phase 2](#phase-2--three-authentication-gates)
5. [ ] **Gate B**: Adjust runtime scripts → [Phase 3](#phase-3--runtime)
6. [ ] **Phase 3**: depth / coverage / both; confirm shell reached; parameter matrix → `param_samples.json`
7. [ ] **Phase 4** (if required): Permission tree → patch stubs → rerun Phase 3 → [Phase 4](#phase-4--permission-tree-recovery)
8. [ ] **Phase 5**: Merge outputs + report + `insert_assets` → [Phase 5](#phase-5--merging--reporting)

---

## Phase 0 — Classification

Fetch entry HTML, **create `OUTDIR`** (do not modify files inside `scripts/`):

- **SPA**: Empty shell + `<div id=app>` + chunks → Phase 1–5
- **MPA**: SSR + `<form>`, no endpoint bundles → After Gate A:

```bash
python3 recon/spider_mpa.py <BASE_URL> <OUTDIR> [--cookie "session=..."] [--max 300] [--depth 5] [--exclude "logout|delete|destroy"]
```

Outputs `forms.txt`, `links.txt`, `api_inline.txt`. If SPA forms $\approx$ 0 → switch to Phase 1.

---

## Phase 1 — Static

Follow [Scripts & Gates](#scripts--gates) and [Tooling & Output Constraints](#tooling--output-constraints).

```bash
python3 recon/harvest_static.py <BASE_URL> <OUTDIR>
```

Harvest: parse HTML scripts → resolve webpack/Vite manifest → download all lazy chunks → output `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt`.

```bash
wc -l OUTDIR/api_static.txt OUTDIR/routes.txt
ls OUTDIR/js | wc -l
```

- Chunk count vs manifest: 404 errors require tuning harvest and retrying; do not curl individual chunks by hand.
- If `api_static.txt` is too sparse → loosen endpoint regex in OUTDIR copy and rerun (see reference).

### Phase 1b — Parameter Reverse Engineering

Paths originate from Phase 1; parameter fields must be investigated separately. For grep rules, see [Tooling & Output Constraints](#tooling--output-constraints).

**Completion Criteria**: For critical endpoints, identify: field name, transport location, inferred data type, required status, sample values, and confidence score.

#### 1b.0 — Transport Mechanisms

| Mechanism | Parameter Location | Primary Static Search Vector |
|---|---|---|
| REST JSON | body + query | Around path anchor: `(params\|data\|body)\s*:\s*\{` |
| GraphQL | `variables` | gql templates, `$page: Int` |
| Traditional Form | urlencoded | `<form>`, `FormData` |
| File Upload | multipart | `FormData.append` |
| Path Parameters | `/user/:id` | Route tables + `useParams` / `$route.params` |
| Encrypted / Signed | Wrapped in `sign`/`data` | Hook encryption function inputs (reference Section D) |

Output: Annotate each endpoint with `transport: query|json|form|graphql|encrypted`.

#### 1b.1 — Anchor Window Expansion

Using known paths as anchors, expand the search window to identify request-building objects:

```bash
grep -n '"/api/user/list"' OUTDIR/js/*.js | head -20
grep -rhoaE '.{0,120}("/api[^"]+").{0,200}' OUTDIR/js/*.js | head -20
grep -rhoaE '(params|data|body|payload)\s*:\s*\{' OUTDIR/js/*.js | head -20
```

| Wrapper Layer | Parameter Clues |
|---|---|
| Axios instance | `data` / `params` |
| Unified request client | Interceptor injection of global fields |
| OpenAPI client | Generated method signatures |
| React Query / SWR | Hook second parameter |
| Vue composable | Composable function arguments |

Schema residues: `yup`/`zod`/rules, `Form.Item name=`, embedded Swagger specifications.

→ `param_candidates.json`: `{ path, fields[], source: "static-callsite", confidence }`

#### 1b.2 — UI Binding Layer

```
Form field → onFinish/handleSubmit → transform → API payload
```

| Binding Source | Discovery Method |
|---|---|
| Form submission | Follow submit handler → payload transform → API call |
| Table search | `getFieldsValue()` → `params` |
| Routing | `:id` / `?tab=` |
| Interceptors | Global `tenantId`, pagination, sign |
| Select dropdowns | `options` array → API enum values |

Use DevTools call stack from `fetch` / `XHR.send` upward to trace assembly functions.

#### 1b.3 — Assembly Questions (Distinct from Phase 2 Auth Gates)

| Question | Verification Target |
|---|---|
| **Assembly** | Where payload is built, transform traces |
| **Validation** | required, pattern, enum definitions |
| **Transport** | path / query / body / multipart / headers |

The interceptor gate (Phase 2) also reveals globally injected fields (Authorization, `X-Tenant-Id`, signature tokens).

#### 1b.4 — Handoff to Phase 3

Field candidates originate from static/binding analysis; **required/optional flags and conditional dependencies** must be finalized through Phase 3 parameter matrices + diffing + Phase 5 error reversal.

---

## Phase 2 — Three Authentication Gates

Search within `OUTDIR/js/` (using `head`), populating `config.json` (see reference for recipes):

| Gate | Question | Keywords |
|---|---|---|
| **Render Gate** | How is logged-in status checked? | `isLogin`, `getToken`, Cookie/localStorage |
| **Interceptor Gate** | What triggers redirection to `/login`? | `response_code`, `errno`, axios interceptor |
| **Content Gate** | Where do menus/permissions originate? | `menu`, `permission`, `role`, `acl`, `routes` |

Never assume a localStorage key name constitutes valid credentials—confirm from chunks and request chains.

**Exit Gate = Gate B**: Consolidate findings into `config.json`, and adapt `OUTDIR/runtime_harvest.js` / `preload.js`.

### Phase 2b — API Observation (Optional)

Use `preload.js` in OUTDIR to observe session keys, Authorization headers, and nested API URLs:

| Configuration | Output |
|---|---|
| `recordDetail: true` | `__API_RECON_DETAIL__` |
| `observe.xhrHeaders: true` | Request headers log |
| `extractUrlsFromResponse: true` | Nested APIs inside responses |
| `observe.storageReads/cookieReads: true` | Feed back to config |
| `neutralizeVueRouter: true` | `__API_RECON_ROUTES__` |

Coverage round export: `__API_RECON_LOG__`, `__API_RECON_DETAIL__`, `__API_RECON_ROUTES__`, `__API_RECON_OBSERVE__`.

---

## Phase 3 — Runtime

Prerequisites: Gate B passed; strictly comply with [Boundaries & Prohibitions](#boundaries--prohibitions-required-reading-for-agent--violations-are-out-of-scope) and mock strategy.

Set `"runtimeMode": "depth" | "coverage" | "both"` in `config.json` (templates in reference).

### Hooks & Stubs (Shared by depth + coverage)

| Level | Scope | Objective |
|---|---|---|
| L1 Exact | auth/permission/bootstrap stub | Pass initial landing auth check |
| L2 Negative Correction | All JSON responses | Rewrite unauthenticated status code → success |
| L3 Fallback Catch-all | Unmatched `/api` endpoints | Empty success body to allow UI to mount |

- **depth**: Fake auth + `forward` rewritten status codes + `stubs`; traverse `routes` (hash/history); outputs `runtime_api.json`.
- **coverage**: Inject `preload.js` at **document-start** (via CDP `addScriptToEvaluateOnNewDocument` or Userscript).

Verification: `window.__API_RECON_PRELOAD__` exists; navigating to business paths does not redirect back to `/login`.

```bash
cd recon && npm install
node runtime_harvest.js config.json
```

### 3b — coverage Dynamic Enumeration (Mandatory)

1. Main navigation / sidebar — click each entry, wait 1–3s for network activity
2. Tabs — `role=tab`, `.ant-tabs-tab`
3. Data tables — view / edit / details on top row
4. Toolbars — export, filter, create (**avoid destructive deletions**)
5. On entering each module — merge APIs and routes
6. SPA — controlled `pushState` for routes in `routes.txt` not yet traversed (prohibited on MPA)

**Parameter Trigger Matrix** (Mandatory): Trigger each action type per module, **diffing multi-sample outputs**:

| Operation | Typical Additional Parameters |
|---|---|
| Default list view | Pagination + default filters |
| Search click | keyword, filter |
| Advanced filtering | Additional optional fields |
| Create / Edit | Full entity schema |
| Batch / Export / Sort | `ids[]`, `exportType`, `sortField` |

**Outbound bodies and headers remain genuine even under stubbing**—record what the client transmits. Log to `scan_raw.json`, `param_samples.json`, `api_detail.json`.

- **Vue**: `neutralizeVueRouter: true` + document-start preload
- **React**: `routes.txt` + sidebar clicks + `pushState`
- **both**: 3a depth first, followed by 3b coverage

---

## Phase 4 — Permission Tree Recovery

**Trigger**: Module views render blank / every route triggers only bootstrap calls (e.g. locale) → Content gate not satisfied.

| Symptom | Implication |
|---|---|
| Shell reached | Render gate + Interceptor gate cleared |
| Sidebar missing items / blank on click | Stub schema or permission codes incomplete |
| Every route calls identical minimal APIs | Failed `v-if permission` directive checks |
| `routes.txt` contains far fewer routes than bundle | Must reconstruct from auth/permission module |

```bash
grep -rhoaE '"/api[^"]*(permission|perm|role|menu|acl)[^"]*"' OUTDIR/js/*.js | sort -u | head -30
grep -rhoaE 'userRouteAuth|getResultTree|routeMap|routeLink|menuList|authList' OUTDIR/js/*.js | head -20
```

Typical chain: `role_permissions` (flat codes) + `permissions/all` (tree) → `getResultTree` → `userRouteAuth[CODE].url`.

```bash
python3 recon/extract_route_map.py recon/js recon/
python3 recon/build_perm_tree.py recon/js recon/ --config recon/config.json
```

Intermediate artifacts: `route_map.json`, `userRouteAuth.json`, `permissions_tree.json`, `*_stub.json`, `perm_codes_all.txt`.

Stub inspection: outer `response_code` matches interceptor gate expectations; flat codes align with tree nodes; `routes` encompasses all links from `route_map`.

Update `config.json` and **rerun Phase 3**. For large SPAs, tune `waitUntil`, `routeTimeout`, `perRouteMs` (see reference Sections A3/I).

---

## Phase 5 — Merging & Reporting

### Output Artifacts

| File | Phase | Content |
|---|---|---|
| `js/`, `api_static.txt`, `routes.txt`, `chunkmap.txt` | 1 | Static bundles and paths |
| `param_candidates.json` | 1b | Static parameter field candidates |
| `config.json` | 2 | Three gates + runtime settings |
| `runtime_api.json` | 3a | Depth detailed capture (including WS/SSE) |
| `param_samples.json`, `scan_raw.json`, `api_detail.json` | 3b | Multi-samples, click logs, detail traces |
| `route_map.json`, etc. | 4 | Permission tree intermediate files (if run) |
| `params_merged.json` | 5 | Consolidated parameters + confidence scores |
| `api_merged.txt` | 5 | `METHOD /path [params] [static\|runtime\|both]` |
| `site_map.json` | 5 | Routes, APIs, parameters, features, limitations |
| **insert_assets** | 5 | Write all discovered services and endpoints to asset store |

### 5b — Parameter Consolidation

Diff across `param_samples.json`; **there is no generic merge script**. For confidence guidelines, see reference Section J7 (high/medium/low/pending-trigger).

### 5c — Error Reversal

Within authorized scope, issuing incomplete requests to observe 400 responses (**purely for parameter recon, not vulnerability testing**): e.g. `field 'x' is required`, enum validation errors. Account for `data` wrappers, `variables`, and pre-encryption `bizData`.

Reports must document: runtimeMode, static vs runtime API counts, parameter confidence breakdown, unreached modules, and a summary of `CHANGES.md` relative to reference templates.

Suggested `site_map.json` structure:

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

For additional fields and grep recipes, see [reference.md](reference.md).

---

## General Notes

- **Framework Agnostic**: Webpack, Vite, and Angular lazy loading methodologies are identical.
- **Protocols**: REST/JSON, GraphQL, WebSocket, SSE; gRPC-web is out of scope.
- **SSR**: Client-side fetches can be logged; RSC and Server Actions are not fully enumerable.
- **Blind Spots**: JSVMP, WASM, strict HMAC/mTLS checks → rely on static analysis + document limitations.
- **Parameter Blind Spots**: Complex conditional dependencies, hidden parameters, WASM builders → classify as "pending trigger" or "unreachable".
- **Static Analysis is the Safety Net**: When runtime execution is blocked, static recon still successfully extracts endpoints.

---

## Additional Resources

- Grep recipes, `config.json` templates, troubleshooting, hooks, parameter reverse engineering (Section J), site_map templates: **[reference.md](reference.md)**
- Reference script paths: see [Scripts & Gates](#scripts--gates) table.
