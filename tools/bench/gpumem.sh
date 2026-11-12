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
    client = fields.get('drm-client-id')
    if client is None:
        continue
    key = (fields.get('drm-pdev', ''), client.strip())
    if key in seen:
        continue
    seen.add(key)
    vram += int(fields.get('drm-memory-vram', '0').split()[0])
    gtt += int(fields.get('drm-memory-gtt', '0').split()[0])
    engine += sum(int(v.split()[0]) for k, v in fields.items() if k.startswith('drm-engine-'))
print(f'vram_kib={vram} gtt_kib={gtt} engine_ns={engine}')
PY
