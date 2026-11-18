#!/usr/bin/env bash
#
# apply-overlay.sh
# Non-invasive build workspace overlay application & pre-flight assertions
# for oh-my-ragflow model-platform extension.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
UPSTREAM_DIR="${ROOT_DIR}/upstream/ragflow"
BUILD_DIR="${ROOT_DIR}/build/ragflow"
OVERLAY_DIR="${ROOT_DIR}/extensions/model-platform/overlay"
BACKEND_DIR="${ROOT_DIR}/extensions/model-platform/backend"

# Isolate inherited Git environment variables so explicit directory arguments (-C) are always respected
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY

CHECK_ONLY=false
if [[ "${1:-}" == "--check-only" || "${1:-}" == "-c" ]]; then
    CHECK_ONLY=true
elif [[ -n "${1:-}" ]]; then
    if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
        echo "Usage: $0 [--check-only|-c]"
        exit 0
    fi
    echo "FATAL: Unknown option: $1 (supported: --check-only, -c, --help, -h)" >&2
    exit 1
fi

echo "=== Model Platform Overlay Pipeline ==="
echo "Project root: ${ROOT_DIR}"
echo "Upstream dir: ${UPSTREAM_DIR}"
echo "Build dir:    ${BUILD_DIR}"

# ---------------------------------------------------------------------------
# Pre-Flight Assertion 1: Submodule Cleanliness
# ---------------------------------------------------------------------------
echo "--> Assertion 1: Checking upstream submodule cleanliness..."
if [[ ! -d "${UPSTREAM_DIR}/.git" && ! -f "${UPSTREAM_DIR}/.git" ]]; then
    echo "FATAL: Upstream submodule not initialized at ${UPSTREAM_DIR}" >&2
    echo "Run: git submodule update --init" >&2
    exit 1
fi

SUBMODULE_STATUS="$(git -C "${UPSTREAM_DIR}" status --porcelain --untracked-files=all)"
if [[ -n "${SUBMODULE_STATUS}" ]]; then
    echo "FATAL: Upstream submodule has uncommitted modifications or untracked files:" >&2
    echo "${SUBMODULE_STATUS}" >&2
    echo "Upstream kernel must remain 100% clean and unmodified!" >&2
    exit 1
fi
echo "    [PASS] Upstream submodule is 100% clean."

# ---------------------------------------------------------------------------
# Pre-Flight Assertion 2: Enterprise Flag Guard
# ---------------------------------------------------------------------------
echo "--> Assertion 2: Verifying VITE_RAGFLOW_ENTERPRISE environment flag..."
if [[ "${VITE_RAGFLOW_ENTERPRISE:-}" == "RAGFLOW_ENTERPRISE" ]]; then
    echo "FATAL: VITE_RAGFLOW_ENTERPRISE must NOT be set to 'RAGFLOW_ENTERPRISE'!" >&2
    echo "Enabling upstream enterprise UI routes prematurely leads to dead links and unauthenticated backend calls." >&2
    exit 1
fi
echo "    [PASS] VITE_RAGFLOW_ENTERPRISE is safe (disabled or unset)."

# ---------------------------------------------------------------------------
# Pre-Flight Assertion 3: 19 Package Baseline Completeness Scan
# ---------------------------------------------------------------------------
echo "--> Assertion 3: Verifying 19-package ee-baseline completeness..."
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

# Helper function: checks if a directory contains a valid //go:build ee stub without negation
has_valid_ee_stub() {
    local target_dir="$1"
    if [[ ! -d "${target_dir}" ]]; then
        return 1
    fi
    # Must contain a file with '//go:build ... ee' where 'ee' is an affirmative build tag
    while IFS= read -r file; do
        if grep -qE '^//(go:build|\+build).*(^|[^!a-zA-Z0-9_])ee([^a-zA-Z0-9_]|$)' "${file}" 2>/dev/null; then
            if ! grep -qE '^//(go:build|\+build).*!\s*\(?\s*ee\b' "${file}" 2>/dev/null; then
                return 0
            fi
        fi
    done < <(find "${target_dir}" -maxdepth 1 -name "*.go" ! -name "*_test.go" ! -name ".*" ! -name "_*" -type f)
    return 1
}

MISSING_PACKAGES=()
for pkg in "${REQUIRED_PACKAGES[@]}"; do
    BASELINE_PKG_DIR="${OVERLAY_DIR}/ee-baseline/${pkg}"
    TRAMPOLINE_PKG_DIR="${OVERLAY_DIR}/trampolines/${pkg}"

    if has_valid_ee_stub "${BASELINE_PKG_DIR}"; then
        continue
    elif has_valid_ee_stub "${TRAMPOLINE_PKG_DIR}"; then
        continue
    else
        MISSING_PACKAGES+=("${pkg}")
    fi
done

