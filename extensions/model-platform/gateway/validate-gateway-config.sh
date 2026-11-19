#!/usr/bin/env bash
#
# validate-gateway-config.sh
# Verifies LiteLLM Gateway baseline configuration, internal network isolation,
# and model pool definition for oh-my-ragflow model-platform extension.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/docker-compose.yml"
CONFIG_FILE="${SCRIPT_DIR}/litellm-config.yaml"

echo "=== LiteLLM Gateway Configuration Validator ==="
echo "Compose file: ${COMPOSE_FILE}"
echo "Config file:  ${CONFIG_FILE}"

# 1. Verify existence of files and parser dependencies
if [[ ! -f "${COMPOSE_FILE}" ]]; then
    echo "FAIL: docker-compose.yml not found at ${COMPOSE_FILE}" >&2
    exit 1
fi
if [[ ! -f "${CONFIG_FILE}" ]]; then
    echo "FAIL: litellm-config.yaml not found at ${CONFIG_FILE}" >&2
    exit 1
fi
if ! python3 -c "import yaml" &>/dev/null; then
    echo "FAIL: Python package 'pyyaml' is required to validate gateway configuration. Install via: pip install pyyaml" >&2
    exit 1
fi
echo "[PASS] Configuration files and YAML parser available."

# 2. Verify network topology & port isolation
echo "--> Checking network topology and port isolation..."
python3 - "${COMPOSE_FILE}" <<'EOF'
import re, sys

compose_file = sys.argv[1]
with open(compose_file) as f:
    lines = f.readlines()

in_ports = False
ports = []
for line in lines:
    if re.match(r'^\s*ports:\s*$', line):
        in_ports = True
        continue
    if in_ports:
        if re.match(r'^\s*[a-zA-Z0-9_-]+:\s*', line):
            in_ports = False
            continue
        m = re.match(r'^\s*-\s*["\']?(.*?)["\']?\s*$', line)
        if m:
            ports.append(m.group(1).strip())
        hip_match = re.search(r'host_ip:\s*["\']?(.*?)["\']?\s*$', line)
        if hip_match:
            hip = hip_match.group(1).strip()
            if hip != '127.0.0.1':
                print(f"FAIL: Detected non-loopback host_ip '{hip}' in {compose_file}!", file=sys.stderr)
                sys.exit(1)

if not ports:
    print(f"FAIL: No published ports found under litellm-proxy in {compose_file}!", file=sys.stderr)
    sys.exit(1)

for p in ports:
    if not p.startswith('127.0.0.1:'):
        print(f"FAIL: Port publication '{p}' in {compose_file} is not strictly bound to loopback 127.0.0.1!", file=sys.stderr)
        sys.exit(1)

if not any('127.0.0.1:4000:4000' in p for p in ports):
    print(f"FAIL: Missing required port mapping '127.0.0.1:4000:4000' in {compose_file}!", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] Port isolation verified (all published ports strictly bound to 127.0.0.1)."

# 3. Verify pinned LiteLLM version (v1.50+)
echo "--> Checking LiteLLM pinned image version..."
python3 - "${COMPOSE_FILE}" <<'EOF'
import re, sys

compose_file = sys.argv[1]
with open(compose_file) as f:
    lines = f.readlines()

in_proxy = False
image_val = None
for line in lines:
    clean_line = line.split('#')[0].strip()
    if re.match(r'^\s*litellm-proxy:\s*$', line):
        in_proxy = True
        continue
    if in_proxy:
        if re.match(r'^\s*[a-zA-Z0-9_-]+:\s*', line) and not line.startswith('    ') and not line.startswith('\t'):
            in_proxy = False
            continue
        m = re.match(r'^\s*image:\s*(.+)$', clean_line)
        if m:
            image_val = m.group(1).strip().strip('"\'')
            break

if not image_val:
    print(f"FAIL: LiteLLM image not specified in {compose_file}!", file=sys.stderr)
    sys.exit(1)

if not image_val.startswith('ghcr.io/berriai/litellm:'):
    print(f"FAIL: LiteLLM image must be from ghcr.io/berriai/litellm, got: {image_val}", file=sys.stderr)
    sys.exit(1)

tag = image_val.split(':', 1)[1]
if tag == 'latest':
    print(f"FAIL: LiteLLM image cannot use 'latest' tag, must be pinned to stable version: {image_val}", file=sys.stderr)
    sys.exit(1)

m_ver = re.match(r'^v?([0-9]+)\.([0-9]+)', tag)
if not m_ver:
    print(f"FAIL: LiteLLM image tag format not recognized as version: {tag}", file=sys.stderr)
    sys.exit(1)

