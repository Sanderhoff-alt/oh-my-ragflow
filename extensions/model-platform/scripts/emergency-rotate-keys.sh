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
# still has a superseded key that may be valid (HTTP 202; the platform keeps revoking it and a rerun
# with --resume finishes it), 2 for invalid usage.
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
}

list_ready_tenants() {
    local page=1 raw code body total seen=0
    while :; do
        raw="$(api GET "${API}/tenants?status=ready&page=${page}&page_size=${PAGE_SIZE}")" ||
            die "could not reach the admin API"
        code="${raw##*$'\n'}"
        body="${raw%$'\n'*}"
        [[ "${code}" == 200 ]] || die "listing tenants failed with HTTP ${code}: ${body}"
        jq -r '.data.items[].tenant_id' <<<"${body}"
        total="$(jq -r '.data.total' <<<"${body}")"
        seen=$((seen + $(jq -r '.data.items | length' <<<"${body}")))
        if [[ "${seen}" -ge "${total}" || "$(jq -r '.data.items | length' <<<"${body}")" -eq 0 ]]; then
            return 0
        fi
        page=$((page + 1))
    done
}

while [[ $# -gt 0 ]]; do
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
    rotate_one "${WORKER}"
    exit 0
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
trap 'rm -rf "${WORK_DIR}"' EXIT
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
export ADMIN_URL ADMIN_TOKEN ROTATE_OUTPUT="${OUTPUT}"
echo "rotating ${COUNT} tenant(s), concurrency ${CONCURRENCY}, at most ${RATE_LIMIT} per second, results in ${OUTPUT}"

INTERVAL="$(awk -v r="${RATE_LIMIT}" 'BEGIN { printf "%.3f", 1 / r }')"
SELF="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
while IFS= read -r id; do
    printf '%s\n' "${id}"
    sleep "${INTERVAL}"
done < "${TENANT_FILE}" | xargs -P "${CONCURRENCY}" -I{} bash "${SELF}" --worker {}

# Only the tenants of this run count, so a reused output file does not distort the summary
jq -s --rawfile run "${TENANT_FILE}" '
    ($run | split("\n") | map(select(. != ""))) as $ids
    | map(select(.tenant_id as $t | $ids | index($t)))
    | {rotated: map(select(.http_status == 200)) | length,
       pending_revocation: map(select(.http_status == 202)) | length,
       failed: map(select(.http_status != 200 and .http_status != 202)) | length}' "${OUTPUT}" > "${WORK_DIR}/summary"
cat "${WORK_DIR}/summary"
if [[ "$(jq '.pending_revocation + .failed' "${WORK_DIR}/summary")" -gt 0 ]]; then
    echo "some tenants need attention, rerun with --resume ${OUTPUT} after checking them:" >&2
    jq -r 'select(.http_status != 200) | "  \(.tenant_id): HTTP \(.http_status)"' "${OUTPUT}" >&2
    exit 1
fi
