"""Create once; never silently rotate passwords paired with persistent volumes."""
import json
import os
from pathlib import Path
import secrets

root = Path(os.environ["RYBBIT_STATE"])
owner = {"workspace": os.environ["RYBBIT_WORKSPACE"], "project": os.environ["RYBBIT_PROJECT"], "port": os.environ["RYBBIT_PORT"]}
marker = root / "owner.json"
if marker.exists():
    if json.loads(marker.read_text()) != owner:
        raise SystemExit("Состояние принадлежит другой конфигурации; сохраните его и выберите новый проект")
    if not (root / ".env").is_file() or not (root / "admin.json").is_file():
        raise SystemExit("Неполное состояние; требуется явное восстановление или очистка")
else:
    if list(root.iterdir()):
        raise SystemExit("Неполное состояние; автоматическая перезапись запрещена")
    values = {key: secrets.token_hex(32) for key in ("POSTGRES_PASSWORD", "REDIS_PASSWORD", "CLICKHOUSE_PASSWORD", "CLICKHOUSE_QUERY_PASSWORD", "BETTER_AUTH_SECRET")}
    (root / ".env").write_text("".join(f"{key}={value}\n" for key, value in values.items()))
    (root / "admin.json").write_text(json.dumps({"email": "admin@marketmesh.test", "password": secrets.token_urlsafe(32), "name": "MarketMesh local"}))
    marker.write_text(json.dumps(owner))
