"""Одноразовые Files workloads поверх публичного MM-44 targets API.

Все credentials синтетические. Конфигурация и диагностика остаются в .cache
(0700/0600); subprocess никогда не печатает env, signed URL или SQL с паролями.
"""
import copy
import http.client
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import socket
import ssl
import subprocess
import tarfile
import time

ROOT = Path(__file__).resolve().parents[2]
os.environ["GOWORK"] = str(ROOT / "backend/go.work")
LOCAL = ROOT / "infra/files-local"
NAMESPACE = "mm43-files"
REMOTE = "/var/lib/mm43-files"
PG = "postgres@sha256:1c59e2c3c818eaa0f0628f695b36e7c9e362d6b219b36a54a32df645cbd7e1af"
S3 = "chrislusf/seaweedfs@sha256:43b768cd62b00d132439cda881b93fd1adebf1b315e996e794087743821d771d"
BAO = "quay.io/openbao/openbao@sha256:11fd73a2102cda9c55d5d881a8c3210303146a7ec1e8ac76f526e175c6d24641"
REDIS = "redis:8.10.1-alpine3.23"
ACCOUNT = "marketmesh-mm43-account:local"
FILES = "marketmesh-mm43-files:local"
AV = "marketmesh-mm43-antivirus:local"
CDR = "marketmesh-mm43-sandbox:local"
ZONES = {"quarantine": ("dc-a-dmz", 8333), "delivery-a": ("dc-a-dmz", 8334),
         "delivery-b": ("dc-b-dmz", 8335), "internal-clean": ("dc-a-internal", 8333)}


class OwnershipError(Exception):
    """Нарушение identity не является повторяемой ошибкой доступности API."""


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(value if isinstance(value, str) else json.dumps(value))
    path.chmod(0o600)


def envfile(path):
    return {k: shlex.split(v)[0] for k, v in (line.split("=", 1) for line in path.read_text().splitlines())}


# Files fault-domain tests deliberately use the bounded password/session fixture.
# Email/2FA is tested through account-local + Mailpit, not this isolated schema.
LEGACY_AUTH_MIGRATIONS = (
    "000001_credentials.up.sql",
    "000002_sessions.up.sql",
    "000003_registration_outbox.up.sql",
)
LEGACY_AUTH_FILES = frozenset(("env", "keys.json", "cert.pem", "key.pem", "ca.pem"))


def legacy_auth_env(path):
    auth = {key: value for key, value in envfile(path).items()
            if not key.startswith(("AUTH_EMAIL_", "AUTH_SMTP_"))}
    auth.update(AUTH_EMAIL_ENABLED="false", AUTH_REGISTRATION_EVENTS_ENABLED="false",
                AUTH_REGISTRATION_PUBLISH_ENABLED="false")
    return auth


def legacy_auth_schema(root):
    return "\n".join((root / name).read_text() for name in LEGACY_AUTH_MIGRATIONS)


