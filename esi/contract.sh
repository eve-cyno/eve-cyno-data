#!/usr/bin/env bash
# ESI contract check (roadmap R2.10).
#
#   core/esi/contract.sh check    diff live ESI against the committed snapshots; exit 1 on any change
#   core/esi/contract.sh update   refresh the snapshots from live ESI (review the git diff, then commit)
#
# Snapshots (core/esi/testdata/):
#   compatibility-dates.json  GET /meta/compatibility-dates, pretty-printed (newest date first)
#   openapi.yaml              GET /meta/openapi.yaml sent with X-Compatibility-Date: <DefaultCompatibilityDate>,
#                             i.e. the exact contract the client is pinned to
#
# A change means CCP released a new compatibility date, raised the minimum date or edited the
# pinned spec. Read the diff, re-check every ESI caller (core/tools, ingest/intel), then either
# bump DefaultCompatibilityDate in core/esi/config.go or just accept the new snapshots.
#
# The ingest intel collector follows the same pin (there is no collector-specific date any more);
# `check` additionally verifies that every route its pollers (ingest/intel/pollers.go) request
# still exists in the pinned spec. (/sovereignty/structures, which the old collector pin kept
# alive, is gone from 2026-05-19 on; the collector reads /sovereignty/systems.)
# No secrets are needed: all endpoints are public. Needs bash, curl, jq, diff.
set -euo pipefail

mode="${1:-check}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../.." && pwd)"
snap="$here/testdata"
base="${ESI_BASE_URL:-https://esi.evetech.net}"
pin="$(sed -n 's/^[[:space:]]*DefaultCompatibilityDate[[:space:]]*=[[:space:]]*"\([0-9-]*\)".*/\1/p' "$here/config.go" | head -n1)"
[ -n "$pin" ] || { echo "cannot read DefaultCompatibilityDate from $here/config.go" >&2; exit 2; }
intel_routes="$(sed -n 's/^.*path: "\(\/[^"]*\)".*$/\1/p' "$repo/ingest/intel/pollers.go")"
[ -n "$intel_routes" ] || { echo "cannot read the polled routes from $repo/ingest/intel/pollers.go" >&2; exit 2; }
ua="EVE-Cyno-contract-check/1 (+https://eve-cyno.dev)"

fetch() { # fetch <path> [extra curl args]: GET with retries, failing on HTTP errors
  local path="$1"; shift
  curl --fail --silent --show-error --location --retry 4 --retry-delay 5 --max-time 90 \
    -A "$ua" "$@" "$base$path"
}

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fetch /meta/compatibility-dates | jq . > "$work/compatibility-dates.json"
fetch /meta/openapi.yaml -H "X-Compatibility-Date: $pin" > "$work/openapi.yaml"

# Sanity: the pinned date must still be served, and the pinned spec must be that version.
if ! jq -e --arg d "$pin" '.compatibility_dates | index($d) != null' "$work/compatibility-dates.json" >/dev/null; then
  echo "::error::pinned compatibility date $pin is no longer listed by ESI (minimum date raised?)"
  pinned_missing=1
fi
if ! grep -q "^  version: \"$pin\"" "$work/openapi.yaml"; then
  echo "::error::the spec fetched for X-Compatibility-Date $pin reports a different info.version"
  pinned_missing=1
fi

# The intel collector shares the pin: every route its pollers request must exist in the spec
# fetched for it (paths sit at two spaces of indentation in the YAML).
intel_problems=""
intel_count=0
for route in $intel_routes; do
  intel_count=$((intel_count + 1))
  grep -qxF "  $route:" "$work/openapi.yaml" \
    || intel_problems="${intel_problems}route $route polled by ingest/intel is missing at the pinned date $pin"$'\n'
done
if [ -n "$intel_problems" ]; then
  while IFS= read -r line; do [ -z "$line" ] || echo "::error::$line"; done <<< "$intel_problems"
fi

case "$mode" in
update)
  [ "${pinned_missing:-0}" = 0 ] || { echo "refusing to snapshot: the pinned date is not served" >&2; exit 1; }
  mkdir -p "$snap"
  cp "$work/compatibility-dates.json" "$snap/compatibility-dates.json"
  cp "$work/openapi.yaml" "$snap/openapi.yaml"
  echo "snapshots updated for pinned date $pin:"
  ls -l "$snap/compatibility-dates.json" "$snap/openapi.yaml"
  exit 0
  ;;
check) ;;
*) echo "usage: $0 check|update" >&2; exit 2 ;;
esac

