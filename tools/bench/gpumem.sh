#!/usr/bin/env bash
# Sum DRM accounting once per client, even when several fds refer to it.
set -euo pipefail
pid=${1:?usage: gpumem.sh <pid>}
[[ $pid =~ ^[0-9]+$ ]] || { echo 'pid must be numeric' >&2; exit 2; }
python3 - "$pid" <<'PY'
import pathlib, sys
seen = set()
vram = gtt = engine = 0
files = list((pathlib.Path('/proc') / sys.argv[1] / 'fdinfo').iterdir())
for path in files:
    try:
        fields = dict(line.split(':', 1) for line in path.read_text().splitlines() if ':' in line)
    except FileNotFoundError:
        continue  # fd closed during sampling
    def kib(key, fields=fields):
        parts = fields.get(key, '').split()
        return int(parts[0]) if parts else 0
    client = fields.get('drm-client-id')
    if client is None:
        continue
    key = (fields.get('drm-pdev', ''), client.strip())
    if key in seen:
        continue
    seen.add(key)
    vram += kib('drm-memory-vram')
    gtt += kib('drm-memory-gtt')
    for k, v in fields.items():
        if k.startswith('drm-engine-') and not k.startswith('drm-engine-capacity-') and v.split():
            engine += int(v.split()[0])
print(f'vram_kib={vram} gtt_kib={gtt} engine_ns={engine}')
PY