class Fixture:
    def __init__(self, instance):
        self.instance = instance
        self.state = ROOT / ".cache/files-dc-e2e" / instance
        self.state.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.state.chmod(0o700)
        self.primary = "dc-a"
        self.snapshots = {}
        self.pods = {}
        self.image_refs = {}
        self.log = self.state / "dependencies.log"
        self.log.touch(mode=0o600)
        self.topology_bin = self.state / "topology"
        self.run("go", "build", "-o", str(self.topology_bin), "./backend/tools/e2e-topology", timeout=180)

    def run(self, *args, input=None, timeout=120, env=None, check=True):
        result = subprocess.run(args, cwd=ROOT, env=env, input=input, capture_output=True, timeout=timeout)
        with self.log.open("ab") as log:
            log.write(result.stdout + result.stderr)
        if check and result.returncode:
            raise RuntimeError("dependency failed: " + Path(args[0]).name)
        return result

    def topology(self, *args, value=None):
        result = self.run(str(self.topology_bin), "--instance", self.instance, *args,
                          input=None if value is None else json.dumps(value).encode(), timeout=900)
        return json.loads(result.stdout) if result.stdout.strip() else None

    def bind(self):
        # A new fixture may consume only a freshly validated, owned MM-44 topology.
        for dc in ("dc-a", "dc-b"):
            for zone in ("internal", "dmz"):
                node = dc + "-" + zone
                self.snapshots[node] = self.topology("targets", "resolve", "--consumer-task", "MM-43",
                    "--consumer-run-id", self.instance, "--dc", dc, "--zone", zone)
        write(self.state / "targets.json", self.snapshots)

    def target(self, node):
        return self.snapshots[node]["targets"][0]

    def validate(self, node, state="running"):
        # iproute2 may produce an incomplete netlink JSON snapshot while a pod
        # interface disappears. Retry the whole read-only proof; never change the
        # expected binding or run a guest command without a successful proof.
        for attempt in range(3):
            try:
                return self.topology("targets", "validate", "--snapshot", "-", "--expected-state", state,
                    "--target", node, value=self.snapshots[node])
            except RuntimeError:
                if attempt == 2:
                    raise
                time.sleep(.5)

    def vm(self, node, *args, **kwargs):
        self.validate(node)
        machine = self.target(node)["machine"]
        # Immutable ID prevents a replacement with the same name; the in-guest
        # boot check closes the restart window between validation and execution.
        return self.run("orbctl", "run", "-m", machine["id"], "sudo", "-n", "sh", "-ec",
            'test "$(cat /proc/sys/kernel/random/boot_id)" = "$1"; shift; exec "$@"',
            "mm43-identity-guard", machine["boot_id"], *args, **kwargs)

    def kubectl(self, node, *args, **kwargs):
        return self.vm(node, "k3s", "kubectl", "-n", NAMESPACE, *args, **kwargs)

    def ip(self, node):
        return self.target(node)["machine"]["ipv4"]

    def copy_tree(self, node, source, destination):
        self.validate(node)
        data = io.BytesIO()
        with tarfile.open(fileobj=data, mode="w") as archive:
            for path in source.rglob("*"):
                if path.is_file():
                    archive.add(path, arcname=str(path.relative_to(source)), recursive=False)
        self.vm(node, "mkdir", "-p", destination)
        self.vm(node, "tar", "-xpf", "-", "-C", destination, input=data.getvalue())
        self.vm(node, "chown", "-R", "10001:10001", destination)
        self.vm(node, "chmod", "700", destination)

    def aliases(self, node):
        dc = node[:4]
        other = "dc-b" if self.primary == "dc-a" else "dc-a"
        names = {"pg-primary": self.ip(self.primary + "-internal"), "pg-replica": "127.0.0.1",
                 "redis": self.ip(self.primary + "-internal"), "auth": "127.0.0.1", "files": "127.0.0.1",
                 "user": "127.0.0.1", "gateway-in": self.ip(dc + "-dmz"), "bao": "127.0.0.1"}
        names.update({zone: self.ip(location) for zone, (location, _) in ZONES.items()})
        grouped={}
        for name,ip in names.items(): grouped.setdefault(ip,[]).append(name)
        return [{"ip":ip,"hostnames":hostnames} for ip,hostnames in grouped.items()]

    def pod(self, node, name, image, command=None, args=None, env=None, mounts=None, root=False,
            memory="256Mi", parser=False):
        labels = {"marketmesh.task": "MM-43", "marketmesh.run": self.instance, "app": name}
        security = {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True,
                    "capabilities": {"drop": ["ALL"]}, "runAsUser": 10001, "runAsGroup": 10001}
        if root:
            # Only database init copies TLS files and drops to the image's postgres UID.
            security = {"allowPrivilegeEscalation": False, "runAsUser": 0,
                        "capabilities": {"drop": ["ALL"], "add": ["CHOWN", "FOWNER", "DAC_OVERRIDE", "SETUID", "SETGID"]}}
        container = {"name": name, "image": self.image_refs.get(image, image), "imagePullPolicy": "Never", "securityContext": security,
                     "resources": {"requests": {"cpu": "25m", "memory": "32Mi"}, "limits": {"cpu": "2", "memory": memory}},
                     "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}, {"name": "work", "mountPath": "/work"}]}
        if command is not None:
            container["command"] = command
        if args is not None:
            container["args"] = args
        if env:
            container["env"] = [{"name": k, "value": v} for k, v in env.items()]
        volumes = [{"name": n, "emptyDir": {"medium": "Memory", "sizeLimit": size}}
                   for n, size in (("tmp", "32Mi"), ("work", "600Mi"))]
        for index, (source, target, readonly) in enumerate(mounts or []):
            volume = "mount-" + str(index)
            volumes.append({"name": volume, "hostPath": {"path": REMOTE + "/" + source, "type": ""}})
            container["volumeMounts"].append({"name": volume, "mountPath": target, "readOnly": readonly})
        spec = {"hostNetwork": not parser, "dnsPolicy": "Default", "hostAliases": self.aliases(node),
                "automountServiceAccountToken": False, "terminationGracePeriodSeconds": 15,
                "securityContext": {"seccompProfile": {"type": "RuntimeDefault"}},
                "containers": [container], "volumes": volumes}
        if parser:
            labels["files-parser"] = "true"
        result = {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": name, "namespace": NAMESPACE, "labels": labels, "annotations": {"marketmesh.source-image": image}}, "spec": spec}
        self.pods[(node, name)] = result
        return result

    def apply(self, node, pod):
        self.kubectl(node, "apply", "-f", "-", input=json.dumps(pod).encode())

    def restart(self, node, name):
        self.validate(node)
        self.delete_pod(node,name)
        pod = self.pods[(node, name)]
        pod["spec"]["hostAliases"] = self.aliases(node)
        self.apply(node, pod)

    def delete_pod(self,node,name):
        # A watch can stall while the k3s API recovers after a VM restart. A
        # successful DELETE is followed by fresh GETs, never by force deletion
        # or treating an API/ownership error as proof of absence.
        def get():
            result=self.kubectl(node,"get","pod",name,"--ignore-not-found","-o","json","--request-timeout=5s",timeout=15)
            return json.loads(result.stdout) if result.stdout.strip() else None
        pod=get()
        if pod is None:return
        labels=pod["metadata"].get("labels",{})
        if labels.get("marketmesh.task")!="MM-43" or labels.get("marketmesh.run")!=self.instance:
            raise RuntimeError("refusing unowned pod deletion")
        uid=pod["metadata"]["uid"]
        self.kubectl(node,"delete","--raw","/api/v1/namespaces/"+NAMESPACE+"/pods/"+name,"-f","-","--request-timeout=10s",
            input=json.dumps({"apiVersion":"v1","kind":"DeleteOptions","preconditions":{"uid":uid}}).encode(),timeout=20)
        def absent():
            current=get()
            if current is None:return True
            if current["metadata"]["uid"]!=uid:raise OwnershipError("pod identity changed while deleting")
            return False
        self.wait(absent,"owned pod deletion "+name,90)

    def wait(self, check, label, timeout=120):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            try:
                if check():
                    return
            except (RuntimeError, OSError, http.client.HTTPException):
                pass
            time.sleep(1)
        raise RuntimeError("readiness timeout: " + label)

    def sql(self, dc, sql):
        result = self.kubectl(dc + "-internal", "exec", "-i", "pg", "--", "gosu", "postgres", "psql", "-XAt",
                              "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "files", input=sql.encode())
        return result.stdout.decode().strip()

    def redis(self, dc, command):
        # Credentials are read inside the container, never placed in command argv.
        return self.kubectl(dc + "-internal", "exec", "-i", "redis", "--", "sh", "-ec",
            'export REDISCLI_AUTH="$(cat /config/password)"; exec redis-cli --raw --tls --cacert /config/pki/ca.crt --sni redis', input=(command+"\n").encode()).stdout.decode().strip()

    def routing(self, dc):
        addresses = {name: self.ip(node) for name, (node, _) in ZONES.items()}
        addresses["frontdoor"] = self.ip(dc + "-dmz")
        write(self.state / "routing.json", addresses)

    def firewall(self, node):
        # Exact additional dependency flows for this disposable Files consumer.
        # The baseline MM-44 chains are retained. DMZ cannot initiate internal connections.
        self.validate(node)
        for source in ("dc-a-internal", "dc-b-internal"):
            ports = [5432, 6379] if node.endswith("internal") else [p for n, p in ZONES.values() if n == node]
            if source == node:
                continue
            for port in sorted(set(ports)):
                rule = ["-s", self.ip(source), "-p", "tcp", "--dport", str(port), "-m", "comment",
                        "--comment", "MM-43:" + self.instance, "-j", "ACCEPT"]
                present = self.vm(node, "iptables", "-w", "5", "-C", "INPUT", *rule, check=False)
                if present.returncode:
                    self.vm(node, "iptables", "-w", "5", "-I", "INPUT", "1", *rule)

    def tree_hash(self):
        digest=hashlib.sha256()
        paths=self.run("git","ls-files","-z","--cached","--others","--exclude-standard").stdout.split(b"\0")
        for raw in sorted(set(paths)):
            if raw:
                path=ROOT / os.fsdecode(raw)
                digest.update(raw+b"\0")
                digest.update(path.read_bytes() if path.is_file() else b"<deleted>")
        return digest.hexdigest()

    def images(self):
        source_tree=self.tree_hash()
        balancer=self.state / "tunnel-balancer"
        self.run("go","build","-o",str(balancer),"backend/fixtures/files-dc-e2e/tunnel_balancer.go",
            env=dict(os.environ,GOOS="linux",CGO_ENABLED="0"),timeout=180)
        for node in self.snapshots:
            if node.endswith("dmz"):
                target=self.state / "nodes" / node / "tunnel" / "balancer"
                target.parent.mkdir(parents=True,exist_ok=True,mode=0o700)
                shutil.copyfile(balancer,target)
                target.chmod(0o755)
        reverse={alias:source for source,alias in self.image_refs.items()}
        for pod in self.pods.values():
            annotations=pod["metadata"].setdefault("annotations",{})
            if "marketmesh.source-image" not in annotations:
                annotations["marketmesh.source-image"]=reverse[pod["spec"]["containers"][0]["image"]]
        for target,image in (("runtime",FILES),("sandbox",CDR),("antivirus",AV)):
            self.run("docker","build","-f",str(LOCAL / "Dockerfile"),"--target",target,"-t",image,".",timeout=900)
        self.run("docker","build","-f","infra/account-local/Dockerfile","-t",ACCOUNT,".",timeout=900)
        provenance={"source_tree_sha256":source_tree,"images":{},"balancer_sha256":hashlib.sha256(balancer.read_bytes()).hexdigest()}
        selected = {node: {ACCOUNT} for node in self.snapshots}
        for node in selected:
            selected[node].update({PG, REDIS, FILES} if node.endswith("internal") else {S3, BAO})
        selected["dc-a-internal"].update({S3, BAO, AV, CDR})
        for image in sorted(set.union(*selected.values())):
            archive = self.state / ("image-" + image.replace("/", "_").replace(":", "_").replace("@", "_") + ".tar")
            if self.run("docker", "image", "inspect", image, check=False).returncode:
                self.run("docker", "pull", image, timeout=600)
            image_id = json.loads(self.run("docker", "image", "inspect", image).stdout)[0]["Id"]
            alias = self.instance + "-" + image_id.removeprefix("sha256:")[:16] + ":local"
            self.run("docker", "tag", image, alias)
            self.image_refs[image] = alias
            self.run("docker", "save", "-o", str(archive), alias, timeout=300)
            # With Docker's containerd store `inspect.Id` may identify the
            # manifest index. CRI image ID identifies the selected image config.
            with tarfile.open(archive) as saved:
                manifests=json.load(saved.extractfile("manifest.json"))
                matching=[entry for entry in manifests if alias in (entry.get("RepoTags") or [])]
                if len(matching) != 1:
                    raise RuntimeError("ambiguous saved image configuration")
                config=saved.extractfile(matching[0]["Config"]).read()
                config_id="sha256:"+hashlib.sha256(config).hexdigest()
            provenance["images"][image] = {"image_id":image_id,"config_id":config_id,"local_reference":alias}
            for node, images in selected.items():
                if image in images:
                    self.validate(node)
                    # The macOS workspace mount is read only input; runtime data is on each VM's disk.
                    self.vm(node, "k3s", "ctr", "images", "import", "/mnt/mac" + str(archive), timeout=300)
            archive.unlink()
        if self.tree_hash() != source_tree:
            raise RuntimeError("source tree changed during image build")
        write(self.state / "image-refs.json", self.image_refs)
        write(self.state / "build.json",provenance)

    def prepare(self):
        spec = importlib.util.spec_from_file_location("files_local_dc", LOCAL / "local.py")
        f = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(f)
        f.STATE = self.state / "files"
        f.PROJECT = "mm43-dc-" + self.instance
        f.ENV = dict(os.environ, GOCACHE=str(self.state / "go-build"))
        f.prepare()
        self.files = f
        self.run("go", "run", str(ROOT / "backend/fixtures/files-local/pki.go"), str(f.STATE / "pki"), "dc-a", timeout=120)
        account = self.state / "account"
        self.run("go", "run", "./backend/tools/account-local/cmd/account-local", "generate",
                 env=dict(os.environ, FIXTURE_ROOT=str(account), ACCOUNT_LOCAL_PORT="8443"), timeout=120)
        for dc in ("dc-a", "dc-b"):
            self.run("go", "run", str(ROOT / "backend/fixtures/files-local/pki.go"), str(self.state / dc / "workload"), dc)
        self.run("go", "run", str(ROOT / "backend/fixtures/files-local/pki.go"), str(self.state / "redis-pki"), "dc-a")
        self.credentials = json.loads((f.STATE / "credentials.json").read_text())
        f.configuration(self.credentials)
        self.passwords = json.loads((f.STATE / "database.json").read_text())
        self.redis_password = envfile(account / "auth/env")["AUTH_REDIS_PASSWORD"]
        for node in self.snapshots:
            node_dir = self.state / "nodes" / node
            node_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
            dc = node[:4]
            if node.endswith("internal"):
                for service in ("auth", "gateway-out"):
                    for path in (account / service).glob("*"):
                        if service == "auth" and path.name not in LEGACY_AUTH_FILES:
                            continue
                        write(node_dir / service / path.name, path.read_text())
                    for path in (self.state / dc / "workload").glob("*"):
                        # Files/gateway/Auth workload pairs are mounted separately below.
                        if path.name in ("ca.crt", service + ".crt", service + ".key"):
                            write(node_dir / service / "workload" / path.name, path.read_text())
                for path in (self.state / dc / "workload").glob("*"):
                    if path.name in ("ca.crt", "files.crt", "files.key"):
                        write(node_dir / "files/workload" / path.name, path.read_text())
                self.app_config(node, node_dir, account)
                for name in ("ca.crt", "pg-primary.crt", "pg-primary.key"):
                    write(node_dir / "pg/pki" / name, (f.STATE / "pki" / name).read_text())
                write(node_dir / "pg/admin-password", self.passwords["postgres"])
                write(node_dir / "pg/replication.pgpass", "pg-primary:5432:*:files_replicator:" + self.passwords["files_replicator"] + "\n")
                write(node_dir / "pg/role", "primary" if dc == "dc-a" else "replica")
                write(node_dir / "pg/start.sh", (Path(__file__).with_name("postgres.sh")).read_text())
                for name in ("redis.crt", "redis.key", "ca.crt"):
                    write(node_dir / "redis/pki" / name, (self.state / "redis-pki" / name).read_text())
                write(node_dir / "redis/password", self.redis_password)
                replica = "" if dc == "dc-a" else "replicaof " + self.ip("dc-a-internal") + " 6379\n"
                write(node_dir / "redis/redis.conf", "bind 0.0.0.0\nport 0\ntls-port 6379\ntls-cert-file /config/pki/redis.crt\ntls-key-file /config/pki/redis.key\ntls-ca-cert-file /config/pki/ca.crt\ntls-auth-clients no\ntls-replication yes\nprotected-mode yes\nrequirepass " + self.redis_password + "\nmasterauth " + self.redis_password + "\nappendonly yes\nappendfsync always\ndir /data\n" + replica)
            else:
                for service in ("gateway-in", "frontdoor"):
                    for path in (account / service).glob("*"):
                        if path.is_file():
                            write(node_dir / service / path.name, path.read_text())
                values = envfile(node_dir / "gateway-in/env")
                values.update(GRPC_ADDRESS="127.0.0.1:30444", DATA_CENTER=dc, FILES_BROWSER_ENABLED="true",
                              USER_BROWSER_ENABLED="false", USER_ADDRESSES_BROWSER_ENABLED="false", USER_SETTINGS_BROWSER_ENABLED="false")
                write(node_dir / "gateway-in/env", "".join(k+"="+shlex.quote(v)+"\n" for k,v in values.items()))
                shutil.copytree(node_dir / "gateway-in",node_dir / "gateway-in-2",dirs_exist_ok=True)
                values.update(GRPC_ADDRESS="127.0.0.1:30445",HTTP_ADDRESS="127.0.0.1:8084")
                write(node_dir / "gateway-in-2/env", "".join(k+"="+shlex.quote(v)+"\n" for k,v in values.items()))
            if any(location == node for location, _ in ZONES.values()):
                for name in ("bao.crt", "bao.key", "ca.crt"):
                    write(node_dir / "bao/pki" / name, (f.STATE / "pki" / name).read_text())
                write(node_dir / "bao/bao.hcl", (f.STATE / "bao.hcl").read_text())
        self.database_init(account)

    def app_config(self, node, node_dir, account):
        dc = node[:4]
        prefix = "spiffe://marketmesh.test/env/test/cluster/" + dc + "/ns/marketmesh/sa/"
        auth = legacy_auth_env(account / "auth/env")
        auth.update(HTTP_ADDRESS=":8081", AUTH_REGISTRATION_EVENTS_ENABLED="false", AUTH_REGISTRATION_PUBLISH_ENABLED="false",
                    AUTH_FILES_SCOPED_ENABLED="true", AUTH_FILES_ADDRESS=":9093", AUTH_ACCESS_TTL="60m", AUTH_REDIS_CONNECT_TIMEOUT="5s",
                    AUTH_FILES_TLS_CERT_FILE="/secrets/workload/auth.crt", AUTH_FILES_TLS_KEY_FILE="/secrets/workload/auth.key",
                    AUTH_FILES_CLIENT_CA_FILE="/secrets/workload/ca.crt", AUTH_FILES_OWN_URI=prefix+"auth", AUTH_FILES_EXPECTED_URI=prefix+"files",
                    AUTH_SESSION_AUDIENCES=json.dumps({"files": ["files:read", "files:write"]}))
        auth.pop("AUTH_REDIS_PLAINTEXT_REASON", None)
        auth.update(AUTH_REDIS_TLS_SERVER_NAME="redis", AUTH_REDIS_CA_FILE="/secrets/redis-ca.crt")
        write(node_dir / "auth/redis-ca.crt", (self.state / "redis-pki/ca.crt").read_text())
        db = envfile(account / "postgres-primary/env")
        for suffix in ("RW", "RO"):
            role = "auth_" + suffix.lower()
            auth["POSTGRES_"+suffix+"_DSN"] = "postgres://"+role+":"+db["AUTH_"+suffix+"_PASSWORD"]+"@pg-primary:5432/auth?sslmode=verify-full&sslrootcert=/secrets/storage-ca.crt" + ("&synchronous_commit=remote_apply" if suffix == "RW" else "")
        auth["POSTGRES_RO_DSN"] = auth["POSTGRES_RO_DSN"].replace("pg-primary:5432", "pg-replica:5433")
        write(node_dir / "auth/storage-ca.crt", (self.files.STATE / "pki/ca.crt").read_text())
        write(node_dir / "auth/env", "".join(k+"="+shlex.quote(v)+"\n" for k,v in auth.items()))
        gateway = envfile(account / "gateway-out/env")
        gateway.update(HTTP_ADDRESS=":8082", INTERNAL_TARGET="auth:9091", INTERNAL_SERVER_NAME="auth",
                       EXPECTED_INTERNAL_URI="spiffe://marketmesh.test/test/auth", GATEWAY_IN_TARGET="gateway-in:30443", FILES_BROWSER_ENABLED="true",
                       USER_BROWSER_ENABLED="false", USER_ADDRESSES_BROWSER_ENABLED="false", USER_SETTINGS_BROWSER_ENABLED="false",
                       FILES_TARGET="files:9094", FILES_SERVER_NAME="files", EXPECTED_FILES_URI=prefix+"files",
                       FILES_TLS_CERT_FILE="/secrets/workload/gateway-out.crt", FILES_TLS_KEY_FILE="/secrets/workload/gateway-out.key",
                       FILES_TLS_ROOT_CA_FILE="/secrets/workload/ca.crt")
        write(node_dir / "gateway-out/env", "".join(k+"="+shlex.quote(v)+"\n" for k,v in gateway.items()))
        cfg = json.loads((self.files.STATE / "control.json").read_text())
        cfg["DatabaseRO"] = cfg["DatabaseRO"].replace(":5432/", ":5433/")
        for zone in [cfg["Quarantine"], *cfg["Delivery"]]:
            for key in ("APIEndpoint", "PublicEndpoint"):
                zone[key] = "https://" + zone["Bucket"] + ":" + str(ZONES[zone["Bucket"]][1])
        scope = {"TrustDomain": "marketmesh.test", "Environment": "test", "Cluster": dc, "Namespace": "marketmesh", "ServiceAccount": "files"}
        tls = {"Certificate": "/config/workload/files.crt", "PrivateKey": "/config/workload/files.key", "RootCA": "/config/workload/ca.crt"}
        cfg["Control"] = {"Enabled": True, "Address": ":9094", "TLS": tls, "Own": scope, "Gateway": dict(scope, ServiceAccount="gateway-out"),
                          "AuthTLS": tls, "AuthTarget": "auth:9093", "AuthServerName": "auth", "AuthURI": prefix+"auth", "Issuer": "auth.marketmesh"}
        write(node_dir / "files/config.json", cfg)
        write(node_dir / "files/storage-ca.crt", (self.files.STATE / "pki/ca.crt").read_text())
        if dc == "dc-a":
            worker = json.loads((self.files.STATE / "worker.json").read_text())
            worker["DatabaseRO"] = worker["DatabaseRO"].replace(":5432/", ":5433/")
            for zone in [worker["Quarantine"], worker["Internal"], *worker["Delivery"]]:
                for key in ("APIEndpoint", "PublicEndpoint"):
                    zone[key] = "https://"+zone["Bucket"]+":"+str(ZONES[zone["Bucket"]][1])
            write(node_dir / "worker/config.json", worker)
            write(node_dir / "worker/storage-ca.crt", (self.files.STATE / "pki/ca.crt").read_text())

    def database_init(self, account):
        directory = self.state / "nodes/dc-a-internal/pg"
        db = envfile(account / "postgres-primary/env")
        sql = (self.files.STATE / "db-init.sql").read_text()
        sql += "CREATE DATABASE auth;\nREVOKE ALL ON DATABASE auth FROM PUBLIC;\n"
        for role in ("rw", "ro"):
            sql += "CREATE ROLE auth_" + role + " LOGIN PASSWORD '" + db["AUTH_"+role.upper()+"_PASSWORD"] + "';\n"
        sql += "ALTER ROLE auth_ro SET default_transaction_read_only=on;\nGRANT CONNECT ON DATABASE auth TO auth_rw,auth_ro;\n"
        write(directory / "db-init.sql", sql)
        write(directory / "files-migration.sql", (ROOT / "backend/services/files/migrations/000001_files.up.sql").read_text())
        auth = legacy_auth_schema(ROOT / "backend/services/auth/migrations")
        auth += "\nGRANT USAGE ON SCHEMA auth TO auth_rw,auth_ro; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA auth TO auth_rw; GRANT SELECT ON ALL TABLES IN SCHEMA auth TO auth_ro; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA auth TO auth_rw;\n"
        write(directory / "auth-migrations.sql", auth)
        init = (LOCAL / "primary-init.sh").read_text().replace("/run/files-init.sql", "/config/db-init.sql").replace("/migrations/000001_files.up.sql", "/config/files-migration.sql")
        init += '\npsql -v ON_ERROR_STOP=1 -U postgres -d auth -f /config/auth-migrations.sql >/dev/null\n'
        init += 'printf "hostssl auth auth_rw all scram-sha-256\\nhostssl auth auth_ro all scram-sha-256\\n" | cat - "$PGDATA/pg_hba.conf" > "$PGDATA/hba.new"\nmv "$PGDATA/hba.new" "$PGDATA/pg_hba.conf"\n'
        write(directory / "init.sh", init)

    def install(self):
        # This VM belongs exclusively to the fixture. A persistent kubelet
        # setting bounds the parser's entire pod, including after VM recovery.
        node="dc-a-internal"
        self.vm(node,"sh","-ec",'test ! -e /etc/rancher/k3s/config.yaml.d/43-files.yaml; install -d -m 700 /etc/rancher/k3s/config.yaml.d; umask 077; cat > /etc/rancher/k3s/config.yaml.d/43-files.yaml',
            input=b'kubelet-arg+:\n  - "pod-max-pids=128"\n')
        self.vm(node,"systemctl","restart","k3s",timeout=120)
        self.wait(lambda:self.vm(node,"k3s","kubectl","get","nodes",check=False).returncode==0,"k3s with pod PID limit")
        for node in self.snapshots:
            self.validate(node)
            ns = self.vm(node, "k3s", "kubectl", "get", "ns", NAMESPACE, "-o", "json", check=False)
            if ns.returncode == 0:
                labels = json.loads(ns.stdout)["metadata"].get("labels", {})
                if labels.get("marketmesh.run") != self.instance or labels.get("marketmesh.task") != "MM-43":
                    raise RuntimeError("refusing unowned namespace")
            else:
                self.vm(node, "k3s", "kubectl", "apply", "-f", "-", input=json.dumps({"apiVersion":"v1", "kind":"Namespace",
                    "metadata":{"name":NAMESPACE, "labels":{"marketmesh.task":"MM-43", "marketmesh.run":self.instance}}}).encode())
            self.copy_tree(node, self.state / "nodes" / node, REMOTE)
            for name in ("pg", "pg-local", "redis", "bao", "quarantine", "internal-clean", "delivery-a", "delivery-b", "av", "sandbox", "signatures"):
                self.vm(node, "install", "-d", "-o", "10001", "-g", "10001", "-m", "700", REMOTE + "/data/" + name)
            if node.endswith("internal"):
                self.vm(node, "chown", "-R", "999:999", REMOTE + "/pg", REMOTE + "/data/pg")
            self.firewall(node)
        self.kubectl("dc-a-internal", "apply", "-f", "-", input=json.dumps({"apiVersion":"networking.k8s.io/v1", "kind":"NetworkPolicy",
            "metadata":{"name":"parser-no-network", "namespace":NAMESPACE}, "spec":{"podSelector":{"matchLabels":{"files-parser":"true"}},
            "policyTypes":["Ingress","Egress"], "ingress":[], "egress":[]}}).encode())

    def bao_request(self, node, path, body=None, token=None, method=None):
        context = ssl.create_default_context(cafile=str(self.files.STATE / "pki/ca.crt"))
        connection = http.client.HTTPSConnection("bao", 8200, context=context, timeout=10)
        connection._create_connection = lambda address, timeout, *unused: socket.create_connection((self.ip(node), 8200), timeout)
        headers = {"Content-Type": "application/json"}
        if token:
            headers["X-Vault-Token"] = token
        connection.request(method or ("POST" if body is not None else "GET"), "/v1/"+path,
                           body=None if body is None else json.dumps(body), headers=headers)
        response = connection.getresponse()
        raw = response.read(1024*1024)
        connection.close()
        if response.status not in (200, 204):
            raise RuntimeError("KMS request failed")
        return json.loads(raw) if raw else None

    def start_kms(self):
        f = self.files
        for node in ("dc-a-internal", "dc-a-dmz", "dc-b-dmz"):
            self.apply(node, self.pod(node, "bao", BAO, args=["server", "-config=/run/bao.hcl"], mounts=[
                ("bao/bao.hcl", "/run/bao.hcl", True), ("bao/pki/bao.crt", "/pki/server.crt", True),
                ("bao/pki/bao.key", "/pki/server.key", True), ("data/bao", "/data", False)]))
            self.wait(lambda: self.bao_request(node, "sys/init") is not None, "KMS " + node)
            f.bao = lambda *args, **kwargs: self.bao_request(node, *args, **kwargs)
            saved = self.state / "kms" / (node + ".json")
            if saved.exists():
                write(f.STATE / "bao-init.json", saved.read_text())
            else:
                (f.STATE / "bao-init.json").unlink(missing_ok=True)
            f.ZONES = tuple(zone for zone, (location, _) in ZONES.items() if node == location)
            f.initialize_kms()
            write(saved, (f.STATE / "bao-init.json").read_text())
            for zone in f.ZONES:
                directory = self.state / "nodes" / node / zone
                write(directory / "s3.json", (f.STATE / (zone+".json")).read_text())
                write(directory / "filer.toml", (f.STATE / "filer.toml").read_text())
                for name in (zone+".crt", zone+".key", "ca.crt"):
                    write(directory / "pki" / name, (f.STATE / "pki" / name).read_text())
                self.copy_tree(node, directory, REMOTE + "/" + zone)
                port = ZONES[zone][1]
                offset = port - 8333
                args = ["mini", "-dir=/data", "-ip=127.0.0.1", "-ip.bind=0.0.0.0", "-master.telemetry=false",
                    "-master.volumeSizeLimitMB=128", "-bucket="+zone, "-s3.config=/config/s3.json",
                    "-s3.cert.file=/pki/server.crt", "-s3.key.file=/pki/server.key", "-s3.port="+str(port),
                    "-master.port="+str(9333+offset), "-filer.port="+str(8888+offset), "-volume.port="+str(9340+offset),
                    "-s3.port.iceberg=0", "-webdav=false", "-admin.ui=false"]
                self.apply(node, self.pod(node, zone, S3, args=args, memory="512Mi", mounts=[
                    (zone, "/config", True), (zone+"/filer.toml", "/etc/seaweedfs/filer.toml", True),
                    (zone+"/pki/"+zone+".crt", "/pki/server.crt", True), (zone+"/pki/"+zone+".key", "/pki/server.key", True),
                    (zone+"/pki/ca.crt", "/pki/ca.crt", True), ("data/"+zone, "/data", False)]))
        f.ZONES = tuple(ZONES)

    def start_database(self):
        for dc in ("dc-a", "dc-b"):
            node = dc + "-internal"
            mounts = [("pg", "/config", True), ("data/pg", "/data", False)]
            if dc == "dc-a":
                mounts.append(("pg/init.sh", "/docker-entrypoint-initdb.d/01-files.sh", True))
            self.apply(node, self.pod(node, "pg", PG, command=["bash", "/config/start.sh"],
                env={"POSTGRES_PASSWORD_FILE":"/config/admin-password", "PGDATA":"/data/pg"},
                mounts=mounts, root=True, memory="384Mi"))
            self.wait(lambda: self.sql(dc, "SELECT 1") == "1", "PostgreSQL " + dc)
            self.apply(node, self.pod(node, "redis", REDIS, command=["redis-server", "/config/redis.conf"],
                mounts=[("redis", "/config", True), ("data/redis", "/data", False)]))
            self.wait(lambda: self.redis(dc, "PING") == "PONG", "Redis " + dc)
        self.wait_sync()
        for dc in ("dc-a", "dc-b"):
            self.local_replica(dc)

    def wait_sync(self):
        self.wait(lambda: self.sql(self.primary, "SELECT count(*) FROM pg_stat_replication WHERE application_name='files_ro' AND state='streaming' AND sync_state='sync'") == "1", "remote_apply standby")
        other = "dc-b" if self.primary == "dc-a" else "dc-a"
        if self.sql(self.primary, "SELECT pg_is_in_recovery()") != "f" or self.sql(other, "SELECT pg_is_in_recovery()") != "t":
            raise RuntimeError("expected exactly one writable PostgreSQL node")
        self.wait(lambda: "master_link_status:up" in self.redis(other, "INFO replication"), "Redis standby")

    def start_apps(self):
        for dc in ("dc-a", "dc-b"):
            internal, dmz = dc+"-internal", dc+"-dmz"
            for service in ("auth", "gateway-out"):
                pod = self.pod(internal, service, ACCOUNT, args=[service], mounts=[(service, "/secrets", True)])
                self.apply(internal, pod)
            self.apply(internal, self.pod(internal, "files", FILES, env={"FILES_CONFIG_FILE":"/config/config.json"}, mounts=[
                ("files", "/config", True), ("files/storage-ca.crt", "/pki/ca.crt", True)]))
            self.start_tunnels(dc)
            self.apply(dmz, self.pod(dmz, "frontdoor", ACCOUNT, command=["account-local", "frontdoor"], mounts=[("frontdoor", "/secrets", True)]))
        self.routing(self.primary)

    def start_tunnels(self, dc):
        dmz=dc+"-dmz"
        for name in ("gateway-in","gateway-in-2"):
            self.apply(dmz,self.pod(dmz,name,ACCOUNT,args=["gateway-in"],env={"HOSTNAME":dc+"-"+name},mounts=[(name,"/secrets",True)]))
        self.apply(dmz,self.pod(dmz,"tunnel-balancer",ACCOUNT,command=["/config/balancer"],mounts=[("tunnel","/config",True)]))

    def start_worker(self):
        node = "dc-a-internal"
        # Refresh signatures in a separate disposable networked job. The scanners
        # themselves have deny-all NetworkPolicy and no credentials/config mounts.
        directory = self.state / "nodes" / node / "freshclam"
        write(directory / "freshclam.conf", (self.files.STATE / "freshclam.conf").read_text())
        self.copy_tree(node, directory, REMOTE + "/freshclam")
        pod = self.pod(node, "signatures", AV, command=["freshclam", "--config-file=/config/freshclam.conf", "--stdout"],
                       mounts=[("freshclam", "/config", True), ("data/signatures", "/signatures", False)], memory="2Gi")
        pod["spec"]["restartPolicy"] = "Never"
        self.apply(node, pod)
        self.wait(lambda: json.loads(self.kubectl(node, "get", "pod", "signatures", "-o", "json").stdout)["status"]["phase"] == "Succeeded", "AV signatures", 300)
        self.apply(node, self.pod(node, "antivirus", AV, parser=True, memory="2Gi", mounts=[
            ("data/signatures", "/signatures", True), ("data/av", "/run/files-clamav", False)]))
        self.apply(node, self.pod(node, "sandbox", CDR, parser=True, memory="1Gi", mounts=[("data/sandbox", "/run/files-sandbox", False)]))
        self.wait(lambda: all(json.loads(self.kubectl(node,"get","pod",name,"-o","json").stdout)["status"]["phase"]=="Running"
            for name in ("antivirus","sandbox")),"isolated parsers")
        self.verify_parsers()
        self.apply(node, self.pod(node, "worker", FILES, env={"FILES_CONFIG_FILE":"/config/config.json"}, memory="512Mi", mounts=[
            ("worker", "/config", True), ("worker/storage-ca.crt", "/pki/ca.crt", True),
            ("data/av", "/run/files-clamav", True), ("data/sandbox", "/run/files-sandbox", True)]))

    def verify_parsers(self):
        node="dc-a-internal"
        control=self.pod(node,"network-control",AV,parser=True,command=["sleep","300"])
        del control["metadata"]["labels"]["files-parser"]
        self.apply(node,control)
        self.wait(lambda:json.loads(self.kubectl(node,"get","pod","network-control","-o","json").stdout)["status"]["phase"]=="Running","network control pod")
        try:
            self.check_parsers(node)
        finally:
            self.delete_pod(node,"network-control")
            self.pods.pop((node,"network-control"),None)

    def check_parsers(self,node):
        for name in ("antivirus","sandbox"):
            pod=json.loads(self.kubectl(node,"get","pod",name,"-o","json").stdout)
            cid=pod["status"]["containerStatuses"][0]["containerID"].removeprefix("containerd://")
            runtime=json.loads(self.vm(node,"k3s","crictl","inspect",cid).stdout)
            pid=int(runtime["info"]["pid"])
            if pid <= 0: raise RuntimeError("parser PID unavailable")
            # The container cgroup may say max; its pod ancestor must be 128.
            limit=self.vm(node,"sh","-ec",'p=/sys/fs/cgroup$(cut -d: -f3 /proc/$1/cgroup); cat "$(dirname "$p")/pids.max"',"pid-limit",str(pid)).stdout.strip()
            if limit != b"128": raise RuntimeError("parser pod PID limit not enforced")
            for target,port in ((self.ip(node),"5432"),(self.ip("dc-b-internal"),"5432"),(self.ip("dc-b-dmz"),"8335")):
                script='exec 3<>/dev/tcp/$1/$2'
                self.kubectl(node,"exec","network-control","--","timeout","3","bash","-ec",script,"network-control",target,port)
                denied=self.kubectl(node,"exec",name,"--","timeout","3","bash","-ec",script,"network-denied",target,port,check=False)
                if denied.returncode not in (1,124): raise RuntimeError("parser network isolation not enforced")
                if denied.returncode==1 and not any(reason in denied.stderr for reason in (b"Operation not permitted",b"No route to host",b"Network is unreachable",b"Permission denied",b"Connection refused")):
                    raise RuntimeError("network probe failed without proving isolation")
                self.kubectl(node,"exec","network-control","--","timeout","3","bash","-ec",script,"network-control",target,port)

    def ready(self, dc):
        def check():
            request = http.client.HTTPConnection(self.ip(dc+"-internal"), 8082, timeout=3)
            request.request("GET", "/readyz")
            response = request.getresponse()
            response.read()
            request.close()
            return response.status == 204
        self.wait(check, "Files/Auth/tunnel " + dc, 150)

    def save(self):
        write(self.state / "fixture.json", {"instance":self.instance, "primary":self.primary,
            "pods":[[node, name, pod] for (node,name),pod in self.pods.items()]})
        write(self.state / "targets.json", self.snapshots)

    def load(self):
        data = json.loads((self.state / "fixture.json").read_text())
        if data["instance"] != self.instance:
            raise RuntimeError("fixture ownership mismatch")
        self.primary = data["primary"]
        self.pods = {(node,name):pod for node,name,pod in data["pods"]}
        self.snapshots = json.loads((self.state / "targets.json").read_text())
        if (self.state / "image-refs.json").exists():
            self.image_refs = json.loads((self.state / "image-refs.json").read_text())
        spec = importlib.util.spec_from_file_location("files_local_dc", LOCAL / "local.py")
        self.files = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.files)
        self.files.STATE = self.state / "files"
        self.files.PROJECT = "mm43-dc-" + self.instance
        self.credentials = json.loads((self.files.STATE / "credentials.json").read_text())
        self.passwords = json.loads((self.files.STATE / "database.json").read_text())
        self.redis_password = envfile(self.state / "account/auth/env")["AUTH_REDIS_PASSWORD"]

    def local_replica(self, dc, rebuild=False):
        # Platform pools require a real hot standby for RO; this local asynchronous
        # reader is never a substitute for the remote files_ro durability peer.
        node=dc+"-internal"
        directory=self.state / "nodes" / node / "pg-local"
        for path in (self.state / "nodes" / node / "pg").rglob("*"):
            if path.is_file():
                write(directory / path.relative_to(self.state / "nodes" / node / "pg"),path.read_text())
        write(directory / "role","replica")
        write(directory / "start.sh",Path(__file__).with_name("postgres.sh").read_text())
        self.copy_tree(node,directory,REMOTE+"/pg-local")
        self.vm(node,"install","-d","-o","999","-g","999","-m","700",REMOTE+"/data/pg-local")
        self.vm(node,"chown","-R","999:999",REMOTE+"/pg-local")
        if rebuild:
            self.delete_pod(node,"pg-local")
            self.validate(node)
            self.vm(node,"rm","-rf",REMOTE+"/data/pg-local/pg")
        self.apply(node,self.pod(node,"pg-local",PG,command=["bash","/config/start.sh"],
            env={"PGDATA":"/data/pg","PGPORT":"5433","PGAPPNAME":"files_local_"+dc[-1]},
            mounts=[("pg-local","/config",True),("data/pg-local","/data",False)],root=True,memory="256Mi"))
        self.wait(lambda: self.kubectl(node,"exec","pg-local","--","gosu","postgres","psql","-XAt","-p","5433","-U","postgres","-d","files","-c","SELECT pg_is_in_recovery()").stdout.strip()==b"t","local RO replica")

    def verify_images(self, build):
        for node in self.snapshots:
            if node.endswith("dmz"):
                digest=self.vm(node,"sha256sum",REMOTE+"/tunnel/balancer").stdout.decode().split()[0]
                if digest != build["balancer_sha256"]:
                    raise RuntimeError("running balancer differs from built helper")
        checked=set()
        for (node,name),pod in self.pods.items():
            source=pod["metadata"]["annotations"]["marketmesh.source-image"]
            expected=build["images"][source]
            live=json.loads(self.kubectl(node,"get","pod",name,"-o","json").stdout)
            if live["metadata"]["labels"].get("marketmesh.run") != self.instance or live["spec"]["containers"][0]["image"] != expected["local_reference"]:
                raise RuntimeError("running workload differs from built image")
            if (node,source) not in checked:
                actual=json.loads(self.vm(node,"k3s","crictl","inspecti",expected["local_reference"]).stdout)
                if actual["status"]["id"] != expected["config_id"]:
                    raise RuntimeError("container runtime image content mismatch")
                checked.add((node,source))
