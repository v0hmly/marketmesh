#!/usr/bin/env python3
"""Настоящие Auth/gateway/Files в одноразовом локальном проекте, без публикации."""
import importlib.util
import json
import os
from pathlib import Path
import secrets
import subprocess
import sys

spec = importlib.util.spec_from_file_location("files_local", Path(__file__).with_name("local.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.existing()
project = "marketmesh-account-mm43-" + secrets.token_hex(4)
state = f.STATE / project
state.mkdir(mode=0o700)
env = dict(f.ENV, ACCOUNT_LOCAL_PROJECT=project, ACCOUNT_LOCAL_STATE=str(state), ACCOUNT_LOCAL_WORKSPACE=str(f.ROOT / "infra/account-local"), ACCOUNT_LOCAL_IMAGE="marketmesh-mm43-account:local", ACCOUNT_LOCAL_PORT="18443", FIXTURE_ROOT=str(state))
overlay = state / "files.override.json"

def run(*args, cwd=None):
    result = subprocess.run(args, env=env, cwd=cwd or f.ROOT)
    if result.returncode:
        raise RuntimeError("E2E dependency failed")

def command(*args):
    return ["docker", "compose", "--project-directory", str(f.ROOT / "infra/account-local"), "--project-name", project, "-f", str(f.ROOT / "infra/account-local/compose.yml"), "-f", str(overlay), *args]

def compose(*args):
    run(*command(*args))

def settings(name, updates):
    path = state / name / "env"
    values = {line.split("=", 1)[0]: line.split("=", 1)[1].strip("'") for line in path.read_text().splitlines()}
    values.update(updates)
    path.write_text("".join(f"{key}='{value}'\n" for key, value in sorted(values.items())))
    path.chmod(0o600)

def prepare():
    run("go", "run", "./cmd/account-local", "generate", cwd=f.ROOT / "tools/account-local")
    run("go", "run", str(f.ROOT / "infra/files-local/pki.go"), str(state / "pki"))
    prefix = "spiffe://marketmesh.test/env/test/cluster/dc-a/ns/marketmesh/sa/"
    settings("auth", {"AUTH_SESSION_AUDIENCES":json.dumps({"user":["user:profile:read","user:profile:write"],"files":["files:read","files:write"]}), "AUTH_FILES_SCOPED_ENABLED":"true", "AUTH_FILES_ADDRESS":":9093", "AUTH_FILES_TLS_CERT_FILE":"/workload/auth.crt", "AUTH_FILES_TLS_KEY_FILE":"/workload/auth.key", "AUTH_FILES_CLIENT_CA_FILE":"/workload/ca.crt", "AUTH_FILES_OWN_URI":prefix+"auth", "AUTH_FILES_EXPECTED_URI":prefix+"files"})
    settings("gateway-in", {"FILES_BROWSER_ENABLED":"true"})
    settings("gateway-out", {"FILES_BROWSER_ENABLED":"true", "FILES_TARGET":"files-control:9094", "FILES_SERVER_NAME":"files", "EXPECTED_FILES_URI":prefix+"files", "FILES_TLS_CERT_FILE":"/workload/gateway-out.crt", "FILES_TLS_KEY_FILE":"/workload/gateway-out.key", "FILES_TLS_ROOT_CA_FILE":"/workload/ca.crt"})
    cfg = json.loads((f.STATE / "control.json").read_text())
    scope = {"TrustDomain":"marketmesh.test", "Environment":"test", "Cluster":"dc-a", "Namespace":"marketmesh", "ServiceAccount":"files"}
    tls = {"Certificate":"/workload/files.crt", "PrivateKey":"/workload/files.key", "RootCA":"/workload/ca.crt"}
    cfg["Control"] = {"Enabled":True, "Address":":9094", "TLS":tls, "Own":scope, "Gateway":dict(scope,ServiceAccount="gateway-out"), "AuthTLS":tls, "AuthTarget":"auth:9093", "AuthServerName":"auth", "AuthURI":prefix+"auth", "Issuer":"auth.marketmesh"}
    config = state / "files-control.json"
    config.write_text(json.dumps(cfg));config.chmod(0o600)
    workload = str(state / "pki")+":/workload:ro"
    networks = ["files-private","files-q","files-a","files-b"]
    external = {"files-private":"files-internal", "files-q":"quarantine-dmz", "files-a":"delivery-a-dmz", "files-b":"delivery-b-dmz"}
    restricted = {"user":f"{os.getuid()}:{os.getgid()}", "read_only":True, "cap_drop":["ALL"], "security_opt":["no-new-privileges:true"], "logging":{"driver":"json-file","options":{"max-size":"1m","max-file":"1"}}}
    services = {
        "auth":{"volumes":[workload]},
        "gateway-out":{"volumes":[workload]},
        "files-control":dict(restricted, image="marketmesh-mm43-files:local", environment={"FILES_CONFIG_FILE":"/config.json"}, networks=["internal",*networks], volumes=[str(config)+":/config.json:ro",workload,str(f.STATE / "pki/ca.crt")+":/pki/ca.crt:ro"]),
        "files-probe":dict(restricted, image="marketmesh-mm43-integration:local", entrypoint=["/usr/local/bin/files-integration","-test.v","-test.run=TestLiveControlThroughAuthAndTunnel","-test.timeout=10m"], environment={"FILES_FIXTURE":"/fixture","FILES_CONTROL_BASE":"https://frontdoor:8443","TMPDIR":"/work"}, networks=["dmz",*networks], tmpfs=[f"/work:rw,noexec,nosuid,size=256m,uid={os.getuid()},gid={os.getgid()},mode=700"], mem_limit="2g", volumes=[str(f.STATE)+":/fixture:ro", str(f.STATE / "pki/ca.crt")+":/pki/ca.crt:ro",str(state / "browser/ca.pem")+":/account-ca.pem:ro","files-sandbox:/run/files-sandbox:ro","files-av:/run/files-clamav:ro"]),
    }
    overlay.write_text(json.dumps({"services":services,"networks":{key:{"external":True,"name":"marketmesh-files-local_"+value} for key,value in external.items()},"volumes":{"files-sandbox":{"external":True,"name":"marketmesh-files-local_sandbox-socket"},"files-av":{"external":True,"name":"marketmesh-files-local_av-socket"}}}))
    overlay.chmod(0o600)

try:
    f.compose("stop", "worker", quiet=True)
    f.compose("build", "worker", "integration")
    prepare()
    compose("build","auth")
    compose("up","-d","--wait","--wait-timeout","150","postgres-primary","postgres-replica")
    compose("up","-d","redis","nats")
    compose("run","--rm","--no-deps","provision")
    compose("up","-d","--wait","--wait-timeout","120","auth","user")
    compose("up","-d","files-control")
    compose("up","-d","--scale","gateway-in=2","gateway-in","frontdoor")
    compose("up","-d","--wait","--wait-timeout","150","gateway-out")
    process = subprocess.Popen(command("run","--rm","--no-deps","files-probe"),cwd=f.ROOT,env=env,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    for line in process.stdout:
        print(line,end="",flush=True)
        if "MM43_TUNNEL_RESTART_POINT" in line:
            compose("restart","gateway-out")
    if process.wait():
        raise RuntimeError("control E2E failed")
except Exception:
    for service in ("files-control", "gateway-out", "auth"):
        result = subprocess.run(command("logs", "--no-color", "--tail", "40", service), cwd=f.ROOT, env=env, capture_output=True)
        target = state / (service+".diagnostic.log")
        target.write_bytes(result.stdout);target.chmod(0o600)
    print("MM-43 E2E failed; private diagnostics retained in " + str(state), file=sys.stderr)
    raise
finally:
    # Unique project generated above; external Files networks/volumes are retained.
    if overlay.is_file():
        compose("down","--volumes","--remove-orphans")
    f.compose("up", "-d", "worker", quiet=True)
