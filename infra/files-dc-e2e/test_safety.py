"""Быстрые отрицательные проверки до обращения к реальному OrbStack."""
import json
from pathlib import Path
import subprocess
from types import SimpleNamespace
import tempfile
import time
import unittest
from unittest.mock import Mock

from fixture import Fixture, OwnershipError, legacy_auth_env, legacy_auth_schema, LEGACY_AUTH_FILES
from run import DCTest


class SafetyTests(unittest.TestCase):
    def test_files_auth_fixture_stays_separate_from_email_runtime(self):
        with tempfile.TemporaryDirectory() as root:
            env = Path(root) / "auth.env"
            env.write_text("AUTH_EMAIL_ENABLED='true'\nAUTH_EMAIL_KEY_FILE='/secrets/mail.key'\nAUTH_SMTP_ADDRESS='mailpit:1025'\nAUTH_SESSIONS_ENABLED='true'\nAUTH_SESSION_KEYS_FILE='/secrets/keys.json'\n")
            actual = legacy_auth_env(env)
            self.assertEqual(actual["AUTH_EMAIL_ENABLED"], "false")
            self.assertEqual(actual["AUTH_REGISTRATION_EVENTS_ENABLED"], "false")
            self.assertEqual(actual["AUTH_SESSIONS_ENABLED"], "true")
            self.assertNotIn("AUTH_EMAIL_KEY_FILE", actual)
            self.assertNotIn("AUTH_SMTP_ADDRESS", actual)
            self.assertNotIn("mail.key", LEGACY_AUTH_FILES)
        schema = legacy_auth_schema(Path(__file__).resolve().parents[2] / "backend/services/auth/migrations")
        self.assertIn("CREATE TABLE auth.sessions", schema)
        self.assertIn("CREATE TABLE auth.registration_outbox", schema)
        self.assertNotIn("CREATE TABLE auth.account_security", schema)

    def test_stale_binding_never_executes_guest_command(self):
        fixture=Fixture.__new__(Fixture)
        fixture.validate=Mock(side_effect=RuntimeError("stale binding"))
        fixture.run=Mock()
        fixture.target=Mock()
        with self.assertRaisesRegex(RuntimeError,"stale binding"):
            fixture.vm("dc-b-internal","arbitrary-privileged-command")
        fixture.run.assert_not_called()
        fixture.target.assert_not_called()

    def test_guest_guard_uses_id_and_rejects_changed_boot(self):
        fixture=Fixture.__new__(Fixture)
        fixture.validate=Mock()
        fixture.target=Mock(return_value={"machine":{"name":"reusable-name","id":"immutable-id","boot_id":"expected"}})
        fixture.run=Mock()
        fixture.vm("dc-b-internal","true")
        args=fixture.run.call_args.args
        self.assertIn("immutable-id",args)
        self.assertNotIn("reusable-name",args)
        with tempfile.TemporaryDirectory() as root:
            boot=Path(root)/"boot";boot.write_text("changed\n")
            marker=Path(root)/"executed"
            script=args[args.index("-ec")+1].replace("/proc/sys/kernel/random/boot_id",str(boot))
            result=subprocess.run(["sh","-ec",script,"guard","expected","touch",str(marker)],capture_output=True)
            self.assertNotEqual(result.returncode,0)
            self.assertFalse(marker.exists())

    def test_role_read_failures_do_not_open_fence(self):
        source=Path(__file__).with_name("postgres.sh").read_text()
        gate=source.split("rm -f /data/fenced-boot-id",1)[0]
        with tempfile.TemporaryDirectory() as root:
            role=Path(root)/"role"
            script=gate.replace("/config/role",str(role))+"\nprintf started"
            for contents in (None,"","unknown","fenced\nprimary"):
                with self.subTest(contents=contents):
                    if contents is not None:role.write_text(contents)
                    else:role.unlink(missing_ok=True)
                    result=subprocess.run(["bash","-c",script],capture_output=True,timeout=3)
                    self.assertNotEqual(result.returncode,0)
                    self.assertNotIn(b"started",result.stdout)
            for contents in ("primary","replica"):
                role.write_text(contents)
                result=subprocess.run(["bash","-c",script],capture_output=True,timeout=3)
                self.assertEqual(result.returncode,0)
                self.assertEqual(result.stdout,b"started")

    def test_fenced_shell_proves_boot_and_stays_closed_on_corruption(self):
        source=Path(__file__).with_name("postgres.sh").read_text()
        gate=source.split("rm -f /data/fenced-boot-id",1)[0]
        with tempfile.TemporaryDirectory() as root:
            role=Path(root)/"role";role.write_text("fenced")
            boot=Path(root)/"boot";boot.write_text("boot-generation\n")
            marker=Path(root)/"fenced"
            script=gate.replace("/config/role",str(role)).replace("/proc/sys/kernel/random/boot_id",str(boot)).replace("/data/fenced-boot-id",str(marker))+"\nprintf started"
            process=subprocess.Popen(["bash","-c",script],stdout=subprocess.PIPE,stderr=subprocess.PIPE)
            try:
                deadline=time.monotonic()+3
                while not marker.exists() and time.monotonic()<deadline:time.sleep(.02)
                self.assertEqual(marker.read_text(),"boot-generation\n")
                self.assertIsNone(process.poll())
                role.write_text("corrupt")
                stdout,_=process.communicate(timeout=3)
                self.assertNotEqual(process.returncode,0)
                self.assertNotIn(b"started",stdout)
            finally:
                if process.poll() is None:process.kill();process.wait()

    def test_second_dc_fence_failure_prevents_promotion(self):
        fixture=Mock()
        fixture.primary="dc-a"
        fixture.redis.return_value="master_repl_offset:7\nslave_repl_offset:7"
        fixture.target.return_value={"machine":{"id":"owned-id"}}
        fixture.validate.side_effect=lambda node,state="running": (_ for _ in ()).throw(RuntimeError("fence failed")) if node=="dc-a-dmz" and state=="stopped" else {}
        with tempfile.TemporaryDirectory() as root:
            fixture.state=Path(root)
            test=DCTest(fixture)
            test.role=Mock()
            with self.assertRaisesRegex(RuntimeError,"fence failed"):test.fault("dc-a")
            fixture.sql.assert_not_called()
            fixture.restart.assert_not_called()

    def test_build_source_mismatch_cannot_generate_pass_report(self):
        fixture=Mock()
        fixture.tree_hash.return_value="changed"
        with tempfile.TemporaryDirectory() as root:
            fixture.state=Path(root)
            (fixture.state/"build.json").write_text(json.dumps({"source_tree_sha256":"built"}))
            with self.assertRaisesRegex(RuntimeError,"differs from built workloads"):DCTest(fixture).test()
            fixture.run.assert_not_called()
            self.assertFalse((fixture.state/"report.json").exists())

    def test_both_fenced_vms_start_before_any_rebind(self):
        fixture=Mock()
        fixture.primary="dc-b"
        fixture.target.side_effect=lambda node:{"machine":{"id":node+"-owned"}}
        test=DCTest(fixture)
        test.stopped={"dc-a-internal":{},"dc-a-dmz":{}}
        def rejoin(dc):
            self.assertEqual(dc,"dc-a")
            self.assertEqual([call.args for call in fixture.run.call_args_list],
                [("orbctl","start","dc-a-internal-owned"),("orbctl","start","dc-a-dmz-owned")])
        test.rejoin=Mock(side_effect=rejoin)
        test.restore("dc-a")
        test.rejoin.assert_called_once_with("dc-a")

    def test_failed_second_vm_start_prevents_rejoin(self):
        fixture=Mock()
        fixture.primary="dc-b"
        fixture.target.side_effect=lambda node:{"machine":{"id":node+"-owned"}}
        fixture.run.side_effect=[None,RuntimeError("start failed")]
        test=DCTest(fixture)
        test.stopped={"dc-a-internal":{},"dc-a-dmz":{}}
        test.rejoin=Mock()
        with self.assertRaisesRegex(RuntimeError,"start failed"):test.restore("dc-a")
        test.rejoin.assert_not_called()

    def deletion_fixture(self):
        fixture=Fixture.__new__(Fixture)
        fixture.instance="mm43-owned"
        pod={"metadata":{"uid":"owned-uid","labels":{"marketmesh.task":"MM-43","marketmesh.run":fixture.instance}}}
        return fixture,pod

    def test_pod_deletion_requires_fresh_successful_absence_proof(self):
        fixture,pod=self.deletion_fixture()
        fixture.kubectl=Mock(side_effect=[SimpleNamespace(stdout=json.dumps(pod).encode()),SimpleNamespace(stdout=b"deleted"),SimpleNamespace(stdout=b"")])
        fixture.wait=lambda check,label,timeout:self.assertTrue(check())
        fixture.delete_pod("dc-a-internal","auth")
        deletion=fixture.kubectl.call_args_list[1]
        self.assertEqual(deletion.args,("dc-a-internal","delete","--raw","/api/v1/namespaces/mm43-files/pods/auth","-f","-","--request-timeout=10s"))
        self.assertEqual(json.loads(deletion.kwargs["input"])["preconditions"],{"uid":"owned-uid"})
        self.assertEqual(fixture.kubectl.call_count,3)

    def test_api_error_is_not_proof_of_pod_deletion(self):
        fixture,pod=self.deletion_fixture()
        fixture.kubectl=Mock(side_effect=[SimpleNamespace(stdout=json.dumps(pod).encode()),SimpleNamespace(stdout=b"deleted"),RuntimeError("API unavailable")])
        fixture.wait=lambda check,label,timeout:check()
        with self.assertRaisesRegex(RuntimeError,"API unavailable"):fixture.delete_pod("dc-a-internal","auth")

    def test_unowned_pod_cannot_be_deleted(self):
        fixture,pod=self.deletion_fixture()
        pod["metadata"]["labels"]["marketmesh.run"]="unrelated"
        fixture.kubectl=Mock(return_value=SimpleNamespace(stdout=json.dumps(pod).encode()))
        with self.assertRaisesRegex(RuntimeError,"unowned"):fixture.delete_pod("dc-a-internal","auth")
        self.assertEqual(fixture.kubectl.call_count,1)

    def test_replaced_pod_then_not_found_cannot_pass_real_wait(self):
        fixture,pod=self.deletion_fixture()
        replacement={"metadata":{"uid":"new-uid"}}
        fixture.kubectl=Mock(side_effect=[SimpleNamespace(stdout=json.dumps(pod).encode()),SimpleNamespace(stdout=b"deleted"),SimpleNamespace(stdout=json.dumps(replacement).encode()),SimpleNamespace(stdout=b"")])
        with self.assertRaisesRegex(OwnershipError,"identity changed"):fixture.delete_pod("dc-a-internal","auth")
        self.assertEqual(fixture.kubectl.call_count,3)


if __name__=="__main__":unittest.main()
