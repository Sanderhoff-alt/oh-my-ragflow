#!/usr/bin/env bash
#
# emergency-rotate-keys.sh
# Rotates the gateway virtual keys of model platform tenants through the admin API, for a credential
# leak or a scheduled secret rotation. The rotation logic lives in the API; this script only selects
# tenants and paces the calls, so it stays safe to interrupt and to run again: a rotation is
# idempotent and always leaves each tenant with exactly one valid key.
#
# Usage:
#   ADMIN_TOKEN=<superuser access token> emergency-rotate-keys.sh --tenant <id>
#   ADMIN_TOKEN=<superuser access token> emergency-rotate-keys.sh --all --force
#
# Options:
#   --tenant <id>       Rotate a single tenant.
#   --all               Rotate every ready tenant (requires --force unless --dry-run).
#   --force             Confirm a rotation of all tenants.
#   --dry-run           List the affected tenants without rotating anything.
#   --concurrency <n>   Rotations in flight at the same time (default 10).
#   --rate-limit <n>    Maximum rotations started per second (default 5).
#   --output <file>     NDJSON result file (default ./rotate-keys-<timestamp>.ndjson).
#   --resume <file>     Skip tenants this earlier result file records as rotated (HTTP 200).
#
# Environment:
#   ADMIN_TOKEN   Access token of a RAGFlow superuser (required).
#   ADMIN_URL     Admin API base URL (default http://127.0.0.1:9381/api/v1/admin).
#
# Exit status: 0 when every tenant was rotated and its old key revoked, 1 when some tenant failed or
# still has a superseded key that may be valid (HTTP 202; the platform keeps revoking it in the
# background, and a rerun with --resume rotates the tenant once more, which also revokes it), 2 for
# invalid usage.
set -euo pipefail

ADMIN_URL="${ADMIN_URL:-http://127.0.0.1:9381/api/v1/admin}"
API="${ADMIN_URL%/}/model-platform"
CONCURRENCY=10
RATE_LIMIT=5
PAGE_SIZE=500
TENANT=""
ALL=false
FORCE=false
DRY_RUN=false
OUTPUT=""
RESUME=""
WORKER=""

die() { echo "error: $*" >&2; exit 2; }

# api METHOD URL: prints the response body, then the HTTP status on its own last line.
# The token travels on stdin so it never shows up in the process list.
api() {
    printf 'header = "Authorization: %s"\n' "${ADMIN_TOKEN}" |
        curl -sS -K - --max-time 60 -X "$1" -w '\n%{http_code}' "$2"
}

valid_tenant() { [[ "$1" =~ ^[A-Za-z0-9_-]{1,64}$ ]]; }

rotate_one() {
    local id="$1" raw code body line
    if raw="$(api POST "${API}/tenants/${id}/rotate-key" 2>&1)"; then
        code="${raw##*$'\n'}"
        body="${raw%$'\n'*}"
    else
        code=0
        body="${raw}"
    fi
    line="$(jq -cn --arg id "${id}" --argjson code "${code}" --arg body "${body}" \
        '{tenant_id: $id, http_status: $code, response: $body}')"
    printf '%s\n' "${line}" >> "${OUTPUT}"
    printf '%s\n' "${line}" >> "${RUN_OUTPUT}"
}

# Tenants are read with a cursor on the tenant id, not by page number: a tenant that leaves the ready set while
# the list is being read would shift every later tenant down by one and an offset would skip it.
list_ready_tenants() {
    local after="" raw code body ids
    while :; do
        raw="$(api GET "${API}/tenants?status=ready&page_size=${PAGE_SIZE}&after=${after}")" ||
            die "could not reach the admin API"
        code="${raw##*$'\n'}"
        body="${raw%$'\n'*}"
        [[ "${code}" == 200 ]] || die "listing tenants failed with HTTP ${code}: ${body}"
        ids="$(jq -r '.data.items[].tenant_id' <<<"${body}")"
        [[ -n "${ids}" ]] || return 0
        printf '%s\n' "${ids}"
        after="$(tail -n 1 <<<"${ids}")"
    done
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --tenant|--concurrency|--rate-limit|--output|--resume|--worker)
            [[ $# -ge 2 ]] || die "$1 requires a value (see --help)" ;;
    esac
    case "$1" in
        --tenant) TENANT="${2:-}"; shift 2 ;;
        --all) ALL=true; shift ;;
        --force) FORCE=true; shift ;;
        --dry-run) DRY_RUN=true; shift ;;
        --concurrency) CONCURRENCY="${2:-}"; shift 2 ;;
        --rate-limit) RATE_LIMIT="${2:-}"; shift 2 ;;
        --output) OUTPUT="${2:-}"; shift 2 ;;
        --resume) RESUME="${2:-}"; shift 2 ;;
        --worker) WORKER="${2:-}"; shift 2 ;;
        -h|--help) sed -n '2,/^set -euo/p' "$0" | sed '$d;s/^# \{0,1\}//'; exit 0 ;;
        *) die "unknown option: $1 (see --help)" ;;
    esac
done

[[ -n "${ADMIN_TOKEN:-}" ]] || die "ADMIN_TOKEN is not set"
command -v curl >/dev/null || die "curl is required"
command -v jq >/dev/null || die "jq is required"

