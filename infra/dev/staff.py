"""Local staff/IdP credentials, TLS and provisioning on the existing PG cluster."""
import base64
import hashlib
import json
import secrets
import subprocess


def write(path, value):
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.write_text(value)
    path.chmod(0o600)


def certificates(directory, renew):
    marker = directory / "renewing"
    valid = not marker.exists() and all(cert.is_file() and subprocess.run(
        ["openssl", "x509", "-checkend", "3600", "-noout", "-in", str(cert)],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
        for cert in (directory / name for name in ("ca.crt", "staff.crt", "oidc.crt", "browser.crt", "health.crt")))
    if valid and not renew:
        return
    marker.touch(mode=0o600)
    def openssl(*args):
        subprocess.run(["openssl", *args], cwd=directory, check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    # Fixture-private CA; never alter the system keychain automatically.
    openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-sha256", "-days", "7",
            "-subj", "/CN=MarketMesh local staff CA", "-keyout", "ca.key", "-out", "ca.crt",
            "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign")
    for name in ("staff", "oidc", "browser", "health"):
        openssl("req", "-new", "-newkey", "rsa:2048", "-nodes", "-subj", f"/CN={name}.localhost",
                "-keyout", f"{name}.key", "-out", f"{name}.csr")
        usage = "clientAuth" if name in ("browser", "health") else "serverAuth"
        write(directory / "ext.cnf", f"subjectAltName=DNS:{name}.localhost\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage={usage}\n")
        openssl("x509", "-req", "-in", f"{name}.csr", "-CA", "ca.crt", "-CAkey", "ca.key", "-CAcreateserial",
                "-days", "7", "-sha256", "-extfile", "ext.cnf", "-out", f"{name}.crt")
    openssl("pkcs12", "-export", "-in", "browser.crt", "-inkey", "browser.key", "-certfile", "ca.crt",
            "-name", "MarketMesh local corporate access", "-out", "browser.p12", "-passout", "pass:")
    for path in directory.glob("*"):
        if path.is_file():
            path.chmod(0o600)
    marker.unlink()


def configure(state, account, files, shared, renew=False):
    directory = state / "staff"
    directory.mkdir(mode=0o700, exist_ok=True)
    credentials = directory / "credentials.json"
    if not credentials.exists():
        if list(directory.iterdir()):
            raise RuntimeError("Неполное состояние staff: восстановите credentials.json")
        write(credentials, json.dumps({name: secrets.token_hex(32) for name in
              ("staff_rw", "staff_ro", "client_secret", "employee_password", "other_password")}))
    values = json.loads(credentials.read_text())
    for value in values.values():
        shared.password(value)
    certificates(directory, renew)
    write(directory / "public/ca.crt", (directory / "ca.crt").read_text())
    (directory / "public/ca.crt").chmod(0o644)
    users = [{"Subject": "local-employee", "Email": "employee@marketmesh.test", "Name": "Тестовый сотрудник", "Password": values["employee_password"]},
             {"Subject": "local-other", "Email": "other@marketmesh.test", "Name": "Другой сотрудник", "Password": values["other_password"]}]
    # Separate mounts: neither IdP nor staff receives the other's private keys,
    # test passwords, database admin credential or CA signing key.
    for name in ("service", "idp"):
        (directory / name).mkdir(mode=0o700, exist_ok=True)
        endpoint = "staff" if name == "service" else "oidc"
        for source, target in ((f"{endpoint}.crt", "cert.pem"), (f"{endpoint}.key", "key.pem"), ("ca.crt", "ca.pem")):
            write(directory / name / target, (directory / source).read_text())
    for name in ("health.crt", "health.key"):
        write(directory / "service" / name, (directory / name).read_text())
    write(directory / "service/db-ca.crt", (files / "pki/ca.crt").read_text())
    write(directory / "service/config.json", json.dumps({"Listen": ":18444", "Origin": "https://staff.localhost:18444",
          "Issuer": "https://oidc.localhost:18445", "ClientID": "marketmesh-staff-local", "ClientSecret": values["client_secret"],
          "CA": "/secrets/ca.pem", "ClientCA": "/secrets/ca.pem", "Cert": "/secrets/cert.pem", "Key": "/secrets/key.pem", "Site": "/site",
          "DSN": f"postgres://staff_rw:{values['staff_rw']}@pg-primary:5432/staff?sslmode=verify-full&sslrootcert=/secrets/db-ca.crt",
          "CorporateNetworks": ["172.31.87.0/24", "127.0.0.0/8", "::1/128"], "IdleSeconds": 1800, "LifetimeSeconds": 28800}))
    write(directory / "idp/config.json", json.dumps({"ClientID": "marketmesh-staff-local", "ClientSecret": values["client_secret"],
          "Cert": "/secrets/cert.pem", "Key": "/secrets/key.pem", "Users": users}))
    sql = ""
    for role in ("staff_rw", "staff_ro"):
        sql += f"DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='{role}') THEN CREATE ROLE {role} LOGIN PASSWORD '{values[role]}' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS; END IF; END $$;\n"
    sql += "ALTER ROLE staff_ro SET default_transaction_read_only=on;\nSELECT 'CREATE DATABASE staff' WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname='staff')\\gexec\nREVOKE ALL ON DATABASE staff FROM PUBLIC;\nGRANT CONNECT ON DATABASE staff TO staff_rw,staff_ro;\n\\connect staff\nREVOKE ALL ON SCHEMA public FROM PUBLIC;\n"
    write(state / "shared/staff-init.sql", sql)
    provision = shared.env_values(account / "provision/env")
    pg = shared.env_values(account / "postgres-primary/env")
    provision["STAFF_ADMIN_DSN"] = f"postgres://fixture_admin:{shared.password(pg['POSTGRES_PASSWORD'])}@pg-primary:5432/staff?sslmode=verify-full&sslrootcert=/secrets/db-ca.crt"
    shared.write_env(account / "provision/env", provision)


def invitation(state, email, role):
    """Generate a fresh local test invitation without storing its raw token in SQL."""
    if email not in ("employee@marketmesh.test", "other@marketmesh.test") or role not in ("support", "moderator", "admin"):
        raise RuntimeError("Локальный invite допускает только тестовые аккаунты и известные роли")
    token = secrets.token_urlsafe(32)
    digest = base64.urlsafe_b64encode(hashlib.sha256(token.encode()).digest()).decode().rstrip("=")
    sql = f"INSERT INTO staff.invites VALUES ('{digest}','{email}','{role}','Local administrator',now()+interval '24 hours',NULL);\n"
    return sql, "https://staff.localhost:18444/staff/invite#token=" + token
