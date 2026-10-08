# Security policy

## Reporting a vulnerability

Please report security problems privately through GitHub: open the **Security** tab of this repository and choose
**Report a vulnerability** (private vulnerability reporting). Do not open a public issue or pull request for a vulnerability.

Include what you found, how to reproduce it, and the version or commit. We aim to acknowledge a report within a few days and
will tell you when a fix is released. This is a volunteer, non-commercial project, so there is no bounty.

## Scope

The code in this repository: the data API (`cmd/dataapi`), the MCP server (`cmd/mcp`), the SDE builder (`cmd/sde`), and the
libraries behind them. Issues in a deployment you run yourself (reverse proxy, API-key handling, exposed ports) are
configuration questions rather than vulnerabilities, but we are happy to improve the defaults and documentation.

## Supported versions

Only the latest release and the `main` branch receive fixes.
