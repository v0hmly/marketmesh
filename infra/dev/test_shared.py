"""Credential stability and isolation of the shared dev bootstrap."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('shared', Path(__file__).with_name('shared.py'))
shared = importlib.util.module_from_spec(spec)
spec.loader.exec_module(shared)


class BootstrapTests(unittest.TestCase):
    def test_repeated_prepare_preserves_passwords_and_updates_only_ca(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            account, files, analytics = (state / name for name in ('account', 'files', 'rybbit'))
            pg = {key: 'a' * 64 for key in ('POSTGRES_PASSWORD', 'AUTH_RW_PASSWORD', 'AUTH_RO_PASSWORD', 'USER_RW_PASSWORD', 'USER_RO_PASSWORD', 'REPLICATOR_PASSWORD')}
            shared.write_env(account / 'postgres-primary/env', pg)
            shared.write_env(analytics / '.env', {key: 'b' * 64 for key in ('POSTGRES_PASSWORD', 'REDIS_PASSWORD', 'CLICKHOUSE_PASSWORD', 'CLICKHOUSE_QUERY_PASSWORD')})
            shared.write(files / 'database.json', json.dumps({key: 'c' * 48 for key in ('files_rw', 'files_ro', 'files_worker')}))
            shared.write(files / 'pki/ca.crt', 'old-ca')
            for app in ('auth', 'user', 'provision'):
                shared.write_env(account / app / 'env', {'DB_DSN': 'postgres://app@postgres-primary:5432/auth?sslmode=disable', 'RO_DSN': 'postgres://app@postgres-replica:5432/auth?sslmode=disable'})
            shared.configure(state, account, files, analytics)
            snapshots = {p: p.read_bytes() for p in (state / 'shared').iterdir()}
            shared.write(files / 'pki/ca.crt', 'renewed-ca')
            shared.configure(state, account, files, analytics)
            self.assertEqual({p: p.read_bytes() for p in snapshots}, snapshots)
            for app in ('auth', 'user', 'provision'):
                self.assertEqual((account / app / 'db-ca.crt').read_text(), 'renewed-ca')
                values = shared.env_values(account / app / 'env')
                self.assertEqual(values['DB_DSN'], 'postgres://app@pg-primary:5432/auth?sslmode=verify-full&sslrootcert=/secrets/db-ca.crt')
                self.assertIn('@pg-replica:', values['RO_DSN'])
            for path in snapshots:
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_invalid_credentials_never_enter_sql_or_environment(self):
        with self.assertRaises(RuntimeError):
            shared.password("'; DROP DATABASE auth; --")
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / 'env'
            for value in ("one\nINJECT=two", "$(command)", "quote'"):
                with self.assertRaises(RuntimeError):
                    shared.write_env(destination, {'KEY': value})
                self.assertFalse(destination.exists())

    def test_partial_shared_state_does_not_generate_new_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            shared.write(state / 'shared/postgres.env', 'preserve')
            with self.assertRaisesRegex(RuntimeError, 'Incomplete'):
                shared.configure(state, state / 'account', state / 'files', state / 'rybbit')
            self.assertFalse((state / 'shared/credentials.json').exists())


if __name__ == '__main__':
    unittest.main()
