#!/usr/bin/env python3
"""Полный отказ логического Files DC на четырёх одноразовых OrbStack VM."""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess
import sys
import time
import traceback

from fixture import Fixture, REMOTE, ROOT, write


class DCTest:
    def __init__(self, fixture):
        self.f = fixture
        self.stopped = {}
        self.phases = []

    def role(self, dc, role):
        if dc not in ("dc-a", "dc-b") or role not in ("fenced", "primary", "replica"):
            raise RuntimeError("invalid role transition")
        node = dc + "-internal"
        self.f.validate(node)
        self.f.vm(node, "sh", "-ec", 'umask 077; cat > /var/lib/mm43-files/pg/role.new; chown 999:999 /var/lib/mm43-files/pg/role.new; mv /var/lib/mm43-files/pg/role.new /var/lib/mm43-files/pg/role; sync -f /var/lib/mm43-files/pg/role; sync -f /var/lib/mm43-files/pg', input=role.encode())

    def fault(self, dc):
        f = self.f
        if dc != f.primary or self.stopped:
            raise RuntimeError("fault must target the sole current primary DC")
        peer = "dc-b" if dc == "dc-a" else "dc-a"
        f.wait_sync()
        # Verify the access-token cache has reached its replica before the fault;
        # canonical session/revocation state is already remote_apply PostgreSQL.
        def cache_synced():
            def info(which):
                return dict(line.split(":",1) for line in f.redis(which,"INFO replication").splitlines() if ":" in line)
            before, after = info(dc), info(peer)
            return before.get("master_repl_offset") == after.get("slave_repl_offset")
        f.wait(cache_synced, "access cache replication")
        for zone in ("internal", "dmz"):
            f.validate(dc+"-"+zone)
        self.role(dc, "fenced")
        write(f.state / "transition.json", {"phase":"fencing", "from":dc, "to":peer})
        # Never use bare `orbctl stop`, --all or --force: those affect unrelated
        # resources. Exact machine IDs are checked again immediately beforehand.
        for zone in ("internal", "dmz"):
            node = dc+"-"+zone
            f.validate(node)
            f.run("orbctl", "stop", f.target(node)["machine"]["id"], timeout=120)
            self.stopped[node] = f.validate(node, "stopped")
        write(f.state / (dc+"-stopped.json"), self.stopped)
        # A failed identity/fence proof aborts before any promotion.
        for node in self.stopped:
            f.validate(node, "stopped")
        if f.sql(peer,"SELECT pg_is_in_recovery()") != "t":
            raise RuntimeError("promotion target is not a standby")
        if f.sql(peer,"SELECT pg_promote(true, 30)") != "t":
            raise RuntimeError("standby promotion failed")
        self.role(peer,"primary")
        f.redis(peer,"REPLICAOF NO ONE")
        f.primary = peer
        f.local_replica(peer, rebuild=True)
        for name in ("auth", "files", "gateway-out"):
            f.restart(peer+"-internal", name)
        f.routing(peer)
        f.ready(peer)
        if f.sql(peer,"SHOW synchronous_standby_names") != "FIRST 1 (files_ro)":
            raise RuntimeError("synchronous policy changed during promotion")
        if f.sql(peer,"SELECT count(*) FROM pg_stat_replication WHERE application_name='files_ro'") != "0":
            raise RuntimeError("failed DC still participates in replication")
        f.save()
        write(f.state / "transition.json", {"phase":"promoted", "from":dc, "to":peer})

    def restore(self, dc):
        f = self.f
        if set(self.stopped) != {dc+"-internal",dc+"-dmz"} or f.primary == dc:
            raise RuntimeError("invalid restore transition")
        # Rebind rebuilds peer-specific firewall rules and requires all owned
        # machines running. Start both fenced VMs before rebinding either one.
        for node in self.stopped:
            f.validate(node,"stopped")
            f.run("orbctl", "start", f.target(node)["machine"]["id"], timeout=120)
        self.rejoin(dc)

    def rejoin(self, dc):
        f=self.f
        if set(self.stopped) != {dc+"-internal",dc+"-dmz"} or f.primary == dc:
            raise RuntimeError("invalid rejoin transition")
        for node,receipt in self.stopped.items():
            def rebind():
                result = f.topology("targets","rebind","--transition","-","--target",node,
                    value={"snapshot":f.snapshots[node],"stopped_receipt":receipt})
                f.snapshots[node] = result["snapshot"]
                write(f.state / (node+"-rebind.json"),result["transition"])
                return True
            f.wait(rebind,"VM ownership after restart",120)
            f.firewall(node)
        # The old primary starts only its fenced shell. Verify no database process
        # is accepting SQL before removing this fixture's obsolete data directory.
        node = dc+"-internal"
        f.wait(lambda: json.loads(f.kubectl(node,"get","pod","pg","-o","json").stdout)["status"]["phase"] == "Running", "fenced postgres pod")
        f.wait(lambda: f.kubectl(node,"exec","pg","--","bash","-ec",
            'test "$(cat /config/role)" = fenced; test "$(cat /data/fenced-boot-id)" = "$(cat /proc/sys/kernel/random/boot_id)"; for p in /proc/[0-9]*/comm; do test "$(cat "$p" 2>/dev/null || true)" != postgres; done', check=False).returncode == 0,
            "positive persistent fence proof", 30)
        unavailable = f.kubectl(node,"exec","pg","--","pg_isready","-h","127.0.0.1", check=False)
        if unavailable.returncode != 2:
            raise RuntimeError("old primary fence did not prove absence of a PostgreSQL listener")
        f.delete_pod(node,"pg")
        f.validate(node)
        # This directory belongs to this disposable fixture, never dev data. A
        # fresh basebackup from the promoted timeline also proves usable recovery.
        f.vm(node,"rm","-rf",REMOTE+"/data/pg/pg")
        self.role(dc,"replica")
        pg = f.pods[(node,"pg")]
        pg["spec"]["hostAliases"] = f.aliases(node)
        f.apply(node,pg)
        f.wait(lambda: f.sql(dc,"SELECT pg_is_in_recovery()") == "t", "rebuilt standby",180)
        f.redis(dc,"REPLICAOF "+f.ip(f.primary+"-internal")+" 6379")
        f.wait_sync()
        f.local_replica(dc, rebuild=True)
        # Every stopped KMS starts sealed; unseal only with this fixture's own key.
        for zone in ("internal","dmz"):
            location=dc+"-"+zone
            saved=f.state / "kms" / (location+".json")
            if saved.exists():
                key=json.loads(saved.read_text())["keys"][0]
                f.wait(lambda: not f.bao_request(location,"sys/unseal",{"key":key},method="PUT")["sealed"],"restored KMS")
        for current in ("dc-a","dc-b"):
            for name in ("auth","files","gateway-out"):
                f.restart(current+"-internal",name)
        f.verify_parsers()
        f.restart("dc-a-internal","worker")
        f.ready("dc-a")
        f.ready("dc-b")
        f.routing(f.primary)
        self.stopped={}
        f.save()
        write(f.state / "transition.json", {"phase":"recovered", "primary":f.primary, "standby":dc})

    def test(self):
        f=self.f
        tested_tree=f.tree_hash()
        build=json.loads((f.state / "build.json").read_text())
        if tested_tree != build["source_tree_sha256"]:
            raise RuntimeError("source tree differs from built workloads; create a fresh fixture")
        f.verify_images(build)
        f.verify_parsers()
        # Compile first; output from tests is intentionally restricted to codes
        # and assertions. Dependency details stay in the private diagnostic file.
        probe=f.state / "probe"
        f.run("go","test","-race","-tags=integration","-c","-o",str(probe),"./backend/services/files/internal/app",timeout=180)
        expected=["fault-dc-a","blocked-dc-a","restore-dc-a","fault-dc-b","blocked-dc-b","restore-dc-b"]
        process=subprocess.Popen([str(probe),"-test.v","-test.run=^TestLiveDCFiles$","-test.timeout=30m"],
            cwd=ROOT,env=dict(os.environ,FILES_DC_FIXTURE=str(f.state)),stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
        try:
            with (f.state / "probe.log").open("w") as log:
                for line in process.stdout:
                    log.write(line);log.flush()
                    print(line,end="",flush=True)
                    if line.startswith("MM43_DC_EVENT "):
                        phase=line.strip().removeprefix("MM43_DC_EVENT ")
                        if not expected or phase!=expected.pop(0):
                            raise RuntimeError("unexpected fault handshake")
                        dc=phase[-4:]
                        if phase.startswith("fault-"):
                            self.fault(dc)
                        elif phase.startswith("blocked-"):
                            f.wait(lambda: int(f.sql(f.primary, "SELECT count(*) FROM pg_stat_activity WHERE application_name='files-control' AND wait_event='SyncRep'")) > 0, "write waiting for remote_apply", 4)
                        else:
                            self.restore(dc)
                        self.phases.append(phase)
                        process.stdin.write(json.dumps({"phase":phase,"ok":True})+"\n");process.stdin.flush()
                code=process.wait(timeout=30)
                if code or expected:
                    raise RuntimeError("DC E2E assertions failed")
        finally:
            if process.poll() is None:
                process.terminate()
                try: process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill();process.wait()
        if f.tree_hash() != tested_tree:
            raise RuntimeError("source tree changed while the acceptance test was running")
        report={"task":"MM-43","instance":f.instance,"result":"pass","phases":self.phases,
            "head":f.run("git","rev-parse","HEAD").stdout.decode().strip(),
            "tested_tree_sha256":tested_tree, "build":build,
            "probe_sha256":__import__("hashlib").sha256(probe.read_bytes()).hexdigest(),
            "physical_hosts":1,"logical_dcs":2,"vm_count":4,"fault":"stop both owned VMs; verified persistent PostgreSQL fencing",
            "time_utc":time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())}
        write(f.state / "report.json",report)


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command",choices=("up","test","down","run"))
    parser.add_argument("--instance",default="mm43-"+secrets.token_hex(4))
    parser.add_argument("--keep",action="store_true",help="сохранить собственный одноразовый стенд для диагностики")
    args=parser.parse_args()
    if not re.fullmatch(r"mm43-[a-z0-9-]{1,15}",args.instance):
        parser.error("instance must be mm43- plus 1..15 lowercase letters, digits or hyphens")
    os.umask(0o077)
    f=Fixture(args.instance)
    try:
        if args.command in ("up","run"):
            if (f.state / "fixture.json").exists():
                raise RuntimeError("fixture already initialized; use test/down with its exact instance")
            print("MM-43: creating four owned OrbStack VM",flush=True)
            f.topology("bootstrap");f.topology("up");f.topology("ready")
            f.bind();f.prepare()
            print("MM-43: building and importing workload images",flush=True)
            f.images();f.install()
            print("MM-43: starting database, KMS and applications",flush=True)
            f.start_database();f.start_kms();f.start_apps();f.start_worker()
            f.ready("dc-a");f.ready("dc-b");f.save()
            print("MM-43: both DC ready; parser isolation verified",flush=True)
        else:
            f.load()
        if args.command in ("test","run"):
            DCTest(f).test()
        if args.command=="down" or (args.command=="run" and not args.keep):
            f.topology("down")
    except Exception:
        with f.log.open("a") as log:
            traceback.print_exc(file=log)
        f.save()
        # No automatic restart/promotion after a failed proof. A partially failed
        # transition retains the fence and exact owned resources for diagnosis.
        print("MM-43 DC E2E не завершён. Приватная диагностика: "+str(f.state),file=sys.stderr)
        raise SystemExit(1) from None


if __name__=="__main__":
    main()
