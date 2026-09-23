"""Generate private configuration for the single shared dev infrastructure."""
import hashlib
import json
import re
import secrets
from pathlib import Path

def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(value)
    path.chmod(0o600)


def read_env(path):
    return dict(line.split("=", 1) for line in path.read_text().splitlines() if line)


def env_values(path):
    return {key: value.strip("'") for key, value in read_env(path).items()}


def write_env(path, values):
    if any(any(c in value for c in "\n\r'$") for value in values.values()):
        raise RuntimeError("Unsafe generated environment")
    write(path, "".join(f"{key}='{value}'\n" for key, value in sorted(values.items())))


def password(value):
    if not re.fullmatch(r"[0-9a-f]{48,64}", value):
        raise RuntimeError("Invalid generated credential")
    return value


def pg_sql(account, files, analytics):
    roles = {"auth_rw": account["AUTH_RW_PASSWORD"], "auth_ro": account["AUTH_RO_PASSWORD"],
             "user_rw": account["USER_RW_PASSWORD"], "user_ro": account["USER_RO_PASSWORD"],
             **{name: files[name] for name in ("files_rw", "files_worker", "files_ro")},
             "rybbit": analytics["POSTGRES_PASSWORD"], "replicator": account["REPLICATOR_PASSWORD"]}
    sql = ""
    for role, secret in roles.items():
        repl = "REPLICATION" if role == "replicator" else "NOREPLICATION"
        sql += f"CREATE ROLE {role} LOGIN PASSWORD '{password(secret)}' NOSUPERUSER NOCREATEDB NOCREATEROLE {repl} NOBYPASSRLS;\n"
    for role in ("auth_ro", "user_ro", "files_ro"):
        sql += f"ALTER ROLE {role} SET default_transaction_read_only = on;\n"
    for name in ("auth", "user", "files", "analytics"):
        sql += f'CREATE DATABASE "{name}";\n'
    sql += 'REVOKE ALL ON DATABASE postgres, template1, auth, "user", files, analytics FROM PUBLIC;\n'
    for database, users in (("auth", "auth_rw,auth_ro"), ("user", "user_rw,user_ro"),
                            ("files", "files_rw,files_worker,files_ro"), ("analytics", "rybbit")):
        sql += f'GRANT CONNECT ON DATABASE "{database}" TO {users};\n\\connect "{database}"\nREVOKE ALL ON SCHEMA public FROM PUBLIC;\n'
    # Rybbit runs its own migrations. It owns only this database; the role has
    # no cluster, replication, role-management or cross-database privileges.
    sql += 'ALTER DATABASE analytics OWNER TO rybbit;\nALTER SCHEMA public OWNER TO rybbit;\n'
    return sql


def redis_config(auth, analytics, health, admin):
    connection = "+ping +hello +client|setinfo +client|setname"
    scripts = "+eval +evalsha +script|load +script|exists"
    return f'''bind 0.0.0.0
protected-mode yes
dir /data
appendonly yes
appendfsync everysec
save ""
maxmemory 256mb
maxmemory-policy noeviction
user default off
user auth on >{password(auth)} ~auth:session:access:* -@all {connection} {scripts} +@hash +pexpireat
user rybbit on >{password(analytics)} ~session:* ~sticky:* ~rl:* ~bot:* ~feature-flags:* -@all {connection} {scripts} +info +@string +@hash +@sortedset +@hyperloglog +@transaction +del +unlink +exists +expire +pexpire +ttl +pttl
user health on >{password(health)} -@all +ping
user platform_admin on >{password(admin)} ~* &* +@all
'''


def clickhouse_users(analytics, admin):
    def user(name, secret, profile, grant):
        digest = hashlib.sha256(password(secret).encode()).hexdigest()
        return f"""<{name}><password_sha256_hex>{digest}</password_sha256_hex>
<networks><ip>::/0</ip></networks><profile>{profile}</profile><quota>default</quota>
<grants><query>{grant}</query></grants></{name}>"""
    return '''<clickhouse>
<profiles>
 <default><enable_json_type>1</enable_json_type><async_insert>1</async_insert><wait_for_async_insert>1</wait_for_async_insert><max_memory_usage>536870912</max_memory_usage><max_threads>2</max_threads><log_queries>0</log_queries></default>
 <rybbit_query><readonly>2</readonly><enable_json_type>1</enable_json_type><max_memory_usage>536870912</max_memory_usage><max_threads>2</max_threads><max_execution_time>10</max_execution_time><max_result_rows>1000</max_result_rows><result_overflow_mode>break</result_overflow_mode><max_concurrent_queries_for_user>8</max_concurrent_queries_for_user>
 <constraints><readonly><readonly/></readonly><max_memory_usage><readonly/></max_memory_usage><max_threads><readonly/></max_threads><max_execution_time><readonly/></max_execution_time><max_result_rows><readonly/></max_result_rows><result_overflow_mode><readonly/></result_overflow_mode><max_concurrent_queries_for_user><readonly/></max_concurrent_queries_for_user></constraints></rybbit_query>
</profiles>
<users><default remove="remove"/>
''' + user("platform_admin", admin, "default", "GRANT ALL ON *.* WITH GRANT OPTION") + user(
        "rybbit", analytics["CLICKHOUSE_PASSWORD"], "default", "GRANT SELECT, INSERT, ALTER, CREATE TABLE, CREATE VIEW, DROP TABLE, DROP VIEW, TRUNCATE, OPTIMIZE ON analytics.*") + user(
        "rybbit_query", analytics["CLICKHOUSE_QUERY_PASSWORD"], "rybbit_query", "GRANT SELECT ON analytics.events") + "\n</users>\n</clickhouse>\n"


