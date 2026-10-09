# Third-party software

Go module dependencies of `eve-cyno.dev/go/data` (every package in this repository, including the binaries under `cmd/`).
The attributions for EVE Online data, the EVE University Wiki and the Pyfa test oracle are in [`NOTICE`](NOTICE).

Checked with Go 1.27.2 from:

```sh
go list -deps -test -f '{{if not .Standard}}{{with .Module}}{{.Path}} {{.Version}}{{end}}{{end}}' ./...
```

Licences were read from the `LICENSE` files of each module version in the Go module cache. CI enforces the permissive
allowlist with `go-licenses check ./...`. Re-run the command and re-check this file whenever `go.mod` changes.

## Copyleft check

**No GPL, LGPL or AGPL dependency** is linked into these packages, at build time or in tests. For the avoidance of doubt:

- Pyfa/eos (GPL-3.0, partly LGPL) is **not a dependency**. It is a locally cloned test oracle only; no source is included or
  linked (see `NOTICE`).
- zKillboard (AGPL-3.0) is reached as a web API only; no zKillboard code is used.

## Modules linked into a build

| Module | Version | Licence (SPDX) | Copyright holder | Notes |
|---|---|---|---|---|
| `github.com/dustin/go-humanize` | v1.1.0 | MIT | Dustin Sallings | via `modernc.org/libc` |
| `github.com/google/jsonschema-go` | v0.4.3 | MIT | JSON Schema Go Project Authors | via the MCP SDK (`core/mcpserver`) |
| `github.com/google/uuid` | v1.6.0 | BSD-3-Clause | Google Inc. | |
| `github.com/modelcontextprotocol/go-sdk` | v1.8.0 | Apache-2.0 AND MIT | Model Context Protocol a Series of LF Projects, LLC; The Go MCP SDK Authors | the official MCP Go SDK (`core/mcpserver`, `core/cmd/mcp`). Its `LICENSE` carries both texts: the project is moving from MIT to Apache-2.0, new code is Apache-2.0 and contributions without relicensing consent stay MIT; documentation is CC-BY-4.0 and is not linked. No `NOTICE` file |
| `github.com/remyoudompheng/bigfft` | v0.0.0-20230129092748-24d4a6f8daec | BSD-3-Clause | The Go Authors | via `modernc.org/mathutil`, `modernc.org/memory` |
| `github.com/segmentio/asm` | v1.1.3 | MIT | Segment | via `segmentio/encoding` |
| `github.com/segmentio/encoding` | v0.5.4 | MIT | Segment.io, Inc. | via the MCP SDK (its JSON codec) |
| `github.com/yosida95/uritemplate/v3` | v3.0.2 | BSD-3-Clause | Kohei YOSHIDA | via the MCP SDK |
| `go.yaml.in/yaml/v3` | v3.0.5 | MIT AND Apache-2.0 | Canonical Ltd; Kirill Simonov (libyaml) | writes the generated `api/openapi.yaml` (`core/dataapi`); parts ported from libyaml (MIT), the rest Apache-2.0 with a Canonical `NOTICE` |
| `golang.org/x/oauth2` | v0.35.0 | BSD-3-Clause | The Go Authors | via the MCP SDK (the OAuth token types its transports mention); ships a `PATENTS` grant |
| `golang.org/x/sync` | v0.23.0 | BSD-3-Clause | The Go Authors | ships a `PATENTS` grant |
| `golang.org/x/sys` | v0.48.0 | BSD-3-Clause | The Go Authors | ships a `PATENTS` grant |
| `golang.org/x/time` | v0.15.0 | BSD-3-Clause | The Go Authors | via the MCP SDK; ships a `PATENTS` grant |
| `modernc.org/libc` | v1.77.1 | BSD-3-Clause | The Libc Authors | embeds Go (BSD-3-Clause), musl libc (MIT), go-netdb (MIT) and NixOS/nixpkgs (MIT) material; notices in its `LICENSE-3RD-PARTY.md` |
| `modernc.org/mathutil` | v1.7.1 | BSD-3-Clause | The mathutil Authors | |
| `modernc.org/memory` | v1.12.1 | BSD-3-Clause | The Memory Authors | also carries `LICENSE-GO` and `LICENSE-MMAP-GO` for code adapted from Go and `edsrzf/mmap-go` |
| `modernc.org/sqlite` | v1.60.1 | BSD-3-Clause | The Sqlite Authors | contains SQLite transpiled to Go (public domain). Only the driver package is imported (`modernc.org/sqlite`); the optional `sqlite-vec` (MIT) and `vfs` packages are not. Notices in its `LICENSE-3RD-PARTY.md` |

The Go standard library and toolchain are BSD-3-Clause (The Go Authors, https://go.dev/LICENSE).

## Modules used only by tests

| Module | Version | Licence (SPDX) | Notes |
|---|---|---|---|
| `github.com/stretchr/testify` | v1.12.1 | MIT | Mat Ryer, Tyler Bunnell and contributors |

## Redistribution

Source distribution needs no more than this file and `NOTICE`. A binary distribution of a build must reproduce the licence
text and copyright notice of every module in the first table (BSD-3-Clause and MIT require it); take the texts from each
module's `LICENSE` file at the version above, plus the files named in the Notes column. The BSD-3-Clause "no endorsement"
clause applies to the names of the copyright holders.
