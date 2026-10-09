[![CI](https://github.com/eve-cyno/eve-cyno-data/actions/workflows/ci.yml/badge.svg)](https://github.com/eve-cyno/eve-cyno-data/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/eve-cyno.dev/go/data.svg)](https://pkg.go.dev/eve-cyno.dev/go/data)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

# EVE-Cyno core: deterministic EVE Online data and tools

`core` is the part of EVE-Cyno that has no language model in it. Given the same inputs it gives the same answers, and every
answer names its sources. It offers:

- **SDE lookups** over a local SQLite build of CCP's Static Data Export: items, ships, skills, stations, routes, reprocessing,
  production chains.
- **Fit validation and statistics** from the Gofa dogma engine: slots, CPU / powergrid, DPS, tank, capacitor, navigation.
- **ESI pass-throughs**: market prices, sovereignty, system activity, killmail battles.
- **Community fits**: search over a vector index of fits published by community sites, with the source and licence on every hit.

Everything is one tool registry (`catalog`) published three ways, so the descriptions never drift apart:

| Surface | Where | Notes |
|---|---|---|
| REST `/v1` + OpenAPI 3.1 | [`api/openapi.yaml`](api/openapi.yaml); a running server serves `GET /v1/openapi.yaml` and `/v1/openapi.json` | the document of a running server describes exactly the endpoints it serves |
| MCP | stdio: `cmd/mcp`; streamable HTTP: `POST /v1/mcp` of `cmd/dataapi` | stateless, JSON responses |
| `llms.txt` | [`llms.txt`](llms.txt), [`llms-full.txt`](llms-full.txt); served at `/llms.txt`, `/llms-full.txt` | the same information in prose |

A hosted instance runs at `https://data.eve-cyno.dev` (R3.5 stage 1: public tier only, rate limited per IP, no API keys yet;
keyed tools answer 401, `appraise_items` needs your own `X-Janice-Key`). Its index page at
[`https://data.eve-cyno.dev/`](https://data.eve-cyno.dev/) lists every entry point (JSON with `Accept: application/json`), and
the [terms of use](https://data.eve-cyno.dev/terms) (draft, source in [`dataapi/terms.md`](dataapi/terms.md)) apply to it.
Everything below uses `http://localhost:8092`, a server
you run yourself.

## Quick start (SDE only, no other services)

You need Go 1.27 and a built SDE file. The SDE is CCP data converted by Fuzzwork; it is not shipped with this code, so you
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
SDE alone), the 13 public tools, the OpenAPI and `llms.txt` routes and the MCP endpoint. `GET /v1/fits/search` answers
`503 unavailable`, `/v1/health` reports `"corpus": false`, and `get_fits` / `list_fits` answer that the corpus is not
configured. Without the variable the server expects Qdrant: if it is configured but down, the process still starts, fit search
answers `502 upstream_error` per request, and `/v1/health` probes Qdrant (`GET /collections/<name>`, 1 second timeout, the verdict
cached for 30 seconds and shared by concurrent requests): `"corpus"` is true only when the corpus is configured and answered,
`"corpus_configured"` says whether a corpus is wired at all. Health stays HTTP 200 either way; a down corpus shows in the body.
The stdio server (`cmd/mcp`) has the same switch: `-sde-only` or `MCP_SDE_ONLY=true`.

**Refreshing the SDE.** Run `go run ./cmd/sde` again after each game patch: it checks Fuzzwork's build id (kept in
`<out>.build`) and rebuilds only when it changed, publishing the new file atomically; `-force` rebuilds anyway. A running
server notices the replaced file and reopens it (`EVE_CORE_SDE_RELOAD_INTERVAL`). A failed build leaves the old file in place
and exits 1. In the monorepo, `go -C ingest run ./cmd/ingest sde` runs the same builder with its own watermark in `ingest.db`.

### Docker

`deploy/Dockerfile.dataapi` builds only this module (`GOWORK=off`) into a small distroless image (about 45 MB, runs as a
non-root user, `EXPOSE 8092`). The entrypoint is `/dataapi`; the SDE builder is a second binary, `/sde`. Build from the
repository root:

```sh
docker build -f deploy/Dockerfile.dataapi --build-arg VERSION=dev -t eve-cyno-dataapi .

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
| public (13) | `get_jumps_between`, `get_systems_in_region`, `get_npc_stations`, `get_reprocessing_yield`, `get_required_skills`, `get_production_chain`, `find_canonical_module`, `search_item_by_name`, `get_ship_stats`, `get_hull_facts`, `get_ship_bonuses`, `compute_fit_stats`, `validate_fitting` | anyone |
| keyed (7) | `get_fits`, `list_fits`, `get_market_price`, `get_type_info`, `get_sovereignty`, `get_system_activity`, `analyze_battle` | a valid API key |
| byo-key (1) | `appraise_items` | anyone who sends their **own** Janice key in the `X-Janice-Key` header (used for that request only, never logged or stored) |
| disabled (1) | `convert_isk_to_real` | nobody on a public surface |

Arguments and result types of every tool are in [`llms-full.txt`](llms-full.txt).

### Authentication

Public tools need nothing. For the keyed tier, a server reads API keys from the file named by `DATAAPI_API_KEYS_FILE`: one
`id:sha256hex` per line, `#` comments allowed. Only the SHA-256 of a key is stored.

```sh
KEY=$(openssl rand -base64 32 | tr -d '=+/')
echo "me:$(printf %s "$KEY" | sha256sum | cut -d' ' -f1)" >> keys.txt
DATAAPI_API_KEYS_FILE=keys.txt DATAAPI_EXPOSE_TOOL_API=true DATAAPI_EXPOSE_MCP=true go run ./cmd/dataapi
```

A client sends the key as `Authorization: Bearer <key>` or `X-API-Key: <key>`. Without a key file there are no keys: keyed
tools answer 401 and are left out of the documents and the MCP tool list.

Errors are always `{"error":{"code":"...","message":"..."}}`:

| Status | Code | Meaning |
|---|---|---|
| 401 | `api_key_required` | a keyed tool without a key |
| 401 | `invalid_api_key` | a key that is not valid (on any tool, public ones too, when the server has keys) |
| 403 | `key_not_permitted` | a valid key without the keyed allowance |
| 403 | `tool_disabled` | the tool is refused on purpose |
| 400 | `janice_key_required` | `appraise_items` without `X-Janice-Key` |
| 400 | `invalid_request` | malformed body or parameters |
| 413 | `payload_too_large` | body over 256 KiB (fit routes) or 64 KiB (tool and MCP) |
| 429 | `rate_limited` | over the limit; wait `Retry-After` seconds |
| 502/503 | `upstream_error` / `unavailable` | an upstream (Qdrant, ESI) or a dependency is down |

Over MCP an invalid key is the same 401 before the protocol starts; a tool you may not use is simply not in `tools/list`.

### Rate limits

Per caller (per API key when one is sent, otherwise per client IP), sliding window, per minute:

| Route | Limit |
|---|---|
| `GET /v1/fits/search`, `GET /v1/items/search` | 120 |
| `POST /v1/fits/detail`, `/v1/fit/stats`, `/v1/fit/suggest` | 60 |
| `POST /v1/tool/{name}` | 30 |
| `POST /v1/mcp` (an MCP session is a few requests plus one per tool call) | 60 |
| `GET /`, `/terms*`, `/v1/openapi.*`, `/llms*.txt` | 60 |

The limiter takes the client IP from Cloudflare's `CF-Connecting-IP` header and falls back to the TCP peer, so run the
service on loopback or behind a Cloudflare tunnel; do not expose it directly to the internet.

## Connect a client

Ready-to-copy files are in [`examples/`](examples/).

### Claude Desktop and Claude Code, local stdio

The stdio server needs no network and no keys; it reads the SDE from `EVE_CORE_SDE_PATH` and registers the 13 public tools
(`-all` or `MCP_EXPOSE_ALL=true` registers every tool, using the keys in your own environment: `JANICE_API_KEY`, `QDRANT_*`). `-sde-only` or
`MCP_SDE_ONLY=true` builds no Qdrant client at all, so Qdrant is never contacted and `get_fits` / `list_fits` answer that the
corpus is not configured; it is never inferred from a missing `QDRANT_URL`.

```sh
go -C core build -o eve-cyno-mcp ./cmd/mcp
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

# a keyed tool
curl -s -X POST $BASE/v1/tool/get_market_price -H "Authorization: Bearer $EVE_CYNO_API_KEY" \
  -H 'Content-Type: application/json' -d '{"type_id":34}'

# your own Janice key for appraise_items
curl -s -X POST $BASE/v1/tool/appraise_items -H "X-Janice-Key: $JANICE_KEY" \
  -H 'Content-Type: application/json' -d '{"items":"Tritanium"}'

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
| `DATAAPI_PUBLIC_URL` | dataapi | the public origin, e.g. `https://data.eve-cyno.dev` (https, no path or query; invalid is a startup error). Makes the OpenAPI `servers` entry and the `llms*.txt` links absolute, which ChatGPT Actions need; unset keeps them relative |
| `DATAAPI_SDE_ONLY` | dataapi | `true` runs without the fit corpus, see [SDE-only mode](#quick-start-sde-only-no-other-services) |
| `MCP_EXPOSE_ALL` (or `-all`) | mcp | register every tool, for your own machine and keys |
| `MCP_SDE_ONLY` (or `-sde-only`) | mcp | `true` runs without the fit corpus, as `DATAAPI_SDE_ONLY` does |
| `EVE_CORE_QDRANT_URL`, `QDRANT_COLLECTION` | both | the fit corpus (default `http://localhost:6333`, `eve_knowledge`); fit search only |
| `DEEPINFRA_EMBED_BASE_URL`, `EMBEDDING_MODEL`, `DEEPINFRA_API_KEY`, `EMBED_QUERY_INSTRUCTION` | both | an OpenAI-compatible embedding endpoint for query embeddings; without a base URL an Ollama server (`EVE_CORE_OLLAMA_URL`, `EVE_CORE_EMBED_MODEL`) is used |
| `JANICE_API_KEY` | mcp `-all` | your Janice key for `appraise_items` over stdio |

The fit corpus itself is built by the `ingest` module of the monorepo; `core` only reads it.

## Attribution and licensing

Read this before you show answers to other people.

- **CCP / Fenris Creations.** EVE Online data (SDE, ESI) is used under the EVE Developer License Agreement; this software is
  non-commercial. Show this notice wherever you publish answers: "(c) 2014 Fenris Creations hf. All rights reserved. 'EVE',
  'EVE Online', 'Fenris Creations', and all related logos and images are trademarks or registered trademarks of Fenris Creations
  hf. This material is used with limited permission of Fenris Creations. No official affiliation or endorsement by Fenris
  Creations is stated or implied. Created under the EVE Developer License Agreement." The software does not modify the game
  client, automate gameplay or trade in-game assets for real money, and neither should what you build on it.
- **EVE University Wiki.** Some reference text is condensed from <https://wiki.eveuniversity.org/>, CC BY-SA 4.0. It was
  shortened and combined with other material and is not endorsed by EVE University; derived text stays CC BY-SA 4.0.
- **Community fits.** Fits belong to their authors and sites. Every hit carries an `attribution` object (`source`,
  `source_url`, `author` when known, `license`, and `credit` / `license_url` when the terms require them): keep the link back
  wherever you show a fit. A search returns at most 24 fits, ranked, with no paging: the service is not a way to dump a site's
  data.
- **Tool answers.** Each response lists its upstreams in `attribution` (REST) or `_meta["eve-cyno/attribution"]` plus a
  `Sources:` text line (MCP). The full list with licences is at the end of [`llms-full.txt`](llms-full.txt).

---

## Licence and notices

Code: [MIT](LICENSE), Copyright (c) 2026 Bythlak. EVE Online data and trademarks belong to CCP Games / Fenris Creations and are
used under the EVE Developer License Agreement; see [`NOTICE`](NOTICE) (including the required disclaimer, the EVE University Wiki
attribution, and the Pyfa validation note) and [`THIRD_PARTY.md`](THIRD_PARTY.md). Contributions: [`CONTRIBUTING.md`](CONTRIBUTING.md).
Vulnerabilities: [`SECURITY.md`](SECURITY.md).
