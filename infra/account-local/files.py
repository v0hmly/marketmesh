#!/usr/bin/env python3
"""Локальный Auth/User/Mailpit + существующий Files; байты идут прямо в storage."""
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("files_local", ROOT / "infra/files-local/local.py")
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.existing()
mode = sys.argv[1] if len(sys.argv) == 2 else ""
if mode not in ("up", "test", "down", "renew"):
    raise SystemExit("Usage: files.py up|test|down|renew (start owned files-local first)")
fresh = mode == "test"
project = "marketmesh-account-avatar-test-" + secrets.token_hex(4) if fresh else os.environ.get("ACCOUNT_LOCAL_PROJECT", "marketmesh-account-avatar")
if not re.fullmatch(r"marketmesh-account-[a-z0-9][a-z0-9-]{0,39}", project):
    raise RuntimeError("invalid account project")
state = ROOT / "infra/account-local/.state" / project
port = os.environ.get("ACCOUNT_LOCAL_PORT", "19443" if fresh else "18443")
if not port.isdigit() or not 1024 <= int(port) <= 65535:
    raise RuntimeError("invalid loopback port")
workspace = str(ROOT / "infra/account-local")
if state.is_symlink() or state.parent.is_symlink():
    raise RuntimeError("symlink state refused")
marker = project + "|" + workspace
if state.exists():
    if fresh or not (state / ".owner").is_file() or (state / ".owner").read_text().strip() != marker:
        raise RuntimeError("unowned account state")
else:
    if mode == "down":
        raise RuntimeError("account state missing")
    state.mkdir(mode=0o700, parents=True)
    (state / ".owner").write_text(marker + "\n")
env = dict(f.ENV, ACCOUNT_LOCAL_PROJECT=project, ACCOUNT_LOCAL_STATE=str(state), ACCOUNT_LOCAL_WORKSPACE=workspace, ACCOUNT_LOCAL_IMAGE=project+":local", ACCOUNT_LOCAL_PORT=port, ACCOUNT_MAILPIT_PORT="0" if fresh else os.environ.get("ACCOUNT_MAILPIT_PORT", "18025"), FIXTURE_ROOT=str(state), ACCOUNT_USER_CONSUME_ENABLED="false" if fresh else "true", ACCOUNT_E2E_PHASE="pending" if fresh else "core", ACCOUNT_E2E_RUN_ID="avatar-"+secrets.token_hex(8))
overlay = state / "files.override.json"

def run(*args, cwd=None):
    subprocess.run(args, env=env, cwd=cwd or ROOT, check=True)

def compose(*args):
    run("docker", "compose", "--project-directory", workspace, "--project-name", project, "-f", str(ROOT / "infra/account-local/compose.yml"), "-f", str(overlay), *args)


def check_resources():
    # Never adopt another checkout's Compose project or volumes.
    for kind in ("container", "volume", "network"):
        args = ["docker", "ps", "-aq"] if kind == "container" else ["docker", kind, "ls", "-q"]
        ids = subprocess.check_output([*args, "--filter", "label=com.docker.compose.project="+project], text=True).split()
        if fresh and ids:
            raise RuntimeError("fresh account resources required")
        for resource in ids:
            info = json.loads(subprocess.check_output(["docker", "inspect" if kind == "container" else kind, *([] if kind == "container" else ["inspect"]), resource], text=True))[0]
            labels = info["Config"]["Labels"] if kind == "container" else info["Labels"]
            field = "com.docker.compose.project.working_dir" if kind == "container" else "marketmesh.account.workspace"
            if labels.get(field) != workspace:
                raise RuntimeError("account resources owned by another workspace")

check_resources()
if mode == "down":
    compose("down", "--remove-orphans")
    raise SystemExit(0)

run("go", "run", "./cmd/account-local", "generate", cwd=ROOT / "tools/account-local")
pki = state / "files-pki"
if mode == "renew":
    if not overlay.is_file():
        raise RuntimeError("owned Files overlay required for renewal")
    compose("down", "--remove-orphans")  # Preserve data; restart all trust consumers together.
if not pki.exists() or mode == "renew":
    run("go", "run", str(ROOT / "infra/files-local/pki.go"), str(pki))
