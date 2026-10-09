# EVE-Cyno Data API: terms of use

Last updated: 2026-10-08

## What this is

The EVE-Cyno Data API is a free, hobby-run service by Bythlak that gives developers and AI agents deterministic EVE Online data: static-data lookups, fit statistics and a community-fit search, over REST and MCP. It is not an official EVE Online or CCP service.

## No warranty, no SLA

The service is provided as is, for free, with no warranty and no uptime promise. Data can be wrong or out of date. It may change, be rate limited, or stop at any time, with or without notice. Do not build anything you cannot afford to lose on it.

## Attribution

- EVE Online, its game data and trademarks belong to Fenris Creations hf (the game is developed by CCP Games). Wherever you publish answers from this service, show the Fenris Creations notice from the [home page](/) (it is also in [llms-full.txt](/llms-full.txt)).
- Some reference text is condensed from the [EVE University Wiki](https://wiki.eveuniversity.org/) and is licensed [CC BY-SA 4.0](https://creativecommons.org/licenses/by-sa/4.0/). Keep the attribution, and share derived text under the same licence.
- Community fits belong to their authors and sites. Every fit carries an `attribution` object: keep its source link and credit wherever you show the fit. Do not bulk-copy fits and do not republish them as a dataset.
- Every tool answer names its upstream sources in `attribution`; keep that with the data.

## Acceptable use

- Respect the rate limits. A refused request is a `429` with `Retry-After`; wait instead of retrying in a loop or rotating addresses.
- Do not scrape the API to rebuild the community-fit corpus, and do not write automation that works around the access tiers (public, key, your own Janice key).
- Do not use the service for botting, game-client modification, or real-money trading of in-game assets. Use that breaks the EVE Developer License Agreement is not allowed.
- Do not attack the service or try to read data you are not meant to reach.

## Fenris Creations and the EVE Developer License Agreement

The game data belongs to Fenris Creations. Your use of what you get here must comply with the [EVE Developer License Agreement](https://developers.eveonline.com/license-agreement). This service is non-commercial, and what you build on it should stay within what that agreement allows.

## API keys

Some tools need an API key. Keys are personal to you, are not to be shared or resold, and may be revoked at any time, for example for abuse. Keyed callers are rate limited per key.

## Your own keys (Janice)

For price appraisals you send your own Janice API key in the `X-Janice-Key` header. It is forwarded to Janice for that one request only and is never stored or logged by this service. The project's own Janice key is never used for you.

## Privacy

- The service writes one access-log line per request with: request ID, HTTP method, matched route, status, response size and duration. It does not log your IP address, API key, Janice key, query string or request body. A failed tool call also logs the tool name and the error text.
- Your IP address (or your API key id) is held in memory only, to apply the rate limit. The service sits behind Cloudflare, which sees traffic as any proxy does under its own terms.
- Logs go to the operator's log store and are kept for a limited time (30 days).
- Free-text you send to some tools (for example a community-fit search query) is forwarded to the upstream provider that answers it (an embedding provider, ESI, Janice), as each tool's description states.
- There are no cookies, no accounts and no analytics.

## Contact

Bythlak, bythlak@eve-cyno.dev. Report abuse, takedown requests (including from fit authors) and questions there.

## Changes

I may change these terms or the service at any time; the date above shows the latest version. If you keep using the service after a change, you accept it.
