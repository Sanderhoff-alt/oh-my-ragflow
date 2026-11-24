#!/usr/bin/env bash
#
# validate-gateway-config.sh
# Verifies LiteLLM Gateway baseline configuration, internal network isolation,
# secret isolation, and model pool definition for oh-my-ragflow model-platform extension.
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${COMPOSE_FILE:-${SCRIPT_DIR}/docker-compose.yml}"
CONFIG_FILE="${CONFIG_FILE:-${SCRIPT_DIR}/litellm-config.yaml}"

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
import sys, yaml

compose_file = sys.argv[1]
with open(compose_file) as f:
    data = yaml.safe_load(f)

services = data.get('services', {})

# 2.1 Reject 'include' and 'extends' declarations to prevent uninspected port/network/secret inheritance
if 'include' in data:
    print(f"FAIL: Compose 'include' directive is not permitted in {compose_file} to ensure self-contained isolation boundary!", file=sys.stderr)
    sys.exit(1)

for svc_name, svc in services.items():
    if isinstance(svc, dict) and 'extends' in svc:
        print(f"FAIL: Service '{svc_name}' must not use 'extends' declaration to prevent hidden port or secret inheritance!", file=sys.stderr)
        sys.exit(1)

# 2.2 PostgreSQL must have NO host port exposure or host networking
pg = services.get('litellm-postgres', {})
if 'ports' in pg:
    print(f"FAIL: PostgreSQL service exposes host ports ({pg['ports']})! It must remain on internal container network.", file=sys.stderr)
    sys.exit(1)
net_mode = str(pg.get('network_mode', '')).lower()
if 'host' in net_mode or any(net_mode.startswith(pfx) for pfx in ['service:', 'container:']):
    print(f"FAIL: PostgreSQL service must not use host or namespace-sharing network_mode (detected {pg.get('network_mode')!r})! It must remain on internal container network.", file=sys.stderr)
    sys.exit(1)
if '$' in net_mode:
    print(f"FAIL: PostgreSQL service must not use unresolved network_mode substitution ({pg.get('network_mode')!r})! It must remain on internal container network.", file=sys.stderr)
    sys.exit(1)

# 2.3 Proxy must bind strictly to loopback 127.0.0.1
proxy = services.get('litellm-proxy', {})
ports = proxy.get('ports', [])
if not ports:
    print(f"FAIL: No published ports found under litellm-proxy in {compose_file}!", file=sys.stderr)
    sys.exit(1)

def parse_port_entry(p):
    if isinstance(p, (int, float)):
        return ('0.0.0.0', None, str(int(p)), 'tcp')
    if isinstance(p, str):
        proto = 'tcp'
        val = p
        if '/' in val:
            parts_slash = val.split('/', 1)
            val = parts_slash[0].strip()
            proto = parts_slash[1].strip().lower()
        parts = val.split(':')
        if len(parts) == 3:
            return (parts[0].strip(), parts[1].strip(), parts[2].strip(), proto)
        elif len(parts) == 2:
            return ('0.0.0.0', parts[0].strip(), parts[1].strip(), proto)
        elif len(parts) == 1:
            return ('0.0.0.0', None, parts[0].strip(), proto)
        else:
            return (None, None, None, proto)
    if isinstance(p, dict):
        host_ip = str(p.get('host_ip', '0.0.0.0')).strip()
        published = str(p.get('published', '')).strip()
        target = str(p.get('target', '')).strip()
        proto = str(p.get('protocol', 'tcp')).strip().lower()
        return (host_ip, published, target, proto)
    return (None, None, None, 'unknown')

found_required_mapping = False
for p in ports:
    host_ip, published, target, proto = parse_port_entry(p)
    if host_ip != '127.0.0.1':
        print(f"FAIL: Port publication '{p}' in {compose_file} is not strictly bound to loopback 127.0.0.1 (got host_ip '{host_ip}')!", file=sys.stderr)
        sys.exit(1)
    if published == '4000' and target == '4000' and proto == 'tcp':
        found_required_mapping = True

