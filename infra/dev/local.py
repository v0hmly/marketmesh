#!/usr/bin/env python3
"""Prepare local credentials; all containers belong to the root Compose project."""
import argparse
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.request

sys.dont_write_bytecode = True

ROOT = Path(__file__).resolve().parents[2]
os.environ["GOWORK"] = str(ROOT / "backend/go.work")
STATE = ROOT / ".cache/dev-stack"
ACCOUNT = STATE / "account"
FILES = STATE / "files"
RYBBIT = STATE / "rybbit"
TOPOLOGY = 2
PROJECT = "marketmesh-dev"
ENV = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", MM_DEV_ROOT=str(ROOT),
           COMPOSE_PROJECT_NAME=PROJECT, ACCOUNT_LOCAL_STATE=str(ACCOUNT), MM_SHARED_STATE=str(STATE / "shared"),
           ACCOUNT_LOCAL_WORKSPACE=str(ROOT), ACCOUNT_LOCAL_IMAGE="marketmesh-dev-account:local",
           ACCOUNT_LOCAL_PORT="18443", ACCOUNT_MAILPIT_PORT="18025", FIXTURE_ROOT=str(ACCOUNT),
           MM_FILES_STATE=str(FILES), MM_FILES_UID=str(os.getuid()), MM_FILES_GID=str(os.getgid()),
           MM_FILES_BROWSER_ORIGINS="https://frontdoor:8443,https://localhost:18443",
           RYBBIT_STATE=str(RYBBIT), RYBBIT_WORKSPACE=str(ROOT), RYBBIT_PROJECT=PROJECT, RYBBIT_PORT="8310")