def configure(state, account, files, rybbit):
    shared = state / "shared"
    shared.mkdir(mode=0o700, exist_ok=True)
    credentials = shared / "credentials.json"
    if not credentials.exists():
        if list(shared.iterdir()):
            raise RuntimeError("Incomplete shared infrastructure credentials; explicit reset required")
        write(credentials, json.dumps({name: secrets.token_hex(32) for name in
                                      ("redis_auth", "redis_health", "redis_admin", "clickhouse_admin")}))
    private = json.loads(credentials.read_text())
    for value in private.values():
        password(value)
    pg = env_values(account / "postgres-primary/env")
    analytics = env_values(rybbit / ".env")
    file_passwords = json.loads((files / "database.json").read_text())
    write(shared / "postgres-init.sql", pg_sql(pg, file_passwords, analytics))
    write(shared / "replication.pgpass", "pg-primary:5432:*:replicator:" + password(pg["REPLICATOR_PASSWORD"]) + "\n")
    write_env(shared / "postgres.env", {"POSTGRES_USER": "fixture_admin", "POSTGRES_DB": "postgres", "POSTGRES_PASSWORD": password(pg["POSTGRES_PASSWORD"]), "PGDATA": "/var/lib/postgresql/18/primary"})
    write(shared / "redis.conf", redis_config(private["redis_auth"], analytics["REDIS_PASSWORD"], private["redis_health"], private["redis_admin"]))
    write_env(shared / "redis-health.env", {"REDISCLI_AUTH": private["redis_health"]})
    write_env(shared / "clickhouse.env", {"CLICKHOUSE_SKIP_USER_SETUP": "1", "CLICKHOUSE_USER": "platform_admin", "CLICKHOUSE_PASSWORD": private["clickhouse_admin"]})
    write(shared / "clickhouse-users.xml", clickhouse_users(analytics, private["clickhouse_admin"]))
    ca = (files / "pki/ca.crt").read_text()
    for service in ("auth", "user", "provision"):
        values = env_values(account / service / "env")
        for key, value in values.items():
            if key.endswith("_DSN"):
                value = value.replace("postgres-primary:", "pg-primary:").replace("postgres-replica:", "pg-replica:")
                value = value.replace("sslmode=disable", "sslmode=verify-full&sslrootcert=/secrets/db-ca.crt")
                values[key] = value
        if service == "auth":
            values.update(AUTH_REDIS_USERNAME="auth", AUTH_REDIS_PASSWORD=private["redis_auth"],
                          AUTH_REDIS_PLAINTEXT_REASON="shared dev Redis on isolated internal network with per-application ACLs")
        write(account / service / "db-ca.crt", ca)
        write_env(account / service / "env", values)
    # A named user is supported by the pinned image's small build-time adapter.
    write_env(shared / "rybbit.env", {"POSTGRES_HOST": "pg-primary", "POSTGRES_PASSWORD": analytics["POSTGRES_PASSWORD"],
        "PGSSL": "verify-full", "PGSSLMODE": "verify-full", "PGSSLROOTCERT": "/pki/ca.crt", "NODE_EXTRA_CA_CERTS": "/pki/ca.crt", "REDIS_HOST": "redis", "REDIS_USERNAME": "rybbit", "REDIS_PASSWORD": analytics["REDIS_PASSWORD"],
        "CLICKHOUSE_HOST": "http://clickhouse:8123", "CLICKHOUSE_USER": "rybbit", "CLICKHOUSE_PASSWORD": analytics["CLICKHOUSE_PASSWORD"],
        "CLICKHOUSE_QUERY_USER": "rybbit_query", "CLICKHOUSE_QUERY_PASSWORD": analytics["CLICKHOUSE_QUERY_PASSWORD"], "CLICKHOUSE_QUERY_MANAGED_EXTERNALLY": "true"})
