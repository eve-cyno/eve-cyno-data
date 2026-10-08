# ChatGPT: use the API as a custom GPT Action

A custom GPT Action calls your API from OpenAI's servers, so it needs a **public HTTPS URL**.
`http://localhost:8092` cannot work. A hosted endpoint is planned (roadmap R3.5) but is not live; until then, run
`cmd/dataapi` on a host you control behind HTTPS (a reverse proxy or a tunnel) and use that URL below.

1. Start the service with the tool endpoint on, because the OpenAPI document describes only what the process serves:
   ```sh
   DATAAPI_EXPOSE_TOOL_API=true go -C core run ./cmd/dataapi
   ```
2. In ChatGPT: **Explore GPTs -> Create -> Configure -> Actions -> Create new action**.
3. **Import from URL**: `https://<your-host>/v1/openapi.yaml`. If the import cannot fetch it, open that URL, copy the
   YAML and paste it into the schema box.
4. The document's `servers` entry is the relative `/v1`. The editor needs an absolute URL: change it to
   `https://<your-host>/v1`.
5. **Authentication**:
   - **None** for the public tools (13 of them: SDE lookups, route planning, fit validation and stats).
   - **API Key -> Bearer** with your key to also use the keyed tools (`get_fits`, `list_fits`, `get_market_price`, ...).
     The key is stored by ChatGPT in the Action's settings, never in the schema. The server accepts it as
     `Authorization: Bearer <key>` (keys are listed in the file named by `DATAAPI_API_KEYS_FILE`).
   - `appraise_items` needs a per-request `X-Janice-Key` header with the caller's own Janice key; a GPT Action cannot
     supply that safely, so leave it out of the Action.
6. Suggested instruction for the GPT: "Use the Action for every EVE Online number (jumps, fit stats, item data); do not
   guess. When an answer lists sources, name them." The responses carry an `attribution` array; CCP's licence and the
   community-fit sources require it to be shown.

Limits to keep in mind: the tool endpoint is rate limited to 30 requests per minute per caller, and an Action is easier
for the model to use with fewer operations; delete the ones you do not need in the schema editor.