else:
    if pki.is_symlink() or not (pki / "ca.crt").is_file():
        raise RuntimeError("invalid owned workload PKI")
    for name in ("user", "files", "auth", "gateway-out"):
        if not (pki / (name+".key")).is_file():
            raise RuntimeError("incomplete workload PKI; use files.py renew")
        valid = subprocess.run(["openssl", "x509", "-checkend", "600", "-noout", "-in", str(pki / (name+".crt"))], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if valid.returncode:
            raise RuntimeError("workload PKI needs coordinated renewal; use files.py renew")
from files_config import configure
configure(state, f.STATE, fresh)
origins = ["https://quarantine:8333", "https://delivery-a:8333", "https://delivery-b:8333"] if fresh else ["https://localhost:18343", "https://localhost:18344", "https://localhost:18345"]
config = state / "files-control.json"
workload = str(state / "files-pki")+":/workload:ro"
external = {"files-private": "files-internal", "files-q": "quarantine-dmz", "files-a": "delivery-a-dmz", "files-b": "delivery-b-dmz"}
services = {
    "auth": {"volumes": [workload], "build": {"args": {"VITE_ACCOUNT_AVATAR_ENABLED": "true", "VITE_FILES_UPLOAD_ORIGIN": origins[0], "VITE_FILES_DOWNLOAD_ORIGINS": ",".join(origins[1:])}}},
    "user": {"volumes": [workload]},
    "gateway-out": {"volumes": [workload]},
    "frontdoor": {"environment": {"ACCOUNT_FILES_ORIGINS": ",".join(origins)}},
    "files-control": {"image": "marketmesh-mm43-files:local", "user": f"{os.getuid()}:{os.getgid()}", "read_only": True, "cap_drop": ["ALL"], "security_opt": ["no-new-privileges:true"], "environment": {"FILES_CONFIG_FILE": "/config.json"}, "networks": ["internal", *external], "volumes": [str(config)+":/config.json:ro", workload, str(f.STATE / "pki/ca.crt")+":/pki/ca.crt:ro"], "logging": {"driver": "json-file", "options": {"max-size": "1m", "max-file": "1"}}},
    "browser": {"environment": {"ACCOUNT_AVATAR_E2E": "true", "PUBLIC_FILES_CA": "/certs/files-ca.pem", "PUBLIC_COMBINED_CA": "/certs/combined-ca.pem"}, "networks": ["dmz", "mail", "files-q", "files-a", "files-b"]},
}
overlay.write_text(json.dumps({"services": services, "networks": {key: {"external": True, "name": f.PROJECT+"_"+value} for key, value in external.items()}})); overlay.chmod(0o600)
# CORS contains exact known dev/test origins only; never a wildcard or credentials.
env["MM_FILES_BROWSER_ORIGINS"] = "https://frontdoor:8443" + ("" if fresh else ",https://localhost:"+port)
try:
    f.compose("build", "worker")
    f.compose("up", "-d", "worker")
    run("go", "run", str(ROOT / "infra/files-local/storage.go"), str(f.STATE))
    compose("build", "auth")
    compose("up", "-d", "--wait", "--wait-timeout", "150", "postgres-primary", "postgres-replica")
    compose("up", "-d", "redis", "nats")
    compose("up", "-d", "--wait", "--wait-timeout", "90", "mailpit")
    compose("run", "--rm", "--no-deps", "provision")
    compose("up", "-d", "--wait", "--wait-timeout", "120", "auth", "user")
    compose("up", "-d", "files-control")
    compose("up", "-d", "--scale", "gateway-in=2", "gateway-in", "frontdoor")
    compose("up", "-d", "--wait", "--wait-timeout", "150", "gateway-out")
    if fresh:
        compose("build", "browser")
        compose("run", "--rm", "--no-deps", "browser")
        env["ACCOUNT_USER_CONSUME_ENABLED"] = "true"
        compose("up", "-d", "--wait", "--wait-timeout", "120", "--force-recreate", "user")
        env["ACCOUNT_E2E_PHASE"] = "core"
        compose("run", "--rm", "--no-deps", "browser")
    else:
        print("Avatar account: https://localhost:"+port+" (local CA: "+str(state / "browser/combined-ca.pem")+")")
finally:
    if fresh:
        # Unique disposable account project; all external Files resources retained.
        compose("down", "--volumes", "--remove-orphans")