rc="${pinned_missing:-0}"
report="$work/report.txt"
: > "$report"

if [ -n "$intel_problems" ]; then
  rc=1
  { echo "## ingest/intel routes at the pinned date $pin"; printf '%s' "$intel_problems"; echo; } >> "$report"
fi

if ! diff -u --label "committed/compatibility-dates.json" --label "live/compatibility-dates.json" \
  "$snap/compatibility-dates.json" "$work/compatibility-dates.json" > "$work/dates.diff"; then
  rc=1
  {
    echo "## /meta/compatibility-dates changed"
    added="$(jq -r --slurpfile old "$snap/compatibility-dates.json" \
      '.compatibility_dates[] | select(. as $d | ($old[0].compatibility_dates | index($d)) == null)' "$work/compatibility-dates.json")"
    removed="$(jq -r --slurpfile new "$work/compatibility-dates.json" \
      '.compatibility_dates[] | select(. as $d | ($new[0].compatibility_dates | index($d)) == null)' "$snap/compatibility-dates.json")"
    echo "new dates:     ${added:-none}" | tr '\n' ' '; echo
    echo "removed dates: ${removed:-none}" | tr '\n' ' '; echo
    echo "pinned date:   $pin"
    echo
    cat "$work/dates.diff"
  } >> "$report"

  # What did the newest date change relative to the pinned one? (route level, JSON spec)
  newest="$(jq -r '.compatibility_dates[0]' "$work/compatibility-dates.json")"
  if [ "$newest" != "$pin" ] \
    && fetch /meta/openapi.json -H "X-Compatibility-Date: $newest" > "$work/newest.json" 2>/dev/null \
    && fetch /meta/openapi.json -H "X-Compatibility-Date: $pin" > "$work/pinned.json" 2>/dev/null; then
    routes='[.paths | to_entries[] | .key as $p | .value | keys[] | select(. != "parameters") | "\(. | ascii_upcase) \($p)"] | sort | .[]'
    jq -r "$routes" "$work/pinned.json" > "$work/routes.pinned"
    jq -r "$routes" "$work/newest.json" > "$work/routes.newest"
    {
      echo
      echo "## routes: pinned $pin -> newest $newest"
      diff -u --label "routes@$pin" --label "routes@$newest" "$work/routes.pinned" "$work/routes.newest" || true
    } >> "$report"
  fi
fi

if ! diff -u --label "committed/openapi.yaml@$pin" --label "live/openapi.yaml@$pin" \
  "$snap/openapi.yaml" "$work/openapi.yaml" > "$work/spec.diff"; then
  rc=1
  {
    echo
    echo "## OpenAPI spec for $pin changed ($(grep -c '^[+-][^+-]' "$work/spec.diff") changed lines; first 400 shown)"
    head -n 400 "$work/spec.diff"
  } >> "$report"
fi

if [ "$rc" -ne 0 ]; then
  cat "$report"
  if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    { echo '```diff'; cat "$report"; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
  fi
  echo
  echo "ESI contract changed. Review it, re-check the ESI callers, then run core/esi/contract.sh update"
  echo "(and bump DefaultCompatibilityDate in core/esi/config.go if the new date is wanted)."
  exit 1
fi
echo "ESI contract unchanged (pinned $pin, $intel_count ingest/intel routes present; $(jq '.compatibility_dates | length' "$work/compatibility-dates.json") compatibility dates)."
