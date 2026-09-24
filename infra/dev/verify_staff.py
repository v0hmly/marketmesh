#!/usr/bin/env python3
"""Verify the actual staff corporate boundary and least-privilege shared PG access."""
import ipaddress
import json
import subprocess

import local as dev
from verify import pg, require


def check():
    dev.check_owned_state()
    containers = {}
    for name, port in (("staff", "18444/tcp"), ("staff-oidc", "18445/tcp")):
        identifier = dev.compose("ps", "-q", name, capture=True).strip()
        require(bool(identifier), "Нет сервиса " + name)
        details = json.loads(dev.run("docker", "inspect", identifier, capture=True))[0]
        bindings = details["NetworkSettings"]["Ports"].get(port)
        require(bool(bindings) and all(item["HostIp"] == "127.0.0.1" for item in bindings), "Staff порт не ограничен loopback")
        require(details["State"].get("Health", {}).get("Status") == "healthy", "Staff сервис не готов")
        containers[name] = details
    network = dev.PROJECT + "_staff-corporate"
    members = json.loads(dev.run("docker", "network", "inspect", network, capture=True))[0]["Containers"]
    require({item["Name"] for item in members.values()} == {containers[name]["Name"].lstrip("/") for name in containers}, "Публичный сервис получил корпоративную сеть")
    address = str(ipaddress.ip_address(containers["staff"]["NetworkSettings"]["Networks"][network]["IPAddress"]))
    # OrbStack SNAT can erase the caller's subnet. Without the corporate client
    # certificate, both direct and host-published paths must fail at TLS, even
    # with the correct server CA, SNI, Origin and spoofed forwarding headers.
    script = """const https=require('node:https'),fs=require('node:fs');
for(const host of [process.argv[1], 'host.docker.internal']) {
 for(const path of ['/staff','/assets/index.js','/staff.v1.StaffService/StartSso']) {
  const code=await new Promise((resolve,reject)=>{
   const req=https.request({host,port:18444,servername:'staff.localhost',path,
    ca:fs.readFileSync('/ca.crt'),method:path.includes('StaffService')?'POST':'GET',
    headers:{Host:'staff.localhost:18444',Origin:'https://staff.localhost:18444','X-Forwarded-For':'172.31.87.1','X-Real-IP':'172.31.87.1','Content-Type':'application/json'}},r=>{r.resume();reject(new Error('HTTP was reachable'))});
   req.on('error',e=>resolve(e.code));req.setTimeout(3000,()=>req.destroy());req.end('{}');
  });
  if(code!=='ERR_SSL_TLSV13_ALERT_CERTIFICATE_REQUIRED')throw new Error('Unexpected TLS result: '+code);
 }
}
"""
    script = '(async()=>{' + script + '})().catch(e=>{console.error(e.message);process.exit(3)})'
    result = subprocess.run(["docker", "run", "--rm", "--read-only", "--cap-drop=ALL", "--network", dev.PROJECT + "_ingress",
                             "-v", str(dev.STATE / "staff/public/ca.crt") + ":/ca.crt:ro", "--entrypoint", "node", "marketmesh-dev-browser", "-e", script, address], capture_output=True, text=True, timeout=30)
    require(result.returncode == 0, "Staff mTLS не отклонил внешний запрос без корпоративного сертификата: " + result.stderr.strip())
    credentials = json.loads((dev.STATE / "staff/credentials.json").read_text())
    for role in ("staff_rw", "staff_ro"):
        own = pg(role, credentials[role], "staff", "SELECT count(*) FROM staff.members", replica=role == "staff_ro")
        require(own.returncode == 0, "Нет собственного TLS/RW/RO доступа " + role)
        for database in ("auth", "user", "files", "analytics"):
            denied = pg(role, credentials[role], database, "SELECT 1")
            require(denied.returncode != 0 and "pg_hba.conf" in denied.stderr, "Нарушена межбазовая изоляция " + role)
    denied = pg("staff_ro", credentials["staff_ro"], "staff", "DELETE FROM staff.members WHERE false")
    require(denied.returncode != 0 and ("read-only" in denied.stderr or "permission denied" in denied.stderr), "RO допускает запись")
    print("Staff: loopback ports, corporate network, external static/API denial, PostgreSQL TLS/RW/RO/isolation: OK")


if __name__ == "__main__":
    try:
        check()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as error:
        raise SystemExit("staff:perimeter: " + (str(error) if isinstance(error, RuntimeError) else "проверка прервана; секреты не выведены")) from None
