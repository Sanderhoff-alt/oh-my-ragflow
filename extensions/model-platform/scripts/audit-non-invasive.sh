#!/usr/bin/env bash
#
# audit-non-invasive.sh
# Automated non-invasive compliance audit and three-gate CI assertion suite
# for oh-my-ragflow model-platform extension.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
UPSTREAM_DIR="${ROOT_DIR}/upstream/ragflow"
OVERLAY_DIR="${ROOT_DIR}/extensions/model-platform/overlay"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

CLEANLINESS_ONLY=false
if [[ "${1:-}" == "--cleanliness-only" || "${1:-}" == "-c" ]]; then
    CLEANLINESS_ONLY=true
elif [[ -n "${1:-}" ]]; then
    if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
        echo "Usage: $0 [--cleanliness-only|-c]"
        exit 0
    fi
    echo "FATAL: Unknown option: $1 (supported: --cleanliness-only, -c, --help, -h)" >&2
    exit 1
fi

echo -e "${BLUE}======================================================================${NC}"
echo -e "${BLUE}      Model Platform Non-Invasive Compliance & CI Audit Suite        ${NC}"
echo -e "${BLUE}======================================================================${NC}"
echo "Project root: ${ROOT_DIR}"
echo "Upstream dir: ${UPSTREAM_DIR}"

AUDIT_FAILURES=0

# ---------------------------------------------------------------------------
# Gate 1: Upstream Submodule Non-Invasiveness & Absolute Cleanliness
# ---------------------------------------------------------------------------
echo ""
echo -e "${BLUE}--> Gate 1: Submodule Cleanliness & Git Diff Audit${NC}"

if [[ ! -d "${UPSTREAM_DIR}/.git" && ! -f "${UPSTREAM_DIR}/.git" ]]; then
    echo -e "${RED}[FAIL] Upstream submodule not initialized at ${UPSTREAM_DIR}${NC}" >&2
    echo "       Please run: git submodule update --init --recursive" >&2
    exit 1
fi

# Check 1.1: Pinned commit alignment check
PINNED_COMMIT="$(git -C "${ROOT_DIR}" rev-parse :upstream/ragflow 2>/dev/null || true)"
ACTUAL_COMMIT="$(git -C "${UPSTREAM_DIR}" rev-parse HEAD 2>/dev/null || true)"
if [[ -n "${PINNED_COMMIT}" && -n "${ACTUAL_COMMIT}" && "${PINNED_COMMIT}" != "${ACTUAL_COMMIT}" ]]; then
    echo -e "${RED}[FAIL] Upstream submodule commit differs from pinned gitlink:${NC}" >&2
    echo "       Pinned: ${PINNED_COMMIT}" >&2
    echo "       Actual: ${ACTUAL_COMMIT}" >&2
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] Upstream submodule commit matches pinned gitlink: ${ACTUAL_COMMIT:-<unknown>}${NC}"
fi

# Check 1.2: Git diff check (upstream code must be 100% identical to pinned commit)
if ! git -C "${UPSTREAM_DIR}" diff --quiet; then
    echo -e "${RED}[FAIL] Upstream submodule has unstaged modifications (git diff not empty):${NC}" >&2
    git -C "${UPSTREAM_DIR}" diff --stat >&2
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] git -C upstream/ragflow diff --quiet: Zero modified lines.${NC}"
fi

# Check 1.3: Git staged changes check
if ! git -C "${UPSTREAM_DIR}" diff --cached --quiet; then
    echo -e "${RED}[FAIL] Upstream submodule has staged changes (git diff --cached not empty):${NC}" >&2
    git -C "${UPSTREAM_DIR}" diff --cached --stat >&2
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] git -C upstream/ragflow diff --cached --quiet: Zero staged changes.${NC}"
fi

# Check 1.4: Git untracked files check
SUBMODULE_PORCELAIN="$(git -C "${UPSTREAM_DIR}" status --porcelain)"
if [[ -n "${SUBMODULE_PORCELAIN}" ]]; then
    echo -e "${RED}[FAIL] Upstream submodule contains untracked or dirty files:${NC}" >&2
    echo "${SUBMODULE_PORCELAIN}" >&2
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] git -C upstream/ragflow status --porcelain: Working tree 100% clean.${NC}"
fi

# If cleanliness-only check requested, exit immediately after Gate 1
if [[ "${CLEANLINESS_ONLY}" == "true" ]]; then
    echo ""
    echo -e "${BLUE}======================================================================${NC}"
    if [[ ${AUDIT_FAILURES} -eq 0 ]]; then
        echo -e "${GREEN}>>> SUBMODULE CLEANLINESS AUDIT: 100% CLEAN & PRISTINE (Exit Code: 0) <<<${NC}"
        echo -e "${BLUE}======================================================================${NC}"
        exit 0
    else
        echo -e "${RED}>>> SUBMODULE CLEANLINESS AUDIT: FAILED (${AUDIT_FAILURES} issues) (Exit Code: 1) <<<${NC}"
        echo -e "${BLUE}======================================================================${NC}"
        exit 1
    fi
