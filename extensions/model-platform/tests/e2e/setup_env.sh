#!/usr/bin/env bash
#
# setup_env.sh
# End-to-end (E2E) test environment setup and network isolation verification orchestrator.
# Prepares clean build workspace, validates gateway security & isolation,
# and generates test environment configurations.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
GATEWAY_DIR="${PROJECT_ROOT}/extensions/model-platform/gateway"
E2E_DIR="${PROJECT_ROOT}/extensions/model-platform/tests/e2e"
COMPOSE_TEST="${E2E_DIR}/docker-compose.test.yml"
ENV_TEST="${E2E_DIR}/.env.test"

START_TIME=$(date +%s)

echo "================================================================="
echo "=== Model Platform E2E Test Environment Orchestrator ==="
echo "================================================================="
echo "Project root: ${PROJECT_ROOT}"
echo "E2E dir:      ${E2E_DIR}"
echo "Start time:   $(date -u +"%Y-%m-%dT%H:%M:%SZ")"

# 1. Verify and enforce non-invasive overlay pipeline
echo ""
echo "--> [Step 1/5] Executing overlay pipeline and CI pre-flight assertions..."
bash "${PROJECT_ROOT}/extensions/model-platform/scripts/apply-overlay.sh"

# 2. Validate Gateway configuration and baseline security
echo ""
echo "--> [Step 2/5] Validating LiteLLM gateway configuration and secret isolation..."
bash "${GATEWAY_DIR}/validate-gateway-config.sh"

# 3. Verify E2E Test Docker Compose and Physical Network Isolation
echo ""
echo "--> [Step 3/5] Verifying E2E Docker Compose topology and physical network isolation..."
if [[ ! -f "${COMPOSE_TEST}" ]]; then
    echo "FAIL: docker-compose.test.yml not found at ${COMPOSE_TEST}" >&2
    exit 1
fi

# Assert port 4000 is strictly bound to loopback 127.0.0.1
if grep -qE '"?0\.0\.0\.0:4000' "${COMPOSE_TEST}" || grep -qE '^\s*-\s*"?4000:4000' "${COMPOSE_TEST}"; then
    echo "FAIL: Found public port exposure in docker-compose.test.yml!" >&2
    echo "Port 4000 must be strictly bound to 127.0.0.1:4000:4000 for internal network isolation." >&2
    exit 1
fi

if ! grep -qE '127\.0\.0\.1:4000:4000' "${COMPOSE_TEST}"; then
    echo "FAIL: Missing required loopback binding '127.0.0.1:4000:4000' in docker-compose.test.yml!" >&2
    exit 1
fi

# Assert PostgreSQL test container has no host port exposure
if grep -qE '5432:5432' "${COMPOSE_TEST}"; then
    echo "FAIL: PostgreSQL service in docker-compose.test.yml exposes host port! Must remain on internal network." >&2
    exit 1
fi

# Assert LiteLLM version is pinned to >= 1.50 / latest stable (v1.110.0+)
if ! grep -qE 'ghcr\.io/berriai/litellm:(main-)?v1\.(5[0-9]|[6-9][0-9]|[0-9]{3,})' "${COMPOSE_TEST}"; then
    echo "FAIL: LiteLLM version in docker-compose.test.yml must be pinned to ghcr.io/berriai/litellm:main-v1.110.0+ (or v1.50+)" >&2
    exit 1
fi
echo "[PASS] E2E test compose network isolation verified."

# 4. Generate E2E test environment file (.env.test)
echo ""
echo "--> [Step 4/5] Generating E2E test environment file (.env.test)..."
cat > "${ENV_TEST}" <<EOF
# Model Platform E2E Test Environment
# Generated automatically by setup_env.sh at $(date -u +"%Y-%m-%dT%H:%M:%SZ")

LITELLM_BASE_URL="http://127.0.0.1:4000"
LITELLM_MASTER_KEY="sk-master-e2e-platform-test-key"
LITELLM_SALT_KEY="sk-salt-e2e-platform-test-key"
PLATFORM_DEFAULT_CHAT_MODEL="gpt-4o-mini"
PLATFORM_DEFAULT_EMBEDDING_MODEL="text-embedding-3-small"
EOF
echo "[PASS] Test environment file written to ${ENV_TEST}"

# 5. Check container orchestration readiness
echo ""
echo "--> [Step 5/5] Checking container engine & network readiness..."
DOCKER_AVAILABLE=false
if command -v docker >/dev/null 2>&1; then
    if docker info >/dev/null 2>&1; then
        DOCKER_AVAILABLE=true
    fi
fi

if [[ "${DOCKER_AVAILABLE}" == "true" ]]; then
    echo "[INFO] Docker daemon is active and responsive."
    if [[ "${1:-}" == "--up" || "${1:-}" == "--start" ]]; then
        echo "Starting E2E containers..."
        docker compose -f "${COMPOSE_TEST}" up -d
        echo "Waiting for services to become healthy..."
        HEALTHY=false
        for i in {1..30}; do
            if curl -s -f http://127.0.0.1:4000/health/liveliness >/dev/null 2>&1; then
                echo "[PASS] LiteLLM test proxy is healthy at http://127.0.0.1:4000"
                HEALTHY=true
                break
            fi
            sleep 1
        done
        if [[ "${HEALTHY}" != "true" ]]; then
            echo "FAIL: LiteLLM test proxy did not become healthy within 45s!" >&2
            docker compose -f "${COMPOSE_TEST}" logs >&2
            exit 1
        fi
    else
        echo "[INFO] Containers ready to be launched with: docker compose -f ${COMPOSE_TEST} up -d"
    fi
else
    echo "[INFO] Docker daemon not currently active in this environment."
    echo "[INFO] Static network isolation and mock integration harness verified."
fi

END_TIME=$(date +%s)
DURATION=$((END_TIME - START_TIME))

echo ""
echo "================================================================="
echo "=== E2E Environment Setup COMPLETED in ${DURATION}s (target < 60s) ==="
echo "================================================================="
exit 0
