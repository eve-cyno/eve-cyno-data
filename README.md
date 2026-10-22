[![CI](https://github.com/eve-cyno/eve-cyno-data/actions/workflows/ci.yml/badge.svg)](https://github.com/eve-cyno/eve-cyno-data/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/eve-cyno.dev/go/data.svg)](https://pkg.go.dev/eve-cyno.dev/go/data)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

# EVE-Cyno core: deterministic EVE Online data and tools

`core` is the part of EVE-Cyno that has no language model in it. Given the same inputs it gives the same answers, and every
answer names its sources. It offers:

- **SDE lookups** over a local SQLite build of Fenris Creations' Static Data Export: items, ships, skills, stations, routes, reprocessing,
  production chains, solar-system search and details (security, neighbours, celestials, stations, ice belts), jump distances and
  jump-drive route planning (hops, light-years, fuel, gate jumps) for any jump-capable hull or an Ansiblex chain.
- **Fit validation and statistics** from the Gofa dogma engine: slots, CPU / powergrid, DPS, tank, capacitor, navigation.
- **ESI pass-throughs**: market prices and history, daily average prices, sovereignty, system activity, killmail battles; plus live
  Thera and Turnur wormhole connections from EvE-Scout.
- **Community fits**: search over a vector index of fits published by community sites, with the source and licence on every hit.

Everything is one tool registry (`catalog`) published three ways, so the descriptions never drift apart:

| Surface | Where | Notes |
|---|---|---|
| REST `/v1` + OpenAPI 3.1 | [`api/openapi.yaml`](api/openapi.yaml); a running server serves `GET /v1/openapi.yaml` and `/v1/openapi.json` | the document of a running server describes exactly the endpoints it serves |
| MCP | stdio: `cmd/mcp`; streamable HTTP: `POST /v1/mcp` of `cmd/dataapi` | stateless, JSON responses |
| `llms.txt` | [`llms.txt`](llms.txt), [`llms-full.txt`](llms-full.txt); served at `/llms.txt`, `/llms-full.txt` | the same information in prose |

A hosted instance runs at `https://data.eve-cyno.dev` (anyone can try the public tools without a key, on trial limits: a few requests a minute and 200 a day per client; a free API
key is for real use, with higher limits, the keyed tool and free-text fit search; `appraise_items` needs no key (an estimate from ESI average prices) and takes your own `X-Janice-Key` for a Janice appraisal). Its portal at
[`https://data.eve-cyno.dev/`](https://data.eve-cyno.dev/) (JSON index with `Accept: application/json`) links the developer docs under
[`/docs/`](https://data.eve-cyno.dev/docs/): getting started, the Claude Code plugin, MCP, REST, one page per tool (27 public,
1 keyed), access tiers and limits, attribution. The pages are generated from the tool registry and the route table (templates in
[`dataapi/portal/`](dataapi/portal/)); the example responses are recorded from the live service. The
[terms of use](https://data.eve-cyno.dev/terms) (source in [`dataapi/terms.md`](dataapi/terms.md)) apply to it.
Everything below uses `http://localhost:8092`, a server
you run yourself.

## Quick start (SDE only, no other services)

You need Go 1.27 and a built SDE file. The SDE is Fenris Creations data converted by Fuzzwork; it is not shipped with this code, so you
build it once with `cmd/sde` (about 440 MB on disk, about half a minute on a normal connection).

```sh

# 1. Build the SDE (needs internet access to fuzzwork.co.uk). The destination is -out,
#    else EVE_CORE_SDE_PATH, else data/sde/sde.sqlite.
go run ./cmd/sde -out data/sde/sde.sqlite

# 2. Run the data API without the fit corpus, with the tool endpoint and the MCP endpoint on.
export EVE_CORE_SDE_PATH=$PWD/data/sde/sde.sqlite
DATAAPI_SDE_ONLY=true DATAAPI_EXPOSE_TOOL_API=true DATAAPI_EXPOSE_MCP=true go run ./cmd/dataapi
```

The server listens on `:8092`. Check it: `GET /v1/health` answers `{"status":"ok","sde":true,...}`.

**SDE-only mode.** `DATAAPI_SDE_ONLY=true` builds the server without a fit-corpus client: Qdrant is never contacted.
Everything that reads the SDE works: `/v1/items/search`, `/v1/fits/detail`, `/v1/fit/stats`, `/v1/fit/suggest` (ranked from the
SDE alone), the 17 public tools that run on the SDE alone (`plan_jump_route` among them: it needs network only for `avoid_incursions` and `live_info`), the live ESI and EvE-Scout tools, `get_average_prices` and `appraise_items` (they also work, with network) and the OpenAPI and `llms.txt` routes and the MCP endpoint. `GET /v1/fits/search` answers
`503 unavailable`, `/v1/health` reports `"corpus": false`, and `get_fits` / `list_fits` answer that the corpus is not
configured. Without the variable the server expects Qdrant: if it is configured but down, the process still starts, fit search
answers `502 upstream_error` per request, and `/v1/health` probes Qdrant (`GET /collections/<name>`, 1 second timeout, the verdict
cached for 30 seconds and shared by concurrent requests): `"corpus"` is true only when the corpus is configured and answered,
`"corpus_configured"` says whether a corpus is wired at all. Health stays HTTP 200 either way; a down corpus shows in the body.
The stdio server (`cmd/mcp`) has the same switch: `-sde-only` or `MCP_SDE_ONLY=true`.

**Refreshing the SDE.** Run `go run ./cmd/sde` again after each game patch: it checks Fuzzwork's build id (kept in
`<out>.build`) and rebuilds only when it changed, publishing the new file atomically; `-force` rebuilds anyway. A running
server notices the replaced file and reopens it (`EVE_CORE_SDE_RELOAD_INTERVAL`). A failed build leaves the old file in place
and exits 1.

### Docker

The root `Dockerfile` builds this module into a small distroless image (about 45 MB, runs as a
non-root user, `EXPOSE 8092`, and `EXPOSE 8093` for the key-store admin API, which a compose file should publish on host
loopback only). The image ships an empty `/data/keys` owned by the non-root user, so a fresh named volume mounted there is
writable for the key store. The entrypoint is `/dataapi`; the SDE builder is a second binary, `/sde`. Build from the
repository root:

```sh
docker build --build-arg VERSION=dev -t eve-cyno-dataapi .

# 1. Build the SDE into a named volume (needs internet access; --user 0:0 because a fresh volume is root-owned).
docker run --rm --user 0:0 -v sde_data:/data/sde --entrypoint /sde eve-cyno-dataapi

# 2. Serve it read-only, SDE-only, with the tool endpoint on.
docker run --rm -p 8092:8092 -v sde_data:/data/sde:ro \
  -e DATAAPI_SDE_ONLY=true -e DATAAPI_EXPOSE_TOOL_API=true eve-cyno-dataapi
```

The image sets `EVE_CORE_SDE_PATH=/data/sde/sde.sqlite` and `DATAAPI_ADDR=:8092`. There is no `HEALTHCHECK` (no shell or curl in
the image); probe `GET /v1/health` from outside. Refresh the SDE after a patch by re-running step 1.

## Tools and tiers

Every tool has one tier, enforced identically on REST and MCP:

| Tier | Tools | Who may run it |
|---|---|---|
| public (27) | `get_jumps_between`, `get_jump_distances` (jump counts from one system to up to 200 others), `plan_jump_route` (jump-drive routes: hops, light-years, gate jumps, fuel), `find_systems` (search systems by security, location, celestials, stations, ice belts, distance and jump range), `get_system_info` (up to 50 systems at once), `get_systems_in_region`, `get_npc_stations`, `get_reprocessing_yield`, `get_required_skills`, `get_production_chain`, `find_canonical_module`, `search_item_by_name`, `get_ship_stats`, `get_hull_facts`, `get_ship_bonuses`, `compute_fit_stats`, `validate_fitting`; community fits: `get_fits`, `list_fits` (a payload-filtered corpus scroll, no embedding call); live ESI data: `get_market_price`, `get_market_history` (price and volume by day or week, up to 90 days), `get_type_info`, `get_sovereignty`, `get_system_activity`; live EvE-Scout data: `get_thera_connections` (Thera and Turnur wormhole connections, optionally nearest to a system); prices and appraisal: `get_average_prices` (ESI's daily average prices of up to 200 items; one shared snapshot, not a live call), `appraise_items` (an estimate from ESI average prices; your own Janice key in `X-Janice-Key`, optional, makes it a Janice appraisal, used for that request only, never logged or stored) | anyone (on a service that issues keys, without one the limits below are trial limits; the live upstream tools have a lower per-caller limit) |
| keyed (1) | `analyze_battle` | a valid API key |
| disabled (1) | `convert_isk_to_real` | nobody on a public surface |

Arguments and result types of every tool are in [`llms-full.txt`](llms-full.txt).

**Live upstream tools.** Six tools read public (unauthenticated) EVE ESI routes: `get_market_price`, `get_market_history`,
`get_type_info`, `get_sovereignty`, `get_system_activity` and `plan_jump_route`, which goes live only when called with
`avoid_incursions` (ESI `/incursions`) or `live_info` (ESI `/sovereignty/systems`, `/universe/names`, `/universe/system_kills`, `/universe/system_jumps`); without
them it reads the SDE alone and spends nothing. `get_thera_connections` reads the EvE-Scout API (EvE-Scout / Signal Cartel,
<https://www.eve-scout.com/>). They answer from a shared in-memory cache that follows the upstream's `Expires`/`ETag`
(concurrent callers share one upstream request) and share one budget of `DATAAPI_UPSTREAM_TOOL_LIMIT` calls per minute per
caller without an API key (default 5; a caller with a key gets twice as many; REST and MCP together; 0 disables), counted
across all of them (one call is one unit, however many items or systems it carries) on top of the route limits. They never send a
`type_id` the SDE does not know, and keep `DATAAPI_ESI_GUARD_PERCENT`
(default 25) of every ESI rate-limit group unspent: below that a call is not sent and answers `503 upstream_busy` with `Retry-After`
(MCP: an error result starting `upstream_busy`). Every ESI call is logged as `esi_call` (no caller, no payload). Set
`ESI_CONTACT` so ESI can reach the operator. To take a live tool off the public tier, move it to keyed in
`catalog.tierTable`.

**Fit search and keys.** `GET /v1/fits/search` filters the corpus by payload fields and works without a key. With `q` it runs a
vector search (the query is embedded through a paid provider), so on a service that accepts keys `q` needs one: a keyless
request with a non-empty `q` is `401 api_key_required` (`401 invalid_api_key` for a key that is not valid).

### Authentication

Public tools need nothing. The keyed tier takes API keys from two sources, which can be used together:

- **A key file**, named by `DATAAPI_API_KEYS_FILE`: one `id:sha256hex` per line, `#` comments allowed. Only the SHA-256 of a key is stored.
- **A self-serve key store**, a SQLite file named by `DATAAPI_KEYS_DB` (created if missing). Keys look like `ecd_` plus 64 hex
  characters, are shown once when created and stored as a hash. One account (all its linked sign-in identities together) may hold
  3 active keys and may create at most 10 keys per UTC day; revoked key records are deleted after 30 days. Each key gets a
  daily request cap (`DATAAPI_KEY_DAILY_CAP`) and its requests are counted per endpoint for 30 days. The store is
  managed through the admin API below; the documents describe self-serve issuance only when the store is on.

An `ecd_` key the store does not hold is refused as invalid; any other key is checked against the file.

```sh
KEY=$(openssl rand -base64 32 | tr -d '=+/')
echo "me:$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)" >> keys.txt
DATAAPI_API_KEYS_FILE=keys.txt DATAAPI_EXPOSE_TOOL_API=true DATAAPI_EXPOSE_MCP=true go run ./cmd/dataapi
```

A client sends the key as `Authorization: Bearer <key>` or `X-API-Key: <key>`. With neither a key file nor a key store there
are no keys: keyed tools answer 401 and are left out of the documents and the MCP tool list. Such a service has nobody to give a
key to, so it serves everyone the keyed numbers of the table below, has no daily cap for clients and leaves `q` open.

#### Admin API (key store only)

With `DATAAPI_KEYS_DB` and `DATAAPI_ADMIN_TOKEN` (at least 32 characters) set, a second listener (`DATAAPI_ADMIN_ADDR`,
default `127.0.0.1:8093`) serves the admin API that a front end (a web app, a chat bot) calls to issue, list and revoke keys on
behalf of signed-in users. It is for a private network only: never publish its port or route it through a tunnel (it refuses
requests that carry `CF-Connecting-IP`). Every request needs `Authorization: Bearer <admin token>`. Owner routes take an
`X-User-ID` header: an account id (`acct:` and 16 hex digits), or a legacy `discord:<id>` (acts for its linked account) or `eve:<id>` (acts only on keys held under that exact id; a create still counts the whole account's caps and blocks):

| Route | Does |
|---|---|
| `GET /admin/v1/keys`, `POST /admin/v1/keys`, `DELETE /admin/v1/keys/{id}` | list, create (optional `X-User-Name`, percent-encoded) and revoke the owner's keys |
| `GET`/`POST /admin/v1/ops/keys` | operator: the active keys plus the block list (`?status=revoked`: the keys revoked in the last 30 days instead, with `revoked_at` and `revoked_by`); issue a key for any owner (exempt from the per-day create limit) |
| `PATCH`/`DELETE /admin/v1/ops/keys/{id}` | operator: change the daily cap; revoke any key (optional `X-Actor`) |
| `GET /admin/v1/ops/keys/{id}/usage` | operator: request history (`?days=1..30`) |
| `GET /admin/v1/ops/usage` | operator: day history of every key with usage (`?days=1..30`; `{"days","keys":{id:[day rows]}}`) |
| `PUT`/`DELETE /admin/v1/ops/owners/{owner}/block` | operator: block an owner and revoke their keys (optional `X-Actor`); unblock |
| `DELETE /admin/v1/ops/owners/{owner}` | operator: delete all of an owner's key records, usage and block-list entry (`200 {"owner","keys_deleted"}`) |
| `POST /admin/v1/accounts/resolve` | the account of a sign-in identity (created on first use; an EVE identity must carry its owner hash; `sign_in: true` after a fresh login moves a character that changed hands; optional `tester` evidence for Discord) |
| `POST /admin/v1/accounts/link`, `/unlink` | attach a freshly verified identity to the caller's account (merging accounts and their keys) / detach one |
| `POST /admin/v1/accounts/delete` | delete the caller's account with its identities and every key row (active and revoked) and usage of its owner set, at once (`{"keys_deleted","identities"}`; `403 owner_blocked` for a blocked account) |
| `DELETE /admin/v1/ops/accounts/{account}` | operator: delete an account the same way; a blocked account's block stays on its sign-in ids (`{"account","keys_deleted","identities"}`) |

Account answers carry `blocked: true` while the account or one of its sign-in ids is blocked.

The account routes are for trusted services only: the caller (a web front end or a bot) has verified the identity it names and is the only source of the identity fields. The operator key list carries the linked `identities` of each account owner. For an identity the store keeps only the provider, the id, the EVE owner hash and the link date; for an account its creation time and when tester evidence was last seen.

Revoked keys are kept for 30 days with their usage. `revoked_by` is the role that revoked the key, never a person:
`owner` (the owner route), `admin` or `operator` (an operator route; the caller sends `X-Actor: admin` for an admin
acting in its panel, `operator` or nothing otherwise; any other value is `400 invalid_actor` and changes nothing), or
`unknown` (revoked before the role was recorded). `X-Actor` is asserted by the holder of the admin token; the API
checks only that it is one of these values.

The admin API answers `429 create_limit` (with `Retry-After`) past the per-day create limit and `409 too_many_keys` past
the active-key limit. Secrets, tokens and owner ids are never logged.

Errors are always `{"error":{"code":"...","message":"..."}}`:

| Status | Code | Meaning |
|---|---|---|
| 401 | `api_key_required` | a keyed tool without a key |
| 401 | `invalid_api_key` | a key that is not valid (on any tool, public ones too, when the server has keys) |
| 403 | `key_not_permitted` | a valid key without the keyed allowance |
| 403 | `tool_disabled` | the tool is refused on purpose |
| 400 | `invalid_request` | malformed body or parameters; also a JSON-RPC batch sent to `POST /v1/mcp` (send one message per request) |
| 413 | `payload_too_large` | body over 256 KiB (fit routes) or 64 KiB (tool and MCP) |
| 429 | `rate_limited` | over the limit; wait `Retry-After` seconds |
| 429 | `daily_quota_exceeded` | a key used its daily cap; wait `Retry-After` seconds (until 00:00 UTC) |
| 502/503 | `upstream_error` / `unavailable` | an upstream (Qdrant, ESI) or a dependency is down |
| 503 | `upstream_busy` | the ESI budget is nearly spent and nothing was sent; wait `Retry-After` seconds |

Over MCP an invalid key is the same 401 before the protocol starts; a tool you may not use is simply not in `tools/list`.

### Rate limits

Per caller (per API key when one is sent, otherwise per client IP), sliding window, per minute. Without a key the service is for
trying it out; a free key is for real use (connecting Claude Code, a model or a bot) and tells the operator who sends what.
The numbers:

| Route | Without a key | With a key |
|---|---|---|
| `POST /v1/mcp` (an MCP session is a few requests plus one per tool call) | 20 | 120 |
| `POST /v1/tool/{name}` | 10 | 60 |
| `GET /v1/fits/search` | 30 | 240 |
| the live upstream tools (`get_market_price`, `get_market_history`, `get_type_info`, `get_sovereignty`, `get_system_activity`, `get_thera_connections`, and `plan_jump_route` only with `avoid_incursions` or `live_info`), one shared budget, REST and MCP together, on top of the rows above | 5 (`DATAAPI_UPSTREAM_TOOL_LIMIT`) | 10 |
| `GET /v1/items/search` | 120 | `DATAAPI_KEYED_LIMIT_FACTOR` times (default 2): 240 |
| `POST /v1/fits/detail`, `/v1/fit/stats`, `/v1/fit/suggest` | 60 | 120 |
| `GET /`, `/docs/*`, `/terms*`, `/v1/openapi.*`, `/llms*.txt` (the static files under `/assets/` are not limited) | 60 | 120 |

The keyed number of a route is `Rate.KeyedMax` in `dataapi.Limits` (`dataapi.DefaultLimits()` holds the table); a route without
one gets `DATAAPI_KEYED_LIMIT_FACTOR` times its limit. Daily caps, all in UTC days and answering `429 daily_quota_exceeded` with
`Retry-After` set to the seconds until 00:00 UTC:

- a client without a key: 200 requests a day (`DATAAPI_ANON_DAILY_CAP`, `0` = none), counted in memory per client IP, never persisted
  or logged. The documentation pages, the portal assets and `/health` are not counted, so browsing the docs never spends the trial.
  The table is bounded (100,000 clients a day; beyond that new clients are served uncounted and a warning is logged once a day);
- a key: 10,000 requests a day with a key store (`DATAAPI_KEY_DAILY_CAP`, default 10000, `0` = none).

The trial limits, the anonymous daily cap and the key-only `q` apply only on a service that accepts keys (a key file or a key
store); without one everyone gets the "With a key" column and no daily cap.

The limiter takes the client IP from Cloudflare's `CF-Connecting-IP` header and falls back to the TCP peer, so run the
service on loopback or behind a Cloudflare tunnel; do not expose it directly to the internet.

## Connect a client

Ready-to-copy files are in [`examples/`](examples/).

### Claude Desktop and Claude Code, local stdio

The stdio server needs no keys; it reads the SDE from `EVE_CORE_SDE_PATH` and registers the 27 public tools (the live ESI and EvE-Scout ones, `get_average_prices` and `appraise_items` need network; `appraise_items` runs without a Janice key there)
(`-all` or `MCP_EXPOSE_ALL=true` registers every tool, using the keys in your own environment: `JANICE_API_KEY` for Janice appraisals, `QDRANT_*`). `-sde-only` or
`MCP_SDE_ONLY=true` builds no Qdrant client at all, so Qdrant is never contacted and `get_fits` / `list_fits` answer that the
corpus is not configured; it is never inferred from a missing `QDRANT_URL`.

```sh
go build -o eve-cyno-mcp ./cmd/mcp
```

- Claude Desktop: add [`examples/claude-desktop.json`](examples/claude-desktop.json) to `claude_desktop_config.json` (use
  absolute paths) and restart the app.
- Claude Code:
  ```sh
  claude mcp add eve-cyno -e EVE_CORE_SDE_PATH=/absolute/path/to/sde.sqlite -- /absolute/path/to/eve-cyno-mcp
  ```
  Add `-s project` to write a shared `.mcp.json` instead (Claude Code asks for approval of project servers on first use).

### Claude Code, streamable HTTP

```sh
claude mcp add --transport http eve-cyno http://localhost:8092/v1/mcp
# with an API key for the keyed tier
claude mcp add --transport http eve-cyno http://localhost:8092/v1/mcp --header "Authorization: Bearer <YOUR_API_KEY>"
```

Or put [`examples/claude-code-http.mcp.json`](examples/claude-code-http.mcp.json) in your project as `.mcp.json`; it reads the
key from the `EVE_CYNO_API_KEY` environment variable. The server must run with `DATAAPI_EXPOSE_MCP=true`. Claude Desktop
connects to remote servers as custom connectors in its settings, which need a public HTTPS URL.

Other clients take the same URL. Codex: `codex mcp add eve-cyno --url http://localhost:8092/v1/mcp`; Gemini CLI:
`gemini mcp add -s user -t http eve-cyno http://localhost:8092/v1/mcp`. Cursor, VS Code and claude.ai are set up as on the hosted
service: see `/docs/mcp` of the running server.

### ChatGPT

A custom GPT Action imports `openapi.yaml`; see [`examples/chatgpt-action.md`](examples/chatgpt-action.md). It needs a public
HTTPS URL, so it does not work against `localhost`.

### A Python agent

[`examples/python/`](examples/python/) has a plain REST client with `httpx` and an MCP client with the official `mcp` SDK:

```sh
cd examples/python
uv run rest_example.py     # REST: tool call, fit detail, error handling, attribution
uv run mcp_example.py      # MCP over streamable HTTP: list tools, call one
```

Both read `EVE_CYNO_URL` (default `http://localhost:8092`) and an optional `EVE_CYNO_API_KEY`.

## curl cheat sheet

```sh
BASE=http://localhost:8092

curl -s $BASE/v1/health
curl -s "$BASE/v1/items/search?q=heavy+neutron&kind=module&limit=5"
curl -s "$BASE/v1/fits/search?ship=Megathron&activity=pve&limit=5"          # needs the fit corpus

curl -s -X POST $BASE/v1/fits/detail -H 'Content-Type: application/json' \
  -d '{"eft":"[Rifter, demo]\n200mm AutoCannon I\n1MN Afterburner I\n"}'
curl -s -X POST $BASE/v1/fit/stats   -H 'Content-Type: application/json' \
  -d '{"eft":"[Rifter, demo]\n200mm AutoCannon I\n1MN Afterburner I\n"}'

# a tool (needs DATAAPI_EXPOSE_TOOL_API=true); the answer is {tool, version, result:{text,data}, attribution}
curl -s -X POST $BASE/v1/tool/get_jumps_between -H 'Content-Type: application/json' \
  -d '{"from_system":"Jita","to_system":"Amarr"}'

# find systems: lowsec and below with an ice belt within 8 jumps of Jita, nearest first
curl -s -X POST $BASE/v1/tool/find_systems -H 'Content-Type: application/json' \
  -d '{"security":{"max":0.4},"has_ice_belt":true,"near":{"origin":"Jita","max_jumps":8},"limit":5}'

# plan a jump-drive route (SDE only unless avoid_incursions or live_info is set)
curl -s -X POST $BASE/v1/tool/plan_jump_route -H 'Content-Type: application/json' \
  -d '{"origin":"Amamake","destination":"1DQ1-A","ship":"Rhea","skills":{"jump_drive_calibration":4}}'

# free-text fit search, and any tool with a higher budget, need an API key
curl -s "$BASE/v1/fits/search?q=cheap+armor+gila" -H "Authorization: Bearer $EVE_CYNO_API_KEY"
curl -s -X POST $BASE/v1/tool/get_market_price -H "Authorization: Bearer $EVE_CYNO_API_KEY" \
  -H 'Content-Type: application/json' -d '{"type_id":34}'

# appraise_items: an estimate from ESI average prices; add your own Janice key for a Janice appraisal
curl -s -X POST $BASE/v1/tool/appraise_items -H 'Content-Type: application/json' -d '{"items":"Tritanium x1000"}'
curl -s -X POST $BASE/v1/tool/appraise_items -H "X-Janice-Key: $JANICE_KEY" \
  -H 'Content-Type: application/json' -d '{"items":"Tritanium x1000"}'

# MCP by hand (needs DATAAPI_EXPOSE_MCP=true; one JSON-RPC message per request)
curl -s -X POST $BASE/v1/mcp -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

curl -s $BASE/v1/openapi.yaml     # the API description
curl -s $BASE/llms.txt            # llms.txt index (also /llms-full.txt)
```

## Configuration

Read from the process environment; the commands do not read a `.env` file.

| Variable | Used by | Meaning |
|---|---|---|
| `EVE_CORE_SDE_PATH` | both | the SDE SQLite; default `data/sde/sde.sqlite` found upward from the working directory. The process cannot start without it |
| `EVE_CORE_SDE_RELOAD_INTERVAL` | both | how often to check for a replaced SDE file (Go duration; `off` disables) |
| `DATAAPI_ADDR` | dataapi | listen address, default `:8092` |
| `DATAAPI_EXPOSE_TOOL_API` | dataapi | `true` registers `POST /v1/tool/{name}` (default off) |
| `DATAAPI_EXPOSE_MCP` | dataapi | `true` mounts `POST /v1/mcp` (default off) |
| `DATAAPI_API_KEYS_FILE` | dataapi | API key file, see [Authentication](#authentication) |
| `DATAAPI_KEYS_DB` | dataapi | path of the self-serve key store (SQLite, created if missing); unset: key file only |
| `DATAAPI_KEY_DAILY_CAP` | dataapi | daily request cap copied into each new key (integer 0-10000000, default 10000, `0` = none) |
| `DATAAPI_KEYED_LIMIT_FACTOR` | dataapi | multiple of the route limits a keyed caller gets on the routes with no explicit keyed figure (integer 1-10, default 2); MCP, tools, fit search and the live-upstream budget have their own keyed numbers |
| `DATAAPI_ANON_DAILY_CAP` | dataapi | requests per UTC day for a client without an API key (integer 0-10000000, default 200, `0` = none); only where keys exist |
| `DATAAPI_UPSTREAM_TOOL_LIMIT` | dataapi | live-upstream calls per minute per caller without a key (default 5; a key gets twice as many; `0` = no extra limit) |
| `DATAAPI_ADMIN_TOKEN` | dataapi | bearer token of the admin API (at least 32 characters; never logged). Unset: no admin listener |
| `DATAAPI_ADMIN_ADDR` | dataapi | admin listen address, default `127.0.0.1:8093`; served only when the key store and the token are both set |
| `DATAAPI_PUBLIC_URL` | dataapi | the public origin, e.g. `https://data.eve-cyno.dev` (https, no path or query; invalid is a startup error). Makes the OpenAPI `servers` entry and the `llms*.txt` links absolute, which ChatGPT Actions need; unset keeps them relative |
| `DATAAPI_SDE_ONLY` | dataapi | `true` runs without the fit corpus, see [SDE-only mode](#quick-start-sde-only-no-other-services) |
| `MCP_EXPOSE_ALL` (or `-all`) | mcp | register every tool, for your own machine and keys |
| `MCP_SDE_ONLY` (or `-sde-only`) | mcp | `true` runs without the fit corpus, as `DATAAPI_SDE_ONLY` does |
| `EVE_CORE_QDRANT_URL`, `QDRANT_COLLECTION` | both | the fit corpus (default `http://localhost:6333`, `eve_knowledge`); fit search only |
| `DEEPINFRA_EMBED_BASE_URL`, `EMBEDDING_MODEL`, `DEEPINFRA_API_KEY`, `EMBED_QUERY_INSTRUCTION` | both | an OpenAI-compatible embedding endpoint for query embeddings; without a base URL an Ollama server (`EVE_CORE_OLLAMA_URL`, `EVE_CORE_EMBED_MODEL`) is used |
| `JANICE_API_KEY` | mcp `-all` | your Janice key: Janice appraisals instead of the ESI estimate over stdio |

The fit corpus itself is built by a separate ingest pipeline; this module only reads it.

## Development

This repository is the only source of the module; there is no upstream copy. See [`CONTRIBUTING.md`](CONTRIBUTING.md) for the checks
to run before a pull request.

## Attribution and licensing

Read this before you show answers to other people.

- **Fenris Creations.** EVE Online data (SDE, ESI) is used under the EVE Developer License Agreement; this software is
  non-commercial. Show this notice wherever you publish answers: "(c) 2014 Fenris Creations hf. All rights reserved. 'EVE',
  'EVE Online', 'Fenris Creations', and all related logos and images are trademarks or registered trademarks of Fenris Creations
  hf. This material is used with limited permission of Fenris Creations. No official affiliation or endorsement by Fenris
  Creations is stated or implied. Created under the EVE Developer License Agreement." The software does not modify the game
  client, automate gameplay or trade in-game assets for real money, and neither should what you build on it.
- **EVE University Wiki.** Some reference text is condensed from <https://wiki.eveuniversity.org/>, CC BY-SA 4.0. It was
  shortened and combined with other material and is not endorsed by EVE University; derived text stays CC BY-SA 4.0.
  The ice-belt list `icebelts/ice_systems.json` is extracted from four of its pages and stays CC BY-SA 4.0 (it is not
  covered by the MIT licence); results that use it name the source.
- **Community fits.** Fits belong to their authors and sites. Every hit carries an `attribution` object (`source`,
  `source_url`, `author` when known, `license`, and `credit` / `license_url` when the terms require them): keep the link back
  wherever you show a fit. A search returns at most 24 fits, ranked, with no paging: the service is not a way to dump a site's
  data.
- **Tool answers.** Each response lists its upstreams in `attribution` (REST) or `_meta["eve-cyno/attribution"]` plus a
  `Sources:` text line (MCP). The full list with licences is at the end of [`llms-full.txt`](llms-full.txt).

---

## Licence and notices

Code: [MIT](LICENSE), Copyright (c) 2026 Bythlak. EVE Online data and trademarks belong to Fenris Creations and are
used under the EVE Developer License Agreement; see [`NOTICE`](NOTICE) (including the required disclaimer, the EVE University Wiki
attribution, and the Pyfa validation note) and [`THIRD_PARTY.md`](THIRD_PARTY.md). Contributions: [`CONTRIBUTING.md`](CONTRIBUTING.md).
Vulnerabilities: [`SECURITY.md`](SECURITY.md).