major, minor = int(m_ver.group(1)), int(m_ver.group(2))
if major < 1 or (major == 1 and minor < 50):
    print(f"FAIL: LiteLLM version must be pinned to >= v1.50, got: {tag}", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] Pinned version verified (>= v1.50)."

# 4. Verify PostgreSQL metadata backend configuration
echo "--> Checking PostgreSQL metadata backend..."
python3 - "${COMPOSE_FILE}" <<'EOF'
import sys, yaml

compose_file = sys.argv[1]
with open(compose_file) as f:
    data = yaml.safe_load(f)

services = data.get('services', {})
if 'litellm-postgres' not in services or not isinstance(services['litellm-postgres'], dict):
    print(f"FAIL: Missing 'litellm-postgres' service block in {compose_file}", file=sys.stderr)
    sys.exit(1)

pg_service = services['litellm-postgres']
image = pg_service.get('image', '')
if not image.startswith('postgres:'):
    print(f"FAIL: litellm-postgres service must use postgres image, got: {image}", file=sys.stderr)
    sys.exit(1)

proxy_service = services.get('litellm-proxy', {})
env = proxy_service.get('environment', {})
db_url = None
if isinstance(env, dict):
    db_url = env.get('DATABASE_URL')
elif isinstance(env, list):
    for entry in env:
        if isinstance(entry, str) and entry.startswith('DATABASE_URL='):
            db_url = entry.split('=', 1)[1]
            break

if not db_url:
    print(f"FAIL: Missing DATABASE_URL in litellm-proxy environment in {compose_file}", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] PostgreSQL metadata backend verified."

# 5. Verify model pool in litellm-config.yaml
echo "--> Checking model pool definitions..."
python3 - "${CONFIG_FILE}" <<'EOF'
import re, sys

config_file = sys.argv[1]
with open(config_file) as f:
    lines = f.readlines()

model_names = []
for line in lines:
    clean_line = line.split('#')[0].strip()
    m = re.match(r'^-\s*model_name:\s*(.+)$', clean_line) or re.match(r'^\s*model_name:\s*(.+)$', clean_line)
    if m:
        model_names.append(m.group(1).strip().strip('"\''))

if 'platform/chat' not in model_names:
    print(f"FAIL: Missing active 'platform/chat' model_name entry in {config_file}", file=sys.stderr)
    sys.exit(1)

if 'platform/embedding' not in model_names:
    print(f"FAIL: Missing active 'platform/embedding' model_name entry in {config_file}", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] Model pool verified (Chat and Embedding models present)."

# 6. Verify security settings (credential desensitization & secret isolation)
echo "--> Checking security settings..."
python3 - "${CONFIG_FILE}" <<'EOF'
import sys, yaml

config_file = sys.argv[1]
with open(config_file) as f:
    data = yaml.safe_load(f)

settings = data.get('litellm_settings', {})
redact = settings.get('redact_user_api_key_info')
if redact is not True:
    print(f"FAIL: litellm_settings.redact_user_api_key_info must be boolean true in {config_file}, got: {redact!r}", file=sys.stderr)
    sys.exit(1)
EOF

# Ensure all model api_key entries use os.environ/ references (rejects plaintext or quoted secret literals)
python3 - "${CONFIG_FILE}" <<'EOF'
import re, sys

config_file = sys.argv[1]
with open(config_file) as f:
    lines = f.readlines()

api_keys_found = False
for idx, line in enumerate(lines, start=1):
    clean_line = line.split('#')[0].strip()
    m = re.match(r'^\s*api_key:\s*["\']?(.*?)["\']?\s*$', clean_line)
    if m:
        api_keys_found = True
        val = m.group(1).strip().strip('"\'')
        if not val.startswith('os.environ/'):
            print(f"FAIL: Hardcoded or non-environment api_key at {config_file}:{idx}! Must use os.environ/ references.", file=sys.stderr)
            sys.exit(1)

if not api_keys_found:
    print(f"FAIL: No api_key declarations found in {config_file}!", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] Credential masking and secret isolation verified."

# 7. Compose syntax validation with docker compose if available
if command -v docker &>/dev/null && docker compose version &>/dev/null; then
    echo "--> Running docker compose config syntax validation..."
    POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-test-db-password}" \
    LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-sk-platform-validation-dummy-key}" \
    LITELLM_SALT_KEY="${LITELLM_SALT_KEY:-test-salt-key}" \
    docker compose -f "${COMPOSE_FILE}" config >/dev/null
    echo "[PASS] docker compose config syntax is valid."
fi

echo "=== All LiteLLM Gateway baseline checks PASSED! ==="
