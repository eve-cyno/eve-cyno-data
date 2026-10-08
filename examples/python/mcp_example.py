"""Talk to the EVE-Cyno MCP endpoint (streamable HTTP) with the official `mcp` SDK.

    uv run mcp_example.py

EVE_CYNO_URL      server root (default http://localhost:8092)
EVE_CYNO_API_KEY  optional API key: unlocks the keyed tools in tools/list
"""

import asyncio
import os

import httpx2  # installed with `mcp`; the SDK takes an httpx2.AsyncClient for custom headers
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client

BASE_URL = os.environ.get("EVE_CYNO_URL", "http://localhost:8092").rstrip("/")
API_KEY = os.environ.get("EVE_CYNO_API_KEY", "")


async def main() -> None:
    headers = {"Authorization": f"Bearer {API_KEY}"} if API_KEY else {}
    async with httpx2.AsyncClient(headers=headers, timeout=30) as http:
        async with streamable_http_client(f"{BASE_URL}/v1/mcp", http_client=http) as streams:
            async with ClientSession(streams[0], streams[1]) as session:
                init = await session.initialize()
                print(f"server: {init.server_info.name} {init.server_info.version}")

                # Which tools you see depends on your tier: anonymous callers get the public ones.
                tools = (await session.list_tools()).tools
                print(f"{len(tools)} tools: {', '.join(t.name for t in tools)}")

                result = await session.call_tool(
                    "get_jumps_between", {"from_system": "Jita", "to_system": "Amarr"}
                )
                if result.is_error:  # a tool failure is a result, not an exception
                    raise SystemExit(f"tool error: {result.content[0].text}")
                print(result.content[0].text)
                # content[1] is a "Sources:" line; the structured form is in _meta.
                for source in (result.meta or {}).get("eve-cyno/attribution", []):
                    print(f"source: {source['name']} ({source['license']})")


if __name__ == "__main__":
    asyncio.run(main())
