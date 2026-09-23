#!/usr/bin/env python3
"""Миграции и подтверждение commit при отказе синхронной реплики MM-43."""
import importlib.util
from pathlib import Path
import secrets
import subprocess
import time

spec = importlib.util.spec_from_file_location("files_local", Path(__file__).with_name("local.py"))
f = importlib.util.module_from_spec(spec)
spec.loader.exec_module(f)
f.existing()
base = ["docker", "compose", "--project-name", f.PROJECT, "-f", str(f.ROOT / "infra/files-local/compose.yml")]
database = "mm43_migration_" + secrets.token_hex(8)
created = False
pending = None


def sql(service, statement, db="postgres", asynchronous=False):
    args = base + ["exec", "-T", service, "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", db, "-c", statement]
    if asynchronous:
        return subprocess.Popen(args, env=f.ENV, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    result = subprocess.run(args, env=f.ENV, capture_output=True, timeout=60)
    if result.returncode:
        raise RuntimeError("database check failed")
    return result.stdout.decode().strip()


try:
    sql("pg-primary", "CREATE DATABASE " + database)
    created = True
    for direction in ("up", "down", "up"):
        migration = (f.ROOT / "backend/services/files/migrations" / ("000001_files." + direction + ".sql")).read_text()
        sql("pg-primary", migration, database)
    sql("pg-primary", "CREATE TABLE probe (id integer PRIMARY KEY)", database)
    f.compose("stop", "pg-replica", quiet=True)
    pending = sql("pg-primary", "SET synchronous_commit=remote_apply; INSERT INTO probe VALUES (1)", database, asynchronous=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if pending.poll() is not None:
            raise RuntimeError("write acknowledged without synchronous replica")
        waits = sql("pg-primary", "SELECT count(*) FROM pg_stat_activity WHERE datname='" + database + "' AND wait_event='SyncRep'")
        if waits == "1":
            break
        time.sleep(0.1)
    else:
        raise RuntimeError("write did not reach synchronous wait")
    # Observed server-side SyncRep wait, not merely a slow client connection.
    f.compose("up", "-d", "--wait", "pg-replica", quiet=True)
    pending.communicate(timeout=60)
    if pending.returncode:
        raise RuntimeError("write failed after replica recovery")
    if sql("pg-replica", "SELECT count(*) FROM probe", database) != "1":
        raise RuntimeError("acknowledged row absent on synchronous replica")
    print("MM-43: up/down/up migrations and synchronous replica outage passed")
finally:
    f.compose("up", "-d", "--wait", "pg-replica", quiet=True)
    if pending is not None and pending.poll() is None:
        pending.communicate(timeout=60)
    if created:
        sql("pg-primary", "DROP DATABASE " + database + " WITH (FORCE)")