fi

# ---------------------------------------------------------------------------
# Gate 2: Enterprise Environment Flag Safety Guard
# ---------------------------------------------------------------------------
echo ""
echo -e "${BLUE}--> Gate 2: Enterprise Environment Flag Safety Guard${NC}"

if [[ "${VITE_RAGFLOW_ENTERPRISE:-}" == "RAGFLOW_ENTERPRISE" ]]; then
    echo -e "${RED}[FAIL] VITE_RAGFLOW_ENTERPRISE is active ('RAGFLOW_ENTERPRISE')!${NC}" >&2
    echo "       Prematurely enabling upstream enterprise UI routes causes broken links and unauthorized backend requests." >&2
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] VITE_RAGFLOW_ENTERPRISE is safe (disabled or unset: '${VITE_RAGFLOW_ENTERPRISE:-<unset>}').${NC}"
fi

# ---------------------------------------------------------------------------
# Gate 3: 19-Package EE-Baseline Stubs & Trampolines Completeness Scan
# ---------------------------------------------------------------------------
echo ""
echo -e "${BLUE}--> Gate 3: 19-Package EE-Baseline Completeness Scan${NC}"

REQUIRED_PACKAGES=(
    "cmd"
    "internal/admin"
    "internal/cli"
    "internal/common"
    "internal/dao"
    "internal/deepdoc/parser/pdf/table"
    "internal/engine/clickhouse"
    "internal/entity"
    "internal/handler"
    "internal/permission"
    "internal/permission/testsupport"
    "internal/router"
    "internal/server"
    "internal/server/config"
    "internal/server/local"
    "internal/service"
    "internal/service/billing"
    "internal/service/dataset"
    "internal/service/document"
)

has_valid_ee_stub() {
    local target_dir="$1"
    if [[ ! -d "${target_dir}" ]]; then
        return 1
    fi
    while IFS= read -r file; do
        if grep -qE '^//(go:build|\+build).*(^|[^!a-zA-Z0-9_])ee([^a-zA-Z0-9_]|$)' "${file}" 2>/dev/null; then
            if ! grep -qE '^//(go:build|\+build).*!\s*\(?ee\b' "${file}" 2>/dev/null; then
                return 0
            fi
        fi
    done < <(find "${target_dir}" -maxdepth 1 -name "*.go" -type f)
    return 1
}

MISSING_PACKAGES=()
for pkg in "${REQUIRED_PACKAGES[@]}"; do
    pkg_overlay_dir="${OVERLAY_DIR}/ee-baseline/${pkg}"
    pkg_trampoline_dir="${OVERLAY_DIR}/trampolines/${pkg}"
    if has_valid_ee_stub "${pkg_overlay_dir}"; then
        continue
    elif has_valid_ee_stub "${pkg_trampoline_dir}"; then
        continue
    else
        MISSING_PACKAGES+=("${pkg}")
    fi
done

if [[ ${#MISSING_PACKAGES[@]} -gt 0 ]]; then
    echo -e "${RED}[FAIL] Missing valid //go:build ee baseline stubs for packages:${NC}" >&2
    for mp in "${MISSING_PACKAGES[@]}"; do
        echo -e "       - ${mp}" >&2
    done
    AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
else
    echo -e "${GREEN}[PASS] All 19 required packages have verified affirmative //go:build ee stubs or trampolines.${NC}"
fi

# Check 3.2: Verify router symbol exclusion in ee-baseline
BASELINE_DIR="${OVERLAY_DIR}/ee-baseline"
if [[ -d "${BASELINE_DIR}" ]]; then
    if grep -rqE 'func[[:space:]]+(SetupEERouter|RegisterEE)' "${BASELINE_DIR}" 2>/dev/null; then
        echo -e "${RED}[FAIL] ${BASELINE_DIR} must NOT declare SetupEERouter or RegisterEE* (reserved for trampolines)!${NC}" >&2
        AUDIT_FAILURES=$((AUDIT_FAILURES + 1))
    else
        echo -e "${GREEN}[PASS] Symbol isolation verified: ee-baseline does not collide with trampolines.${NC}"
    fi
fi

# ---------------------------------------------------------------------------
# Summary & Exit Code
# ---------------------------------------------------------------------------
echo ""
echo -e "${BLUE}======================================================================${NC}"
if [[ ${AUDIT_FAILURES} -eq 0 ]]; then
    echo -e "${GREEN}>>> AUDIT RESULT: 100% COMPLIANT - ALL THREE GATES PASSED (Exit Code: 0) <<<${NC}"
    echo -e "${BLUE}======================================================================${NC}"
    exit 0
else
    echo -e "${RED}>>> AUDIT RESULT: FAILED - ${AUDIT_FAILURES} AUDIT GATE(S) VIOLATED (Exit Code: 1) <<<${NC}"
    echo -e "${BLUE}======================================================================${NC}"
    exit 1
fi
