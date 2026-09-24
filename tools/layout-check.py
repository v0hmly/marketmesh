#!/usr/bin/env python3
"""Check language boundaries and local references without installing dependencies."""
from pathlib import Path
import re
import shlex
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
files = set(subprocess.check_output(
    ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=ROOT,
).decode().split("\0")) - {""}
errors = []
for name in sorted(files):
    if re.match(r"frontend/apps/[^/]+/(test-results|playwright-report)/", name) or (name.startswith("backend/api/gen/go/") and name.endswith((".pb.go", ".connect.go"))) or (name.startswith("frontend/gen/") and name.endswith("_pb.ts")) or name in {"docs/design/design-system/tokens.css", "frontend/packages/design-system/tokens.css"}:
        errors.append(f"{name}: generated artifacts must be ignored, not tracked")
    path = ROOT / name
    if not path.is_file():
        continue  # Deleted paths can remain in the index before staging.
    suffix = path.suffix
    if (suffix == ".go" or path.name in {"go.mod", "go.sum", "go.work", "go.work.sum"}) and not name.startswith("backend/"):
        errors.append(f"{name}: Go files belong in backend/")
    if (suffix in {".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx"} or path.name in {"package.json", "pnpm-lock.yaml", "pnpm-workspace.yaml"}) and not name.startswith("frontend/"):
        errors.append(f"{name}: TS/JS files belong in frontend/")
    if path.name in {"easyp.yaml", "easyp.lock"} and path.parent != ROOT / "api":
        errors.append(f"{name}: EasyP configuration belongs in api/")
    if "Dockerfile" in path.name:
        for line in path.read_text().splitlines():
            if not line.startswith("COPY ") or "--from=" in line:
                continue
            for source in shlex.split(line)[1:-1]:
                if not source.startswith("--") and not list(ROOT.glob(source)):
                    errors.append(f"{name}: missing COPY source {source}")
    if suffix == ".md":
        for target in re.findall(r"\]\(([^)]+)\)", path.read_text()):
            if "://" in target or target.startswith(("#", "mailto:")):
                continue
            if not (path.parent / target.split("#", 1)[0]).exists():
                errors.append(f"{name}: missing local link {target}")
if errors:
    sys.exit("\n".join(errors))
print("Repository layout, Docker COPY sources and local documentation links: OK")
