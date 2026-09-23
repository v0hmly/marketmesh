#!/usr/bin/env python3
"""Check the existing shared dev; never reset or create a second database stack."""
import argparse
import json
import secrets
import shlex
import subprocess

import local as dev
import shared


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def execute(service, argv, data=''):
    identifier = dev.compose('ps', '-q', service, capture=True).strip()
    require(bool(identifier), 'Нет запущенного сервиса ' + service)
    return subprocess.run(['docker', 'exec', '-i', identifier, *argv], input=data, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)


def pg(role, secret, database, query, replica=False):
    argv = ['psql', '-X', '-v', 'ON_ERROR_STOP=1', '-At', '-h', 'pg-replica' if replica else 'pg-primary', '-U', role, '-d', database, '-c', query]
    script = 'read -r PGPASSWORD; export PGPASSWORD PGSSLMODE=verify-full PGSSLROOTCERT=/var/lib/postgresql/tls/ca.crt; exec ' + shlex.join(argv)
    return execute('postgres-replica', ['sh', '-c', script], secret + '\n')


def redis(role, secret, *command):
    script = 'read -r REDISCLI_AUTH; export REDISCLI_AUTH; exec ' + shlex.join(['redis-cli', '--raw', '--user', role, *command])
    result = execute('redis', ['sh', '-c', script], secret + '\n')
    require(result.returncode == 0, 'Redis test connection failed')
    return result.stdout.strip()


def ch(role, secret, query):
    # Send secrets via stdin, never command-line arguments or logs.
    script = 'const r=await fetch("http://clickhouse:8123/",{method:"POST",headers:{"X-ClickHouse-User":' + json.dumps(role) + ',"X-ClickHouse-Key":' + json.dumps(secret) + '},body:' + json.dumps(query) + '}); console.log(JSON.stringify({status:r.status,body:await r.text()}));'
    result = execute('analytics-backend', ['node', '--input-type=module', '-'], script)
    require(result.returncode == 0, 'ClickHouse test connection failed')
    return json.loads(result.stdout)