if [[ -n "${WORKER}" ]]; then
    OUTPUT="${ROTATE_OUTPUT:?}"
    RUN_OUTPUT="${ROTATE_RUN_OUTPUT:?}"
    # Wait for a start token: the gate hands out one every 1/rate-limit seconds to all workers together.
    # A worker that sees the pipe closed without a token (read fails) simply waits again.
    until read -r _ < "${ROTATE_GATE:?}"; do :; done
    rotate_one "${WORKER}"
    exit 0
fi

if [[ "${ADMIN_URL}" =~ ^http://(\[::1\]|[^/:]+) ]]; then
    case "${BASH_REMATCH[1]}" in
        127.0.0.1|localhost|"[::1]") ;;
        *) echo "warning: ADMIN_URL is plain HTTP to ${BASH_REMATCH[1]}, the admin token is sent unencrypted" >&2 ;;
    esac
fi

[[ -n "${TENANT}" || "${ALL}" == true ]] || die "choose --tenant <id> or --all"
[[ -z "${TENANT}" || "${ALL}" == false ]] || die "--tenant and --all are mutually exclusive"
if [[ "${ALL}" == true && "${FORCE}" == false && "${DRY_RUN}" == false ]]; then
    die "--all rotates every ready tenant, pass --force to confirm (or --dry-run to preview)"
fi
[[ "${CONCURRENCY}" =~ ^[1-9][0-9]*$ ]] || die "--concurrency must be a positive integer"
[[ "${RATE_LIMIT}" =~ ^[1-9][0-9]*$ ]] || die "--rate-limit must be a positive integer"
[[ -z "${TENANT}" ]] || valid_tenant "${TENANT}" || die "invalid tenant id: ${TENANT}"
[[ -z "${RESUME}" || -r "${RESUME}" ]] || die "cannot read the resume file: ${RESUME}"

WORK_DIR="$(mktemp -d)"
GATE_PID=""
cleanup() {
    [[ -z "${GATE_PID}" ]] || kill "${GATE_PID}" 2>/dev/null || true
    rm -rf "${WORK_DIR}"
}
trap cleanup EXIT
TENANT_FILE="${WORK_DIR}/tenants"

if [[ -n "${TENANT}" ]]; then
    echo "${TENANT}" > "${TENANT_FILE}"
else
    list_ready_tenants > "${TENANT_FILE}"
fi
if [[ -n "${RESUME}" ]]; then
    jq -r 'select(.http_status == 200) | .tenant_id' "${RESUME}" | sort -u > "${WORK_DIR}/done"
    grep -vxFf "${WORK_DIR}/done" "${TENANT_FILE}" > "${WORK_DIR}/todo" || true
    mv "${WORK_DIR}/todo" "${TENANT_FILE}"
fi
COUNT="$(grep -c . "${TENANT_FILE}" || true)"

if [[ "${DRY_RUN}" == true ]]; then
    cat "${TENANT_FILE}"
    echo "dry run: ${COUNT} tenant(s) would be rotated, nothing was changed" >&2
    exit 0
fi
[[ "${COUNT}" -gt 0 ]] || { echo "nothing to rotate"; exit 0; }

OUTPUT="${OUTPUT:-./rotate-keys-$(date +%Y%m%d-%H%M%S).ndjson}"
: >> "${OUTPUT}"
# The summary and the exit status describe this run only, while OUTPUT keeps the history of earlier runs
RUN_OUTPUT="${WORK_DIR}/run.ndjson"
: > "${RUN_OUTPUT}"
echo "rotating ${COUNT} tenant(s), concurrency ${CONCURRENCY}, at most ${RATE_LIMIT} per second, results in ${OUTPUT}"

INTERVAL="$(awk -v r="${RATE_LIMIT}" 'BEGIN { printf "%.6f", 1 / r }')"
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"

# The rate limit is enforced where requests start. The gate writes a token only while a worker waits for one
# (opening the pipe for writing blocks until a reader is there) and then sleeps, so the starts of all workers
# together are at least INTERVAL apart, however long a rotation takes and however many tokens would have queued.
GATE="${WORK_DIR}/gate"
mkfifo "${GATE}"
while :; do echo tick > "${GATE}"; sleep "${INTERVAL}"; done &
GATE_PID=$!
disown "${GATE_PID}"
export ADMIN_URL ADMIN_TOKEN ROTATE_OUTPUT="${OUTPUT}" ROTATE_RUN_OUTPUT="${RUN_OUTPUT}" ROTATE_GATE="${GATE}"
xargs -P "${CONCURRENCY}" -I{} bash "${SELF}" --worker {} < "${TENANT_FILE}"
kill "${GATE_PID}" 2>/dev/null || true
GATE_PID=""

jq -s --argjson count "${COUNT}" '
    {rotated: map(select(.http_status == 200)) | length,
     pending_revocation: map(select(.http_status == 202)) | length}
    | . + {failed: ($count - .rotated - .pending_revocation)}' "${RUN_OUTPUT}" > "${WORK_DIR}/summary"
cat "${WORK_DIR}/summary"
if [[ "$(jq '.pending_revocation + .failed' "${WORK_DIR}/summary")" -gt 0 ]]; then
    echo "some tenants need attention, rerun with --resume ${OUTPUT} after checking them:" >&2
    jq -r 'select(.http_status != 200) | "  \(.tenant_id): HTTP \(.http_status)"' "${RUN_OUTPUT}" >&2
    exit 1
fi
