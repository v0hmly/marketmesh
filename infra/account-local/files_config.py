"""Shared private Auth/User/Files configuration for local and disposable stands."""
import json


def configure(state, files_state, fresh=False):
    def settings(name, updates):
        path = state / name / "env"
        values = {line.split("=", 1)[0]: line.split("=", 1)[1].strip("'") for line in path.read_text().splitlines()}
        values.update(updates)
        path.write_text("".join(f"{key}='{value}'\n" for key, value in sorted(values.items())))
        path.chmod(0o600)

    (state / "browser/files-ca.pem").write_bytes((files_state / "pki/ca.crt").read_bytes())
    (state / "browser/combined-ca.pem").write_bytes((state / "browser/ca.pem").read_bytes() + (files_state / "pki/ca.crt").read_bytes())
    for name in ("files-ca.pem", "combined-ca.pem"):
        (state / "browser" / name).chmod(0o644)
    prefix = "spiffe://marketmesh.test/env/test/cluster/dc-a/ns/marketmesh/sa/"
    settings("auth", {"AUTH_SESSION_AUDIENCES": json.dumps({"user": ["user:profile:read", "user:profile:write", "user:addresses:read", "user:addresses:write", "user:settings:read", "user:settings:write"], "files": ["files:read", "files:write"]}), "AUTH_FILES_SCOPED_ENABLED": "true", "AUTH_FILES_ADDRESS": ":9093", "AUTH_FILES_TLS_CERT_FILE": "/workload/auth.crt", "AUTH_FILES_TLS_KEY_FILE": "/workload/auth.key", "AUTH_FILES_CLIENT_CA_FILE": "/workload/ca.crt", "AUTH_FILES_OWN_URI": prefix+"auth", "AUTH_FILES_EXPECTED_URI": prefix+"files"})
    settings("gateway-in", {"FILES_BROWSER_ENABLED": "true", "USER_AVATAR_BROWSER_ENABLED": "true"})
    settings("gateway-out", {"FILES_BROWSER_ENABLED": "true", "USER_AVATAR_BROWSER_ENABLED": "true", "FILES_TARGET": "files-control:9094", "FILES_SERVER_NAME": "files", "EXPECTED_FILES_URI": prefix+"files", "FILES_TLS_CERT_FILE": "/workload/gateway-out.crt", "FILES_TLS_KEY_FILE": "/workload/gateway-out.key", "FILES_TLS_ROOT_CA_FILE": "/workload/ca.crt"})
    settings("user", {"USER_AVATAR_ENABLED": "true", "USER_FILES_TARGET": "files-control:9095", "USER_FILES_SERVER_NAME": "files", "USER_FILES_TLS_CERT_FILE": "/workload/user.crt", "USER_FILES_TLS_KEY_FILE": "/workload/user.key", "USER_FILES_TLS_CA_FILE": "/workload/ca.crt", "USER_FILES_OWN_URI": prefix+"user", "USER_FILES_EXPECTED_URI": prefix+"files"})
    origins = ["https://quarantine:8333", "https://delivery-a:8333", "https://delivery-b:8333"] if fresh else ["https://localhost:18343", "https://localhost:18344", "https://localhost:18345"]
    cfg = json.loads((files_state / "control.json").read_text())
    cfg["Quarantine"]["PublicEndpoint"] = origins[0]
    for bucket, origin in zip(cfg["Delivery"], origins[1:]):
        bucket["PublicEndpoint"] = origin
    scope = {"TrustDomain": "marketmesh.test", "Environment": "test", "Cluster": "dc-a", "Namespace": "marketmesh", "ServiceAccount": "files"}
    tls = {"Certificate": "/workload/files.crt", "PrivateKey": "/workload/files.key", "RootCA": "/workload/ca.crt"}
    cfg["Control"] = {"Enabled": True, "Address": ":9094", "TLS": tls, "Own": scope, "Gateway": dict(scope, ServiceAccount="gateway-out"), "AuthTLS": tls, "AuthTarget": "auth:9093", "AuthServerName": "auth", "AuthURI": prefix+"auth", "Issuer": "auth.marketmesh", "Avatar": {"Enabled": True, "Address": ":9095", "User": dict(scope, ServiceAccount="user")}}
    config = state / "files-control.json"
    config.write_text(json.dumps(cfg)); config.chmod(0o600)
