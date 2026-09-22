#!/usr/bin/env python3
"""Изолированный стенд MM-43. Секреты остаются в .cache/files-local (0700)."""
import json
import os
import re
from pathlib import Path
import secrets
import ssl
import subprocess
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
PROJECT = os.environ.get("FILES_LOCAL_PROJECT", "marketmesh-files-local")
if not re.fullmatch(r"marketmesh-files-[a-z0-9][a-z0-9-]{0,39}", PROJECT):
    raise RuntimeError("invalid Files project")
STATE = Path(os.environ.get("FILES_LOCAL_STATE", str(ROOT / ".cache/files-local")))
if not STATE.is_absolute() or STATE.is_symlink() or not STATE.resolve().is_relative_to((ROOT / ".cache").resolve()):
    raise RuntimeError("Files state must be owned storage below repository .cache")
ZONES = ("quarantine", "internal-clean", "delivery-a", "delivery-b")
ENV = dict(os.environ, MM_FILES_STATE=str(STATE), MM_FILES_UID=str(os.getuid()), MM_FILES_GID=str(os.getgid()), GOCACHE=str(STATE / "go-build"))


def run(*args, quiet=False):
    result = subprocess.run(args, cwd=ROOT, env=ENV, stdout=subprocess.DEVNULL if quiet else None, check=False)
    if result.returncode:
        raise RuntimeError("fixture command failed")


def compose(*args, quiet=False):
    run("docker", "compose", "--project-name", PROJECT, "-f", str(ROOT / "infra/files-local/compose.yml"), *args, quiet=quiet)


def write(name, value):
    target = STATE / name
    target.write_text(value if isinstance(value, str) else json.dumps(value))
    target.chmod(0o600)


def credential():
    return {"AccessKey": secrets.token_hex(12), "SecretKey": secrets.token_hex(32)}


def prepare():
    STATE.mkdir(mode=0o700, parents=True, exist_ok=True)
    STATE.chmod(0o700)
    if (STATE / "owner.json").exists():
        if json.loads((STATE / "owner.json").read_text()) != {"task": "MM-43", "project": PROJECT}:
            raise RuntimeError("unowned fixture state")
        return
    run("go", "run", str(ROOT / "infra/files-local/pki.go"), str(STATE / "pki"), quiet=True)
    creds = {zone: {role: credential() for role in ("admin", "control", "worker", "capability")} for zone in ZONES}
    write("credentials.json", creds)
    passwords = {role: secrets.token_hex(24) for role in ("postgres", "files_rw", "files_worker", "files_ro", "files_replicator")}
    write("database.json", passwords)
    write("admin-password", passwords["postgres"])
    sql = "CREATE DATABASE files;\nREVOKE ALL ON DATABASE files FROM PUBLIC;\n"
    for role in ("files_rw", "files_worker", "files_ro", "files_replicator"):
        attribute = " REPLICATION" if role == "files_replicator" else ""
        sql += f"CREATE ROLE {role} LOGIN{attribute} PASSWORD '{passwords[role]}';\n"
    sql += "GRANT CONNECT ON DATABASE files TO files_rw, files_worker, files_ro;\n"
    write("db-init.sql", sql)
    write("replication.pgpass", "pg-primary:5432:*:files_replicator:" + passwords["files_replicator"] + "\n")
    write("bao.hcl", '''disable_mlock = true
storage "file" { path = "/data" }
listener "tcp" {
  address = "0.0.0.0:8200"
  tls_cert_file = "/pki/server.crt"
  tls_key_file = "/pki/server.key"
  tls_min_version = "tls13"
}
''')
    write("filer.toml", '[leveldb2]\nenabled = true\ndir = "/data/filerldb2"\n')
    write("freshclam.conf", "DatabaseMirror database.clamav.net\nDatabaseDirectory /signatures\nForeground yes\nLogTime no\nConnectTimeout 30\nReceiveTimeout 120\n")
    write("owner.json", {"task": "MM-43", "project": PROJECT})