if not found_required_mapping:
    print(f"FAIL: Missing required TCP port mapping for 127.0.0.1:4000->4000 in {compose_file}!", file=sys.stderr)
    sys.exit(1)

# 2.4 Verify secret reference injection boundary in Compose services
proxy_env = proxy.get('environment', {})
proxy_has_upstream_key = False
if isinstance(proxy_env, dict):
    val = str(proxy_env.get('PLATFORM_UPSTREAM_OPENAI_KEY', '')).strip()
    proxy_has_upstream_key = bool(val and 'PLATFORM_UPSTREAM_OPENAI_KEY' in val)
elif isinstance(proxy_env, list):
    for e in proxy_env:
        if isinstance(e, str) and e.startswith('PLATFORM_UPSTREAM_OPENAI_KEY='):
            val = e.split('=', 1)[1].strip()
            if val and 'PLATFORM_UPSTREAM_OPENAI_KEY' in val:
                proxy_has_upstream_key = True
                break
        elif e == 'PLATFORM_UPSTREAM_OPENAI_KEY':
            proxy_has_upstream_key = True
            break

if not proxy_has_upstream_key:
    print(f"FAIL: Required non-empty fail-fast 'PLATFORM_UPSTREAM_OPENAI_KEY' binding missing from 'litellm-proxy' in {compose_file}!", file=sys.stderr)
    sys.exit(1)

for svc_name, svc_conf in services.items():
    if svc_name == 'litellm-proxy' or not isinstance(svc_conf, dict):
        continue
    env_entries = svc_conf.get('environment', {})
    has_key = False
    if isinstance(env_entries, dict):
        has_key = any(
            'PLATFORM_UPSTREAM_OPENAI_KEY' in str(k) or 'PLATFORM_UPSTREAM_OPENAI_KEY' in str(v)
            for k, v in env_entries.items()
        )
    elif isinstance(env_entries, list):
        has_key = any(
            isinstance(e, str) and 'PLATFORM_UPSTREAM_OPENAI_KEY' in e
            for e in env_entries
        )
    sec_entries = svc_conf.get('secrets', [])
    if isinstance(sec_entries, list):
        has_key = has_key or any(
            'PLATFORM_UPSTREAM_OPENAI_KEY' in str(s)
            for s in sec_entries
        )
    elif isinstance(sec_entries, dict):
        has_key = has_key or any(
            'PLATFORM_UPSTREAM_OPENAI_KEY' in str(k) or 'PLATFORM_UPSTREAM_OPENAI_KEY' in str(v)
            for k, v in sec_entries.items()
        )
    if has_key:
        print(f"FAIL: Service '{svc_name}' must not receive or reference 'PLATFORM_UPSTREAM_OPENAI_KEY'! Upstream credentials must remain isolated to 'litellm-proxy'.", file=sys.stderr)
        sys.exit(1)
EOF
echo "[PASS] Port isolation verified (all LiteLLM ports on 127.0.0.1, PostgreSQL unexposed)."

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

# 5. Verify model definitions in litellm-config.yaml
echo "--> Checking model definitions..."
python3 - "${CONFIG_FILE}" <<'EOF'
import sys, yaml

class UniqueKeySafeLoader(yaml.SafeLoader):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._checked_nodes = set()

    def _check_mapping_duplicates(self, mapping_node):
        if id(mapping_node) in self._checked_nodes:
            return
        self._checked_nodes.add(id(mapping_node))
        explicit_keys = set()
        for k_node, _ in mapping_node.value:
            if k_node.tag == 'tag:yaml.org,2002:merge' or k_node.value == '<<':
                if '<<' in explicit_keys:
                    raise ValueError("Duplicate merge key '<<' found in YAML mapping")
                explicit_keys.add('<<')
                continue
            k = self.construct_object(k_node)
            if k in explicit_keys:
                raise ValueError(f"Duplicate key {k!r} found in YAML mapping")
            explicit_keys.add(k)

    def flatten_mapping(self, node):
        for key_node, value_node in node.value:
            if key_node.tag == 'tag:yaml.org,2002:merge' or key_node.value == '<<':
                if isinstance(value_node, yaml.MappingNode):
                    self._check_mapping_duplicates(value_node)
                elif isinstance(value_node, yaml.SequenceNode):
                    for subnode in value_node.value:
                        if isinstance(subnode, yaml.MappingNode):
                            self._check_mapping_duplicates(subnode)
        super().flatten_mapping(node)

    def construct_mapping(self, node, deep=False):
        self._check_mapping_duplicates(node)
        return super().construct_mapping(node, deep=deep)

