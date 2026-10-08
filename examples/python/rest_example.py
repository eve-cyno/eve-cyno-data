"""Call the EVE-Cyno REST API with httpx.

    uv run rest_example.py

EVE_CYNO_URL      server root (default http://localhost:8092)
EVE_CYNO_API_KEY  optional API key, sent as `Authorization: Bearer ...`
EVE_CYNO_JANICE_KEY  optional, your own Janice key for appraise_items (`X-Janice-Key`)

The server needs DATAAPI_EXPOSE_TOOL_API=true for the /v1/tool/{name} calls.
"""

import os
import sys
from typing import Any

import httpx

BASE_URL = os.environ.get("EVE_CYNO_URL", "http://localhost:8092").rstrip("/")
API_KEY = os.environ.get("EVE_CYNO_API_KEY", "")


class ApiError(Exception):
    """A non-2xx answer. Every error body is {"error": {"code", "message"}}."""

    def __init__(self, status: int, code: str, message: str, retry_after: str | None = None):
        super().__init__(f"{status} {code}: {message}")
        self.status, self.code, self.retry_after = status, code, retry_after


def request(client: httpx.Client, method: str, path: str, **kwargs: Any) -> Any:
    resp = client.request(method, path, **kwargs)
    if resp.is_success:
        return resp.json()
    try:
        err = resp.json()["error"]
    except (ValueError, KeyError):
        err = {"code": "unknown", "message": resp.text[:200]}
    raise ApiError(resp.status_code, err["code"], err["message"], resp.headers.get("Retry-After"))


def main() -> None:
    headers = {"Authorization": f"Bearer {API_KEY}"} if API_KEY else {}
    with httpx.Client(base_url=BASE_URL, headers=headers, timeout=30) as client:
        # 1. A public tool: the {tool, version, result: {text, data}, attribution} envelope.
        env = request(client, "POST", "/v1/tool/get_jumps_between",
                      json={"from_system": "Jita", "to_system": "Amarr"})
        print(env["result"]["text"].splitlines()[0])
        for src in env["attribution"]:  # licence condition: show these wherever you show the answer
            print(f"  source: {src['name']} ({src['license']})")

        # 2. Fit detail: parse an EFT block against the SDE.
        eft = "[Rifter, demo]\n200mm AutoCannon I\n1MN Afterburner I\n"
        detail = request(client, "POST", "/v1/fits/detail", json={"eft": eft})
        print(f"fit: {detail['ship_name']} ({detail['ship_class']}), valid={detail['valid']}")

        # 3. Community fits need the fit corpus (Qdrant). Every hit carries its attribution.
        try:
            found = request(client, "GET", "/v1/fits/search", params={"ship": "Megathron", "limit": 3})
            for hit in found["hits"]:
                a = hit["attribution"]
                print(f"  hit from {a['source']}: {a.get('source_url', '')} [{a['license']}]")
        except ApiError as e:
            print(f"fit search unavailable ({e}); the SDE-based calls above do not need it")

        # 4. The tier errors, on purpose.
        for tool, body, extra in [
            ("get_fits", {"ship": "Megathron"}, {}),                      # keyed tool, no key
            ("appraise_items", {"items": "Tritanium"}, {}),               # byo-key, no X-Janice-Key
            ("convert_isk_to_real", {}, {}),                              # disabled
            # A key that is not in the server's key file is refused on every tool, public ones
            # included (only when the server has a key file at all).
            ("search_item_by_name", {"names": ["Tritanium"]}, {"Authorization": "Bearer wrong"}),
        ]:
            try:
                request(client, "POST", f"/v1/tool/{tool}", json=body, headers=extra)
                print(f"{tool}: ok")
            except ApiError as e:
                # 401 api_key_required / invalid_api_key, 403 key_not_permitted / tool_disabled,
                # 400 janice_key_required, 429 rate_limited (wait Retry-After seconds).
                print(f"{tool}: {e}")

        # 5. A malformed request is a 400 invalid_request.
        try:
            request(client, "GET", "/v1/items/search", params={"q": "x"})
        except ApiError as e:
            print(f"items/search: {e}")


if __name__ == "__main__":
    try:
        main()
    except httpx.ConnectError:
        sys.exit(f"cannot reach {BASE_URL}; is cmd/dataapi running?")
    except ApiError as e:  # e.g. 401 invalid_api_key when EVE_CYNO_API_KEY is wrong
        sys.exit(f"request failed: {e}")