def bao(path, body=None, token=None, method=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["X-Vault-Token"] = token
    request = urllib.request.Request("https://localhost:18210/v1/" + path, data=None if body is None else json.dumps(body).encode(), headers=headers, method=method)
    context = ssl.create_default_context(cafile=str(STATE / "pki/ca.crt"))
    with urllib.request.urlopen(request, context=context, timeout=10) as response:
        raw = response.read(1024 * 1024)
        return json.loads(raw) if raw else None


def initialize_kms():
    for attempt in range(30):
        try:
            initialized = bao("sys/init")["initialized"]
            break
        except (OSError, urllib.error.URLError):
            time.sleep(1)
    else:
        raise RuntimeError("KMS not ready")
    if not initialized:
        write("bao-init.json", bao("sys/init", {"secret_shares": 1, "secret_threshold": 1}, method="PUT"))
    init = json.loads((STATE / "bao-init.json").read_text())
    bao("sys/unseal", {"key": init["keys"][0]}, method="PUT")
    token = init["root_token"]
    if "transit/" not in bao("sys/mounts", token=token):
        bao("sys/mounts/transit", {"type": "transit"}, token)
    creds = json.loads((STATE / "credentials.json").read_text())
    for zone in ZONES:
        key = "test-" + zone
        bao("transit/keys/" + key, {"type": "aes256-gcm96"}, token)
        policy = f'path "transit/encrypt/{key}" {{ capabilities = ["update"] }}\npath "transit/decrypt/{key}" {{ capabilities = ["update"] }}\npath "transit/keys/{key}" {{ capabilities = ["read"] }}'
        bao("sys/policies/acl/" + key, {"policy": policy}, token, method="PUT")
        limited = bao("auth/token/create", {"policies": [key], "ttl": "12h", "renewable": False, "no_default_policy": True}, token)["auth"]["client_token"]
        zone_config = {"identities": [], "policies": [], "kms": {"default_provider": "files", "providers": {"files": {"type": "openbao", "address": "https://bao:8200", "token": limited, "ca_cert": "/pki/ca.crt", "cache_enabled": False, "request_timeout": 5}}}}
        for role, pair in creds[zone].items():
            allowed = {
                "admin": ["s3:*"],
                "control": ["s3:GetObject"],
                "worker": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"],
                "capability": ["s3:PutObject"] if zone == "quarantine" else ["s3:GetObject"],
            }[role]
            if zone == "internal-clean" and role not in ("worker", "admin"):
                continue
            resource = [f"arn:aws:s3:::{zone}", f"arn:aws:s3:::{zone}/*"]
            statements = [{"Effect": "Allow", "Action": allowed, "Resource": resource}]
            if role == "capability" and zone == "quarantine":
                statements += [{"Effect": "Deny", "Action": ["s3:GetObject", "s3:DeleteObject", "s3:ListBucket"], "Resource": resource}]
            policy_name = zone + "-" + role
            zone_config["policies"].append({"name": policy_name, "content": json.dumps({"Version": "2012-10-17", "Statement": statements})})
            zone_config["identities"].append({"name": policy_name, "credentials": [{"accessKey": pair["AccessKey"], "secretKey": pair["SecretKey"]}], "actions": [], "policyNames": [policy_name]})
        write(zone + ".json", zone_config)
    configuration(creds)


def configuration(creds):
    passwords = json.loads((STATE / "database.json").read_text())
    def dsn(role, host, sync=False):
        return f"postgres://{role}:{passwords[role]}@{host}:5432/files?sslmode=verify-full&sslrootcert=/pki/ca.crt" + ("&synchronous_commit=remote_apply" if sync else "")
    def bucket(zone, worker):
        result = {"APIEndpoint": f"https://{zone}:8333", "PublicEndpoint": f"https://{zone}:8333", "Bucket": zone, "KMSKey": "test-" + zone, "Control": creds[zone]["worker" if worker else "control"]}
        if not worker:
            result["Capability"] = creds[zone]["capability"]
        return result
    worker = {"Role": "worker", "DatabaseRW": dsn("files_worker", "pg-primary", True), "DatabaseRO": dsn("files_ro", "pg-replica"), "StorageCA": "/pki/ca.crt", "TempDir": "/work", "ClamSocket": "/run/files-clamav/clamd.sock", "SandboxSocket": "/run/files-sandbox/clean.sock", "Quarantine": bucket("quarantine", True), "Internal": bucket("internal-clean", True), "Delivery": [bucket("delivery-a", True), bucket("delivery-b", True)]}
    write("worker.json", worker)
    control = {"Role": "control", "DatabaseRW": dsn("files_rw", "pg-primary", True), "DatabaseRO": dsn("files_ro", "pg-replica"), "StorageCA": "/pki/ca.crt", "Quarantine": bucket("quarantine", False), "Delivery": [bucket("delivery-a", False), bucket("delivery-b", False)]}
    write("control.json", control)


def start():
    prepare()
    compose("build", "sandbox", "antivirus", "worker")
    compose("up", "-d", "volume-init", "bao", "pg-primary", "pg-replica")
    initialize_kms()
    compose("run", "--rm", "signatures")
    compose("up", "-d", "--force-recreate", "quarantine", "internal-clean", "delivery-a", "delivery-b", "sandbox", "antivirus")
    for attempt in range(30):
        result = subprocess.run(["go", "run", str(ROOT / "infra/files-local/storage.go"), str(STATE)], cwd=ROOT, env=ENV, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if result.returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError("bucket policies unavailable")
    compose("up", "-d", "--force-recreate", "worker")
    print("MM-43: приватный файловый контур запущен; публичный API выключен.")


def existing():
    if not (STATE / "owner.json").is_file() or json.loads((STATE / "owner.json").read_text()) != {"task": "MM-43", "project": PROJECT}:
        raise RuntimeError("MM-43 fixture is not initialized")


def main():
    command = sys.argv[1] if len(sys.argv) == 2 else ""
    if command == "up":
        start()
    elif command == "renew":
        existing()
        compose("down", "--remove-orphans")
        run("go", "run", str(ROOT / "infra/files-local/pki.go"), str(STATE / "pki"), quiet=True)
        start()  # Storage volumes, transit keys and metadata are retained.
    elif command == "down":
        existing()
        compose("down", "--remove-orphans")
    elif command == "status":
        existing()
        compose("ps")
    elif command == "test":
        existing()
        compose("stop", "worker")
        try:
            run(sys.executable, str(ROOT / "infra/files-local/documents.py"))
            compose("build", "integration")
            compose("run", "--rm", "integration")
            compose("run", "--rm", "--entrypoint", "/usr/local/bin/files-s3-integration", "integration", "-test.v", "-test.run=TestLive", "-test.timeout=3m")
            run(sys.executable, str(ROOT / "infra/files-local/database_checks.py"))
        finally:
            compose("up", "-d", "worker")
    else:
        raise RuntimeError("usage: local.py up|down|status|test|renew")


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Dependencies can echo signed URLs or credentials; never emit raw exceptions.
        print("MM-43: операция стенда не завершена; подробности зависимости не выводятся.", file=sys.stderr)
        sys.exit(1)
