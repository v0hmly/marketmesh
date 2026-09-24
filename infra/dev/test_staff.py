"""Staff PKI renewal must resume without replacing persistent credentials."""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('staff_local', Path(__file__).with_name('staff.py'))
staff = importlib.util.module_from_spec(spec)
spec.loader.exec_module(staff)


class StaffStateTests(unittest.TestCase):
    def test_interrupted_certificate_renewal_is_retried(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credentials = root / 'credentials.json'
            credentials.write_text('persistent credentials')
            actual = subprocess.run
            def interrupt(args, **kwargs):
                if 'oidc.csr' in args:
                    raise OSError('interrupted before IdP certificate')
                return actual(args, **kwargs)
            with patch.object(staff.subprocess, 'run', side_effect=interrupt):
                with self.assertRaises(OSError):
                    staff.certificates(root, True)
            self.assertTrue((root / 'staff.crt').exists())
            self.assertTrue((root / 'renewing').exists())
            staff.certificates(root, False)
            self.assertFalse((root / 'renewing').exists())
            self.assertEqual(credentials.read_text(), 'persistent credentials')
            result = actual(['openssl', 'verify', '-CAfile', str(root / 'ca.crt'), str(root / 'staff.crt'), str(root / 'oidc.crt')], capture_output=True)
            self.assertEqual(result.returncode, 0)
            self.assertEqual((root / 'ca.key').stat().st_mode & 0o777, 0o600)
            for name in ('browser', 'health'):
                result = actual(['openssl', 'verify', '-purpose', 'sslclient', '-CAfile', str(root / 'ca.crt'), str(root / (name + '.crt'))], capture_output=True)
                self.assertEqual(result.returncode, 0)
            self.assertEqual((root / 'browser.p12').stat().st_mode & 0o777, 0o600)

    def test_invite_inputs_are_closed_allowlists(self):
        for email, role in [('attacker@example.test', 'support'), ('employee@marketmesh.test', "admin'); DROP SCHEMA staff; --")]:
            with self.assertRaises(RuntimeError):
                staff.invitation(Path('/unused'), email, role)
        sql, link = staff.invitation(Path('/unused'), 'employee@marketmesh.test', 'support')
        self.assertIn('INSERT INTO staff.invites', sql)
        self.assertNotIn(link.split('=')[-1], sql)
        self.assertTrue(link.startswith('https://staff.localhost:18444/staff/invite#token='))