config_file = sys.argv[1]
try:
    with open(config_file) as f:
        data = yaml.load(f, Loader=UniqueKeySafeLoader)
except ValueError as e:
    print(f"FAIL: {e} in {config_file}", file=sys.stderr)
    sys.exit(1)

model_list = data.get('model_list', [])
if not isinstance(model_list, list):
    print(f"FAIL: 'model_list' must be a list in {config_file}", file=sys.stderr)
    sys.exit(1)

models_by_name = {}
for entry in model_list:
    if not isinstance(entry, dict):
        print(f"FAIL: Every model_list item must be a mapping in {config_file}", file=sys.stderr)
        sys.exit(1)
    name = entry.get('model_name')
    if not isinstance(name, str) or not name.strip():
        print(f"FAIL: Model entry missing nonblank 'model_name' string in {config_file}", file=sys.stderr)
        sys.exit(1)
    params = entry.get('litellm_params')
    if not isinstance(params, dict):
        print(f"FAIL: Model '{name}' litellm_params must be a mapping in {config_file}", file=sys.stderr)
        sys.exit(1)
    target = params.get('model')
    if not isinstance(target, str) or not target.strip():
        print(f"FAIL: Model '{name}' litellm_params.model must be a nonblank string in {config_file}", file=sys.stderr)
        sys.exit(1)
    models_by_name.setdefault(name, []).append(entry)

# The catalog decides which models exist, so an empty model_list is valid. Every route that is listed
# must name its provider explicitly (for example openai/gpt-4o-mini).
for name, entries in models_by_name.items():
    for entry in entries:
        target_model = entry['litellm_params']['model']
        if '/' not in target_model:
            print(f"FAIL: Model '{name}' litellm_params.model must carry an explicit provider prefix, got '{target_model}' in {config_file}", file=sys.stderr)
            sys.exit(1)
EOF
echo "[PASS] Model definitions verified (every listed route has an explicit provider prefix)."

# 6. Verify security settings (credential desensitization & secret isolation)
echo "--> Checking security settings..."
# 6.1 Verify litellm_settings desensitization flags
python3 - "${CONFIG_FILE}" <<'EOF'
import sys, yaml

class UniqueKeySafeLoader(yaml.SafeLoader):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._checked_nodes = set()

    def _check_mapping_duplicates(self, mapping_node):
        if id(mapping_node) in self._checked_nodes:
            return
        self._checked_nodes.add(id(mapping_node))
        explicit_keys = set()
        for k_node, _ in mapping_node.value:
            if k_node.tag == 'tag:yaml.org,2002:merge' or k_node.value == '<<':
                if '<<' in explicit_keys:
                    raise ValueError("Duplicate merge key '<<' found in YAML mapping")
                explicit_keys.add('<<')
                continue
            k = self.construct_object(k_node)
            if k in explicit_keys:
                raise ValueError(f"Duplicate key {k!r} found in YAML mapping")
            explicit_keys.add(k)

    def flatten_mapping(self, node):
        for key_node, value_node in node.value:
            if key_node.tag == 'tag:yaml.org,2002:merge' or key_node.value == '<<':
                if isinstance(value_node, yaml.MappingNode):
                    self._check_mapping_duplicates(value_node)
                elif isinstance(value_node, yaml.SequenceNode):
                    for subnode in value_node.value:
                        if isinstance(subnode, yaml.MappingNode):
                            self._check_mapping_duplicates(subnode)
        super().flatten_mapping(node)

    def construct_mapping(self, node, deep=False):
        self._check_mapping_duplicates(node)
        return super().construct_mapping(node, deep=deep)