if [[ ${#MISSING_PACKAGES[@]} -gt 0 ]]; then
    echo "FATAL: The following required Go packages lack valid //go:build ee fallback stubs:" >&2
    for mp in "${MISSING_PACKAGES[@]}"; do
        echo "  - ${mp}" >&2
    done
    exit 1
fi

# Verify router symbol exclusion in ee-baseline to prevent redeclared errors with trampolines
ADMIN_BASELINE_DIR="${OVERLAY_DIR}/ee-baseline/internal/admin"
if [[ -d "${ADMIN_BASELINE_DIR}" ]]; then
    if grep -rqE 'func[[:space:]]+(SetupEERouter|RegisterEE)' "${ADMIN_BASELINE_DIR}" 2>/dev/null; then
        echo "FATAL: ${ADMIN_BASELINE_DIR} must NOT declare SetupEERouter or RegisterEE* (reserved for trampolines)!" >&2
        exit 1
    fi
fi

ROUTER_BASELINE_DIR="${OVERLAY_DIR}/ee-baseline/internal/router"
if [[ -d "${ROUTER_BASELINE_DIR}" ]]; then
    if grep -rqE 'func[[:space:]]+(SetupEERouter|RegisterEE)' "${ROUTER_BASELINE_DIR}" 2>/dev/null; then
        echo "FATAL: ${ROUTER_BASELINE_DIR} must NOT declare SetupEERouter or RegisterEE* (reserved for trampolines)!" >&2
        exit 1
    fi
fi

echo "    [PASS] All 19 packages verified with ee-baseline stubs and trampoline isolation."

if [[ "${CHECK_ONLY}" == true ]]; then
    echo "=== All assertions PASSED (--check-only completed). ==="
    exit 0
fi

# ---------------------------------------------------------------------------
# Step 1: Clean checkout of upstream into build workspace
# ---------------------------------------------------------------------------
echo "--> Preparing clean upstream build workspace at ${BUILD_DIR}..."
rm -rf "${BUILD_DIR}"
mkdir -p "${BUILD_DIR}"
git -C "${UPSTREAM_DIR}" archive HEAD | tar -x -C "${BUILD_DIR}"
echo "    Upstream baseline archived and extracted successfully."

# ---------------------------------------------------------------------------
# Step 2: Layered Overlay Application
# ---------------------------------------------------------------------------
# Layer 1: ee-baseline stubs
if [[ -d "${OVERLAY_DIR}/ee-baseline" && "$(ls -A "${OVERLAY_DIR}/ee-baseline" 2>/dev/null)" ]]; then
    echo "--> Applying Layer 1: ee-baseline stubs..."
    cp -R "${OVERLAY_DIR}/ee-baseline/." "${BUILD_DIR}/"
fi

# Layer 2: Trampolines (if present)
if [[ -d "${OVERLAY_DIR}/trampolines" && "$(ls -A "${OVERLAY_DIR}/trampolines" 2>/dev/null)" ]]; then
    echo "--> Applying Layer 2: Trampolines..."
    cp -R "${OVERLAY_DIR}/trampolines/." "${BUILD_DIR}/"
fi

# Layer 3: Backend extension code (if present)
if [[ -d "${BACKEND_DIR}" && "$(ls -A "${BACKEND_DIR}" 2>/dev/null)" ]]; then
    echo "--> Applying Layer 3: Backend extension code..."
    mkdir -p "${BUILD_DIR}/extensions/model-platform/backend"
    cp -R "${BACKEND_DIR}/." "${BUILD_DIR}/extensions/model-platform/backend/"
fi

# Layer 3.5: Integration tests (if present)
TESTS_DIR="${ROOT_DIR}/extensions/model-platform/tests"
if [[ -d "${TESTS_DIR}" && "$(ls -A "${TESTS_DIR}" 2>/dev/null)" ]]; then
    echo "--> Applying Layer 3.5: Integration tests..."
    mkdir -p "${BUILD_DIR}/extensions/model-platform/tests"
    cp -R "${TESTS_DIR}/." "${BUILD_DIR}/extensions/model-platform/tests/"
fi

# Layer 4: Web frontend overlay (if present)
if [[ -d "${OVERLAY_DIR}/web" && "$(ls -A "${OVERLAY_DIR}/web" 2>/dev/null)" ]]; then
    echo "--> Applying Layer 4: Web frontend overlays..."
    mkdir -p "${BUILD_DIR}/web"
    cp -R "${OVERLAY_DIR}/web/." "${BUILD_DIR}/web/"
fi

# Final check: Ensure upstream submodule was never touched during the process
AFTER_STATUS="$(git -C "${UPSTREAM_DIR}" status --porcelain --untracked-files=all)"
if [[ -n "${AFTER_STATUS}" ]]; then
    echo "FATAL: Submodule dirty after overlay process!" >&2
    exit 1
fi

echo "=== Model Platform Overlay Pipeline completed successfully! ==="
echo "Workspace is ready at: ${BUILD_DIR}"