def run(*args, env=None, cwd=ROOT, capture=False):
    return subprocess.run(args, cwd=cwd, env=env or ENV, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def compose(*args, capture=False):
    values = dict(line.split("=", 1) for line in (STATE / "compose.env").read_text().splitlines())
    return run("docker", "compose", "--project-directory", str(ROOT), "--project-name", PROJECT,
               "--env-file", str(STATE / "compose.env"), "-f", str(ROOT / "compose.yml"),
               "--profile", "*", *args, capture=capture,
               env=dict(ENV, **{key: value.strip("'") for key, value in values.items()}))


def regular(path):
    for parent in (path, *path.parents):
        if parent == ROOT:
            break
        if parent.is_symlink():
            raise RuntimeError("Символическая ссылка в локальном состоянии запрещена")


def check_owned_state(allow_incomplete=False):
    regular(STATE)
    if STATE.exists():
        for path in STATE.rglob("*"):
            regular(path)
    for kind in ("container", "volume", "network"):
        command = ("docker", "ps", "-aq") if kind == "container" else ("docker", kind, "ls", "-q")
        ids = run(*command, "--filter", "label=com.docker.compose.project=" + PROJECT, capture=True).split()
        if ids and not (STATE / "owner.json").is_file():
            raise RuntimeError("Есть ресурсы marketmesh-dev без конфигурации. Восстановите .cache/dev-stack; данные сохранены.")
        if ids and not allow_incomplete and any(not path.is_file() for path in (ACCOUNT / "ready.json", FILES / "owner.json", RYBBIT / "owner.json")):
            raise RuntimeError("Конфигурация существующего стенда неполная. Восстановите .cache/dev-stack; ключи не изменены.")
        for identifier in ids:
            field = '.Config.Labels' if kind == "container" else '.Labels'
            label = "com.docker.compose.project.working_dir" if kind == "container" else "marketmesh.dev.workspace"
            command = ("docker", "inspect") if kind == "container" else ("docker", kind, "inspect")
            labels = json.loads(run(*command, "--format", "{{json " + field + "}}", identifier, capture=True)) or {}
            if labels.get(label) != str(ROOT):
                raise RuntimeError("Ресурсы marketmesh-dev принадлежат другому стенду")
    marker = STATE / "owner.json"
    if marker.exists():
        owner = json.loads(marker.read_text())
        if owner.get("workspace") != str(ROOT) or owner.get("project") != PROJECT or set(owner) - {"workspace", "project", "topology"}:
            raise RuntimeError("Каталог состояния принадлежит другому стенду")


def check_ports(model, selected):
    required = {int(port["published"]) for name in selected for port in model[name].get("ports", []) if port.get("published")}
    owned = set()
    ids = run("docker", "ps", "-q", "--filter", "label=com.docker.compose.project=" + PROJECT, capture=True).split()
    for identifier in ids:
        ports = json.loads(run("docker", "inspect", "--format", "{{json .NetworkSettings.Ports}}", identifier, capture=True))
        owned.update(int(binding["HostPort"]) for bindings in ports.values() if bindings for binding in bindings)
    for port in required - owned:
        with socket.socket() as listener:
            try:
                listener.bind(("127.0.0.1", port))
            except OSError:
                raise RuntimeError(f"Порт {port} занят другим стендом. Остановите его перед запуском; чужие ресурсы не изменены.") from None


def expires(path):
    return not path.is_file() or subprocess.run(
        ["openssl", "x509", "-checkend", "3600", "-noout", "-in", str(path)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode != 0


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def save_env():
    # Only explicitly selected local variables; never persist the caller's environment.
    keys = ("MM_DEV_ROOT", "COMPOSE_PROJECT_NAME", "ACCOUNT_LOCAL_STATE", "ACCOUNT_LOCAL_WORKSPACE",
            "ACCOUNT_LOCAL_IMAGE", "ACCOUNT_LOCAL_PORT", "MM_SHARED_STATE", "ACCOUNT_MAILPIT_PORT", "MM_FILES_STATE",
            "MM_FILES_UID", "MM_FILES_GID", "RYBBIT_WORKSPACE", "RYBBIT_PORT", "ACCOUNT_RYBBIT_ENABLED", "RYBBIT_SITE_ID")
    values = {key: ENV[key] for key in keys if key in ENV}
    values.update(line.split("=", 1) for line in (RYBBIT / ".env").read_text().splitlines())
    # Extended observability definitions share a file with an unused legacy DB fixture.
    for key in ("POSTGRES_ADMIN_PASSWORD", "POSTGRES_APP_RW_PASSWORD", "POSTGRES_APP_RO_PASSWORD", "POSTGRES_REPLICATOR_PASSWORD", "REDIS_EDGE_PASSWORD", "REDIS_AUTH_PASSWORD", "SEAWEED_QUARANTINE_ACCESS_KEY", "SEAWEED_QUARANTINE_SECRET_KEY", "SEAWEED_PUBLIC_ACCESS_KEY", "SEAWEED_PUBLIC_SECRET_KEY", "SEAWEED_INTERNAL_ACCESS_KEY", "SEAWEED_INTERNAL_SECRET_KEY"):
        values[key] = "unused-by-dev-project"
    if any(any(char in value for char in "\n\r'$") for value in values.values()):
        raise RuntimeError("Недопустимое значение в локальной конфигурации")
    (STATE / "compose.env").write_text("".join(f"{key}='{value}'\n" for key, value in sorted(values.items())))


def prepare(force_renew=False):
    marker = STATE / "owner.json"
    if not marker.exists():
        if any(path.name != "lock" for path in STATE.iterdir()):
            raise RuntimeError("Неполное состояние: восстановите конфигурацию; автоматическая перезапись запрещена")
        marker.write_text(json.dumps({"workspace": str(ROOT), "project": PROJECT, "topology": TOPOLOGY}))
    if json.loads(marker.read_text()).get("topology") != TOPOLOGY:
        raise RuntimeError("Старый раздельный dev-стенд: выполните явный task dev:reset, затем task dev:up; переноса данных нет")
    # Stop consumers before changing trust, retain every volume and credential.
    renew_account = (ACCOUNT / "ready.json").exists() and (force_renew or expires(ACCOUNT / "browser/ca.pem") or (STATE / "account-renewing").exists())
    renew_files = (FILES / "owner.json").exists() and (force_renew or expires(FILES / "pki/quarantine.crt") or (STATE / "files-renewing").exists())
    renew_workload = (ACCOUNT / "files-pki").exists() and (force_renew or expires(ACCOUNT / "files-pki/auth.crt") or (STATE / "workload-renewing").exists())
    if renew_account or renew_files or renew_workload:
        compose("down", "--remove-orphans")
    if renew_account:
        renew_account_certificates()
    run("go", "run", "./cmd/account-local", "generate", cwd=ROOT / "backend/tools/account-local")
    f = module("files_local", "infra/files-local/local.py")
    f.PROJECT, f.STATE = PROJECT, FILES
    f.ENV = dict(ENV, GOCACHE=str(STATE / "go-build"))
    f.prepare()
    if renew_files:
        (STATE / "files-renewing").touch()
        run("go", "run", str(ROOT / "backend/fixtures/files-local/pki.go"), str(FILES / "pki"))
        (STATE / "files-renewing").unlink()
    if not (ACCOUNT / "files-pki").exists() or renew_workload:
        (STATE / "workload-renewing").touch()
        run("go", "run", str(ROOT / "backend/fixtures/files-local/pki.go"), str(ACCOUNT / "files-pki"))
        (STATE / "workload-renewing").unlink()
    f.configuration(json.loads((FILES / "credentials.json").read_text()))
    for zone in f.ZONES:
        if not (FILES / (zone + ".json")).exists():
            f.write(zone + ".json", {})  # Mount targets exist before KMS bootstrap.
    module("files_config", "infra/account-local/files_config.py").configure(ACCOUNT, FILES)
    RYBBIT.mkdir(mode=0o700, exist_ok=True)
    run(sys.executable, str(ROOT / "infra/rybbit/state.py"))
    module("shared_infra", "infra/dev/shared.py").configure(STATE, ACCOUNT, FILES, RYBBIT)
    if (RYBBIT / "site-id").exists():
        ENV["RYBBIT_SITE_ID"] = (RYBBIT / "site-id").read_text().strip()
    save_env()
    return f


def renew_account_certificates():
    # All consumers must be stopped. Copy only PKI/signing material, never DB,
    # mail/HMAC keys, passwords, env files or the owner's marker.
    marker = json.loads((ACCOUNT / "ready.json").read_text())
    if marker.get("Port") != "18443" or marker.get("Topology") != 3:
        raise RuntimeError("Версия или порт сохранённого аккаунта несовместимы")
    pending = STATE / "account-renewing"
    pending.touch(mode=0o600)
    with tempfile.TemporaryDirectory(prefix=".dev-renew-", dir=ACCOUNT.parent) as directory:
        temporary = Path(directory)
        run("go", "run", "./cmd/account-local", "generate", cwd=ROOT / "backend/tools/account-local",
            env=dict(ENV, FIXTURE_ROOT=str(temporary)))
        names = [f"{service}/{file}" for service in ("auth", "user", "gateway-in", "gateway-out", "frontdoor", "nats", "provision")
                 for file in ("cert.pem", "key.pem", "ca.pem")]
        names += ["auth/keys.json", "browser/ca.pem", "ready.json"]
        for name in names:
            target = ACCOUNT / name
            regular(target)
            staging = target.with_name(target.name + ".renew")
            regular(staging)
            shutil.copyfile(temporary / name, staging)
            staging.chmod(0o644 if name == "browser/ca.pem" else 0o600)
            staging.replace(target)
    pending.unlink()


def selection(model, targets, profiles):
    if targets:
        unknown = set(targets) - model.keys()
        if unknown:
            raise RuntimeError("Неизвестные сервисы: " + ", ".join(sorted(unknown)))
        selected = set(targets)
    else:
        selected = {name for name, config in model.items() if (not config.get("profiles") and profiles != ["infrastructure"]) or set(config.get("profiles", [])) & set(profiles)}
        if "infrastructure" in profiles:
            selected.update({"postgres-primary", "postgres-replica", "redis", "nats", "clickhouse"} & model.keys())
    pending = list(selected)
    while pending:
        name = pending.pop()
        dependencies = set(model[name].get("depends_on", {}))
        # Apply bootstrap dependencies to the transitive closure as well.
        if name in {"quarantine", "internal-clean", "delivery-a", "delivery-b", "worker", "files-control"}:
            dependencies.update({"bao", "quarantine", "internal-clean", "delivery-a", "delivery-b"})
        for dependency in dependencies:
            if dependency not in selected:
                selected.add(dependency)
                pending.append(dependency)
    return selected


def wait_http(url, ca=None):
    context = ssl.create_default_context(cafile=str(ca)) if ca else None
    for _ in range(30):
        try:
            with urllib.request.urlopen(url, context=context, timeout=5) as reply:
                if reply.status == 200:
                    return
        except (OSError, ValueError):
            pass
        time.sleep(1)
    raise RuntimeError("Не дождались готовности " + url)


def up(args):
    profiles = args.profile if args.profile is not None else ([] if args.services else ["full"])
    ENV["ACCOUNT_RYBBIT_ENABLED"] = "true" if {"full", "analytics"} & set(profiles) or any(name.startswith("analytics-") for name in args.services) else "false"
    f = prepare(args.command == "renew")
    model = json.loads(compose("config", "--format", "json", capture=True))["services"]
    selected = selection(model, args.services, profiles)
    check_ports(model, selected)
    names = sorted(selected)
    compose("build", *names)
    if "bao" in selected:
        compose("up", "-d", "bao")
        f.initialize_kms()
    if "quarantine" in selected:
        compose("up", "-d", "--force-recreate", "quarantine", "internal-clean", "delivery-a", "delivery-b")
        for attempt in range(30):
            result = subprocess.run(["go", "run", str(ROOT / "backend/fixtures/files-local/storage.go"), str(FILES)], cwd=ROOT, env=ENV,
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if result.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Не удалось настроить bucket policy/CORS")
    if "analytics-gateway" in selected:
        compose("up", "-d", "--wait", "--wait-timeout", "240", "analytics-gateway")
        run(sys.executable, str(ROOT / "infra/rybbit/bootstrap.py"))
        ENV["RYBBIT_SITE_ID"] = (RYBBIT / "site-id").read_text().strip()
        save_env()
    compose("up", "-d", "--wait", "--wait-timeout", "240", *names)
    if "frontdoor" in selected:
        wait_http("https://localhost:18443/", ACCOUNT / "browser/ca.pem")
    print("\nСервисы готовы: " + ", ".join(names))
    for service, url in {"frontdoor": "https://localhost:18443", "mailpit": "http://localhost:18025", "observability-gateway": "http://localhost:3000", "analytics-gateway": "http://localhost:8310"}.items():
        if service in selected:
            print("  " + service + ": " + url)
    if "frontdoor" in selected:
        print("Локальные CA для браузера: " + str(ACCOUNT / "browser/combined-ca.pem"))
    if "analytics-gateway" in selected:
        print("Учётная запись Rybbit: " + str(RYBBIT / "admin.json"))


def reset():
    check_owned_state(allow_incomplete=True)
    # Verify EVERY resource before the first mutation, including partial setup.
    # No wildcard deletion or Docker prune: only IDs with this project's labels.
    for kind in ("container", "network", "volume"):
        command = ("docker", "ps", "-aq") if kind == "container" else ("docker", kind, "ls", "-q")
        ids = run(*command, "--filter", "label=com.docker.compose.project=" + PROJECT, capture=True).split()
        if ids:
            removal = ("docker", "rm", "-f") if kind == "container" else ("docker", kind, "rm")
            run(*removal, *ids, capture=True)
    for path in STATE.iterdir():
        if path.name != "lock":
            if path.is_dir():
                shutil.rmtree(path)
            else:
                path.unlink()
    print("Сброшен только локальный marketmesh-dev. Следующий task dev:up создаст пустой общий стенд.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("up", "down", "status", "renew", "config", "logs", "reset", "stop"))
    parser.add_argument("--profile", choices=("full", "core", "analytics", "observability", "infrastructure"), action="append")
    parser.add_argument("services", nargs="*")
    args = parser.parse_intermixed_args()
    os.umask(0o077)
    for tool in ("docker", "go", "openssl"):
        if not shutil.which(tool):
            raise RuntimeError("Не найден обязательный инструмент: " + tool)
    run("docker", "info", "--format", "{{.ServerVersion}}", capture=True)
    regular(STATE)
    regular(STATE / "lock")
    STATE.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (STATE / "lock").open("w") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise RuntimeError("Другая команда dev уже выполняется") from None
        check_owned_state(allow_incomplete=args.command == "reset")
        if args.command == "reset":
            reset()
        elif args.command in ("up", "renew"):
            up(args)
        elif args.command == "config":
            ENV["ACCOUNT_RYBBIT_ENABLED"] = "true"
            prepare()
            compose("config", "--quiet")
        elif not (STATE / "compose.env").is_file():
            print("Стенд ещё не инициализирован; выполните task dev:up")
        elif args.command == "down":
            compose("down", "--remove-orphans")
            print("Стенд остановлен. Данные, письма и ключи сохранены.")
        elif args.command == "stop":
            if not args.services:
                raise RuntimeError("Укажите сервисы; полный стенд останавливается task dev:down")
            shared = {"postgres-primary", "postgres-replica", "redis", "nats", "clickhouse"}
            if shared & set(args.services):
                raise RuntimeError("Общие сервисы останавливаются вместе со стендом через task dev:down")
            compose("stop", *args.services)
        elif args.command == "logs":
            compose("logs", "--tail", "100", *args.services)
        else:
            compose("ps", "--all")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, subprocess.CalledProcessError, OSError, ValueError) as error:
        print("dev: " + (str(error) if isinstance(error, RuntimeError) else "запуск прерван; проверьте шаг выше и task dev:status"), file=sys.stderr)
        sys.exit(1)