config_file = sys.argv[1]
try:
    with open(config_file) as f:
        data = yaml.load(f, Loader=UniqueKeySafeLoader)
except ValueError as e:
    print(f"FAIL: {e} in {config_file}", file=sys.stderr)
    sys.exit(1)

settings = data.get('litellm_settings', {})
expected_flags = [
    ('redact_user_api_key_info', True),
    ('drop_params', True),
    ('set_verbose', False),
    ('json_logs', True),
]
for flag_name, expected_val in expected_flags:
    val = settings.get(flag_name)
    if val is not expected_val:
        print(f"FAIL: litellm_settings.{flag_name} must be {expected_val!r} in {config_file}, got: {val!r}", file=sys.stderr)
        sys.exit(1)

# Ensure certificate verification is not disabled
if settings.get('ssl_verify') is False:
    print(f"FAIL: litellm_settings.ssl_verify must not be False in {config_file} to ensure TLS certificate verification!", file=sys.stderr)
    sys.exit(1)
EOF

# 6.2 Ensure all model api_key entries use os.environ/ references (rejects plaintext or quoted secret literals)
python3 - "${CONFIG_FILE}" <<'EOF'
import os, sys, yaml

class UniqueKeySafeLoader(yaml.SafeLoader):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self._checked_nodes = set()

    def _check_mapping_duplicates(self, mapping_node):
        if id(mapping_node) in self._checked_nodes:
            return
        self._checked_nodes.add(id(mapping_node))
        explicit_keys = set()
        for k_node, _ in mapping_node.value:
            if k_node.tag == 'tag:yaml.org,2002:merge' or k_node.value == '<<':
                if '<<' in explicit_keys:
                    raise ValueError("Duplicate merge key '<<' found in YAML mapping")
                explicit_keys.add('<<')
                continue
            k = self.construct_object(k_node)
            if k in explicit_keys:
                raise ValueError(f"Duplicate key {k!r} found in YAML mapping")
            explicit_keys.add(k)

    def flatten_mapping(self, node):
        for key_node, value_node in node.value:
            if key_node.tag == 'tag:yaml.org,2002:merge' or key_node.value == '<<':
                if isinstance(value_node, yaml.MappingNode):
                    self._check_mapping_duplicates(value_node)
                elif isinstance(value_node, yaml.SequenceNode):
                    for subnode in value_node.value:
                        if isinstance(subnode, yaml.MappingNode):
                            self._check_mapping_duplicates(subnode)
        super().flatten_mapping(node)

    def construct_mapping(self, node, deep=False):
        self._check_mapping_duplicates(node)
        return super().construct_mapping(node, deep=deep)

config_file = sys.argv[1]
try:
    with open(config_file) as f:
        content = f.read()
    loader = UniqueKeySafeLoader(content)
    root_node = loader.get_single_node()
    data = loader.construct_document(root_node)
except ValueError as e:
    print(f"FAIL: {e} in {config_file}", file=sys.stderr)
    sys.exit(1)

def find_node_credentials(node, loader, visited=None):
    if visited is None:
        visited = set()
    if node is None or id(node) in visited:
        return []
    visited.add(id(node))
    found = []
    if isinstance(node, yaml.MappingNode):
        for k_node, v_node in node.value:
            k = loader.construct_object(k_node)
            if k in {'api_key', 'master_key'}:
                v = loader.construct_object(v_node)
                found.append((k, v))
            found.extend(find_node_credentials(v_node, loader, visited))
    elif isinstance(node, yaml.SequenceNode):
        for item in node.value:
            found.extend(find_node_credentials(item, loader, visited))
    return found

creds = find_node_credentials(root_node, loader)
if not creds:
    print(f"FAIL: No credential declarations found in {config_file}!", file=sys.stderr)
    sys.exit(1)

