# EVE-Cyno Data API: terms of use

Last updated: 2026-10-15

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
- The tools that read live game data from EVE's ESI (market price, type info, sovereignty, system activity) answer from a shared cache and have a lower limit per caller. Do not use them to mirror or bulk-collect ESI data: use ESI itself or a dataset such as EVE Ref for that. When they answer `503 upstream_busy`, the shared ESI budget is spent; wait for `Retry-After`.
- Do not scrape the API to rebuild the community-fit corpus, and do not write automation that works around the access tiers (public, key).
- Do not use the service for botting, game-client modification, or real-money trading of in-game assets. Use that breaks the EVE Developer License Agreement is not allowed.
- Do not attack the service or try to read data you are not meant to reach.

## Fenris Creations and the EVE Developer License Agreement

The game data belongs to Fenris Creations. Your use of what you get here must comply with the [EVE Developer License Agreement](https://developers.eveonline.com/license-agreement). This service is non-commercial, and what you build on it should stay within what that agreement allows.

## API keys

Some tools need an API key. Keys are personal to you, are not to be shared or resold, and may be revoked at any time, for example for abuse. Keyed callers are rate limited per key.

## Your own keys (Janice)

For Janice appraisals you can send your own Janice API key in the `X-Janice-Key` header. It is forwarded to Janice for that one request only and is never stored or logged by this service. The project's own Janice key is never used for you. Without it, `appraise_items` answers with an estimate from ESI average prices.

## Privacy

- The service writes one access-log line per request with: request ID, HTTP method, matched route, status, response size and duration. It does not log your IP address, API key, Janice key, query string or request body. On tool and MCP requests, and on a refused request over a key's daily quota, the line also carries the tool name (REST) and an opaque caller token: a salted hash of your IP address or API key id, which cannot be turned back into either and is used only to spot a client that sends far more than the others. A failed tool call also logs the tool name and the error text.
- Your IP address is held in memory only, to apply the rate limit. Data kept for an API key is described below. The service sits behind Cloudflare, which sees traffic as any proxy does under its own terms.
- Logs go to the operator's log store and are kept for a limited time (30 days).
- Free-text you send to some tools (for example a community-fit search query) is forwarded to the upstream provider that answers it (an embedding provider, ESI, Janice), as each tool's description states.
- The data API sets no cookies and runs no analytics. An account is needed only to create an API key: sign in with EVE Online or Discord on ai.eve-cyno.dev, or run /apikey in our Discord server. An account is created when you first sign in or first use our Discord bot, and your sign-ins can be linked into one account, so your keys and limits are the same everywhere. In our account store, the service keeps for an account an id of its own, its creation time and, for chat testers, when the tester role was last seen; for each linked sign-in, it keeps the EVE character id or Discord user id, the date it was linked and, for an EVE character, the owner hash EVE sends with every login (it changes when the character is transferred; the link is removed when the new owner signs in). The account store keeps nothing else about your characters or your Discord account. Unlinking a sign-in removes it from the account.
- Discord sign-ins go through our sign-in service (Authentik). It keeps a user record with your Discord user id, your Discord username, the Discord access token from your last sign-in and, if Discord shares it, your e-mail address. At each sign-in on ai.eve-cyno.dev it uses that token to read your current Discord avatar from Discord. It also keeps a sign-in event log with your username, e-mail address and IP address for 30 days; these events expire on their own and are not removed when you delete your account. EVE Online sign-ins do not go through Authentik.
- On ai.eve-cyno.dev, your browser loads EVE character portraits from EVE Online's image server (images.evetech.net) and your Discord avatar from Discord (cdn.discordapp.com), as other EVE community sites do; those servers see your IP address. The Discord avatar's id is kept only in your sign-in cookie.
- You can delete your account in Settings on ai.eve-cyno.dev. This deletes your linked logins, your keys and their history at once and, for a Discord login, your user record in our sign-in service (the operator's own accounts are kept). A blocked account cannot delete itself; ask on Discord. Deleting does not reset today's question counter, which resets at 00:00 UTC. The access you granted our app on Discord stays until you remove it in Discord (Settings → Authorized Apps).
- For each API key the service stores the key id, a hash of the key (never the key), your account id and display name, the key name, created and last-used dates, and daily request counts per endpoint for 30 days. Revoking a key stops it at once; ask on Discord and the operator deletes your key records. If an account is blocked for abuse, its account id or sign-in ids (EVE character id, Discord user id) and display name stay on a block list until it is unblocked, also after the account is deleted.

## Contact

The EVE-Cyno Discord, https://discord.gg/XZzkH38dt9, for questions, feedback, support, abuse reports and takedown
requests (including from fit authors). In game: Bythlak.

## Changes

I may change these terms or the service at any time; the date above shows the latest version. If you keep using the service after a change, you accept it.
