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
SKIP_AUDIT="${SKIP_AUDIT:-false}"

while [[ $# -gt 0 ]]; do
    case "$1" in
        --check-only|-c)
            CHECK_ONLY=true
            shift
            ;;
        --skip-audit)
            SKIP_AUDIT=true
            shift
            ;;
        --help|-h)
            echo "Usage: $0 [--check-only|-c] [--skip-audit]"
            exit 0
            ;;
        *)
            echo "FATAL: Unknown option: $1 (supported: --check-only, -c, --skip-audit, --help, -h)" >&2
            exit 1
            ;;
    esac
done

echo "=== Model Platform Overlay Pipeline ==="
echo "Project root: ${ROOT_DIR}"
echo "Upstream dir: ${UPSTREAM_DIR}"
echo "Build dir:    ${BUILD_DIR}"

if [[ "${CHECK_ONLY}" == true ]]; then
    echo "--> Running compliance audit (--check-only)..."
    bash "${SCRIPT_DIR}/audit-non-invasive.sh"
    echo "=== All assertions PASSED (--check-only completed). ==="
    exit 0
fi

if [[ "${SKIP_AUDIT}" != true ]]; then
    echo "--> Running pre-flight compliance audit..."
    bash "${SCRIPT_DIR}/audit-non-invasive.sh"
else
    echo "--> Pre-flight compliance audit skipped (SKIP_AUDIT=true)."
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
bash "${SCRIPT_DIR}/audit-non-invasive.sh" --cleanliness-only

echo "=== Model Platform Overlay Pipeline completed successfully! ==="
echo "Workspace is ready at: ${BUILD_DIR}"