def check(mode):
    dev.check_owned_state()
    require(json.loads((dev.STATE / 'owner.json').read_text()).get('topology') == dev.TOPOLOGY, 'Сначала обновите dev-стенд')
    pg_env = shared.env_values(dev.ACCOUNT / 'postgres-primary/env')
    analytics = shared.env_values(dev.RYBBIT / '.env')
    private = json.loads((dev.STATE / 'shared/credentials.json').read_text())
    files = json.loads((dev.FILES / 'database.json').read_text())
    model = json.loads(dev.compose('config', '--format', 'json', capture=True))['services']
    # Count services in the canonical model and running containers; a healthy
    # endpoint alone must not hide another copy of the old database services.
    expected = {'postgres-primary', 'postgres-replica', 'redis', 'clickhouse', 'nats'}
    databases = {name for name, config in model.items() if any(config.get('image', '').startswith(prefix) for prefix in ('postgres:', 'redis:', 'clickhouse/', 'nats:'))}
    require(databases == expected, 'В Compose появился дублирующий набор БД')
    for name in expected:
        require(len(dev.compose('ps', '-q', name, capture=True).split()) == 1, 'Неверное количество контейнеров ' + name)
    rows = pg('fixture_admin', pg_env['POSTGRES_PASSWORD'], 'postgres', "SELECT sync_state || ':' || application_name FROM pg_stat_replication")
    require(rows.returncode == 0 and rows.stdout.strip() == 'sync:marketmesh_sync', 'Нет синхронной реплики')
    roles = [('auth', 'auth', 'auth.credentials'), ('user', 'user', 'users.profiles'), ('files', 'files', 'files.uploads')]
    for database, prefix, table in roles:
        for suffix in ('rw', 'ro'):
            role = prefix + '_' + suffix
            secret = files[role] if prefix == 'files' else pg_env[role.upper() + '_PASSWORD']
            own = pg(role, secret, database, 'SELECT count(*) FROM ' + table, replica=suffix == 'ro')
            require(own.returncode == 0, 'Нет собственного TLS/RW/RO доступа ' + role)
            for other in ('auth', 'user', 'files', 'analytics'):
                if other != database:
                    denied = pg(role, secret, other, 'SELECT 1')
                    require(denied.returncode != 0 and ('pg_hba.conf' in denied.stderr or 'permission denied for database' in denied.stderr), 'Межбазовая изоляция ' + role)
            if suffix == 'ro':
                denied = pg(role, secret, database, 'DELETE FROM ' + table + ' WHERE false')
                require(denied.returncode != 0 and ('read-only' in denied.stderr or 'permission denied' in denied.stderr), 'RO роль допускает запись ' + role)
    own = pg('rybbit', analytics['POSTGRES_PASSWORD'], 'analytics', 'SELECT 1')
    require(own.returncode == 0, 'Rybbit PostgreSQL TLS')
    denied = pg('rybbit', analytics['POSTGRES_PASSWORD'], 'auth', 'SELECT 1')
    require(denied.returncode != 0 and 'pg_hba.conf' in denied.stderr, 'Rybbit PostgreSQL isolation')
    require(redis('auth', private['redis_auth'], 'PING') == 'PONG', 'Redis auth authentication')
    require(redis('rybbit', analytics['REDIS_PASSWORD'], 'PING') == 'PONG', 'Redis Rybbit authentication')
    for user, password, key in [('auth', private['redis_auth'], 'session:foreign'), ('rybbit', analytics['REDIS_PASSWORD'], 'auth:session:access:foreign')]:
        require('NOPERM' in redis(user, password, 'HGET', key, 'value'), 'Redis namespace isolation ' + user)
        require('permission' in redis('platform_admin', private['redis_admin'], 'ACL', 'DRYRUN', user, 'FLUSHALL').lower(), 'Redis administrative command isolation ' + user)
        require('NOPERM' in redis(user, password, 'EVAL', "return redis.call('HGET', KEYS[1], 'probe')", '1', key), 'Redis Lua namespace isolation ' + user)
    for query in ('SELECT count(*) FROM analytics.events',):
        require(ch('rybbit_query', analytics['CLICKHOUSE_QUERY_PASSWORD'], query)['status'] == 200, 'ClickHouse query access')
    for query in ('CREATE DATABASE mm99_denied', 'CREATE USER mm99_denied'):
        response = ch('rybbit', analytics['CLICKHOUSE_PASSWORD'], query)
        require(response['status'] != 200 and 'ACCESS_DENIED' in response['body'], 'ClickHouse writer privileges too broad')
    response = ch('rybbit_query', analytics['CLICKHOUSE_QUERY_PASSWORD'], 'CREATE TABLE analytics.mm99_denied (id UInt64) ENGINE=Memory')
    require(response['status'] != 200 and any(code in response['body'] for code in ('READONLY', 'ACCESS_DENIED')), 'ClickHouse query user permits DDL')
    probe = dev.STATE / 'shared/verification.json'
    if mode == 'seed':
        marker = secrets.token_hex(16)
        require(redis('auth', private['redis_auth'], 'HSET', 'auth:session:access:mm99-persistence', 'probe', marker) in ('0', '1'), 'Redis auth write')
        require(redis('rybbit', analytics['REDIS_PASSWORD'], 'SET', 'session:mm99-persistence', marker) == 'OK', 'Redis analytics write')
        for query in ('CREATE TABLE IF NOT EXISTS analytics.mm99_persistence (marker String) ENGINE=MergeTree ORDER BY marker', "INSERT INTO analytics.mm99_persistence VALUES ('" + marker + "')"):
            require(ch('rybbit', analytics['CLICKHOUSE_PASSWORD'], query)['status'] == 200, 'ClickHouse writer own data')
        shared.write(probe, json.dumps({'marker': marker}))
    if mode in ('seed', 'check'):
        marker = json.loads(probe.read_text())['marker']
        require(redis('auth', private['redis_auth'], 'HGET', 'auth:session:access:mm99-persistence', 'probe') == marker, 'Redis auth persistence')
        require(redis('rybbit', analytics['REDIS_PASSWORD'], 'GET', 'session:mm99-persistence') == marker, 'Redis analytics persistence')
        require(marker in ch('rybbit', analytics['CLICKHOUSE_PASSWORD'], 'SELECT marker FROM analytics.mm99_persistence')['body'], 'ClickHouse persistence')
    dev.run('docker', 'run', '--rm', '--network', dev.PROJECT + '_internal', '--user', '0:0',
            '--mount', f'type=bind,src={dev.ACCOUNT / "auth"},dst=/certs,readonly',
            '--mount', f'type=bind,src={dev.ROOT / "frontend/tools/verify-dev-nats.mjs"},dst=/verify.mjs,readonly',
            '--entrypoint', 'node', 'marketmesh-dev-rybbit:local', '/verify.mjs')
    dev.run('python3', str(dev.ROOT / 'infra/rybbit/verify.py'), 'check' if mode == 'check' else 'send')
    print('MM-99: одна инфраструктура, TLS/RW/RO, ACL PostgreSQL/Redis/ClickHouse/NATS и Rybbit проверены; режим ' + mode)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', nargs='?', default='smoke', choices=('smoke', 'seed', 'check'))
    try:
        check(parser.parse_args().mode)
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as error:
        # Never dump command input, config or credentials on a failed assertion.
        raise SystemExit('dev:verify: ' + (str(error) if isinstance(error, RuntimeError) else 'проверка прервана; секреты не выведены')) from None