for key_name, key_val in creds:
    if not isinstance(key_val, str) or not key_val.startswith('os.environ/') or not key_val.split('/', 1)[1].strip():
        print(f"FAIL: Hardcoded, empty, or non-environment {key_name} in {config_file}! Must reference a valid non-empty os.environ/ variable name.", file=sys.stderr)
        sys.exit(1)
    if key_name == 'master_key' and key_val != 'os.environ/LITELLM_MASTER_KEY':
        print(f"FAIL: master_key in {config_file} must reference os.environ/LITELLM_MASTER_KEY!", file=sys.stderr)
        sys.exit(1)
    if key_name == 'api_key' and key_val != 'os.environ/PLATFORM_UPSTREAM_OPENAI_KEY':
        print(f"FAIL: api_key in {config_file} must reference injected os.environ/PLATFORM_UPSTREAM_OPENAI_KEY!", file=sys.stderr)
        sys.exit(1)
EOF

# 6.3 Scan gateway directory tracked/configuration files for hardcoded plaintext secret patterns
# Disable execution tracing temporarily to prevent credential leakage in verbose environments
{ set +x; } 2>/dev/null
python3 - "${SCRIPT_DIR}" <<'EOF'
import os, re, subprocess, sys
from pathlib import Path

script_dir = Path(sys.argv[1]).resolve()
secret_pattern = re.compile(r'sk-(?:proj-)?[a-zA-Z0-9_\-]{20,}')

target_files = []
has_git = False

try:
    proc_git = subprocess.run(
        ['git', '-C', str(script_dir), 'rev-parse', '--is-inside-work-tree'],
        capture_output=True, text=True, check=False
    )
    if proc_git.returncode == 0:
        has_git = True
        proc_ls = subprocess.run(
            ['git', '-C', str(script_dir), 'ls-files', '-z', '.'],
            capture_output=True, check=False
        )
        if proc_ls.returncode != 0:
            print(f"FAIL: git ls-files failed in {script_dir} with returncode {proc_ls.returncode}", file=sys.stderr)
            sys.exit(1)
        raw_files = proc_ls.stdout.split(b'\0')
        for rf in raw_files:
            if not rf:
                continue
            path_str = rf.decode('utf-8', errors='replace')
            p = script_dir / path_str
            if p.suffix in ('.md', '.example') or p.name == 'validate-gateway-config.sh':
                continue
            if p.is_file():
                target_files.append(p)
except FileNotFoundError:
    has_git = False

if not has_git:
    for p in script_dir.rglob('*'):
        if p.is_file():
            if p.suffix in ('.md', '.example') or p.name.startswith('.env') or p.name == 'validate-gateway-config.sh':
                continue
            target_files.append(p)

found_secrets = []
for p in target_files:
    try:
        content = p.read_text(encoding='utf-8', errors='replace')
        if secret_pattern.search(content):
            found_secrets.append(p.name)
    except Exception as e:
        print(f"FAIL: Could not read {p}: {e}", file=sys.stderr)
        sys.exit(1)

if found_secrets:
    print(f"FAIL: Hardcoded plaintext API key pattern found in gateway configuration files: {found_secrets}!", file=sys.stderr)
    sys.exit(1)
EOF
echo "[PASS] Credential masking configuration and secret isolation verified."

# 7. Compose syntax validation with docker compose if available
if command -v docker &>/dev/null && docker compose version &>/dev/null; then
    echo "--> Running docker compose config syntax validation..."
    # Keep execution tracing disabled during dummy secret interpolation
    { set +x; } 2>/dev/null
    POSTGRES_PASSWORD="${POSTGRES_PASSWORD:-test-db-password}" \
    LITELLM_MASTER_KEY="${LITELLM_MASTER_KEY:-platform-validation-dummy-key}" \
    LITELLM_SALT_KEY="${LITELLM_SALT_KEY:-test-salt-key}" \
    PLATFORM_UPSTREAM_OPENAI_KEY="${PLATFORM_UPSTREAM_OPENAI_KEY:-platform-validation-dummy-key}" \
    PLATFORM_UPSTREAM_OPENAI_BASE="${PLATFORM_UPSTREAM_OPENAI_BASE:-https://api.openai.com/v1}" \
    docker compose -f "${COMPOSE_FILE}" config >/dev/null
    echo "[PASS] docker compose config syntax is valid."
fi

echo "=== All LiteLLM Gateway baseline checks PASSED! ==="
