"""Regressions for dependency selection and data-preserving local bootstrap."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('dev_local', Path(__file__).with_name('local.py'))
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


class SelectionTests(unittest.TestCase):
    model = {
        'auth': {'depends_on': {'provision': {}, 'redis': {}}},
        'redis': {},
        'provision': {'depends_on': {'postgres': {}}},
        'postgres': {},
        'grafana': {'profiles': ['observability', 'full'], 'depends_on': {'loki': {}}},
        'loki': {'profiles': ['observability', 'full']},
        'analytics': {'profiles': ['analytics', 'full']},
        'signatures': {'profiles': ['tools']},
    }

    def test_service_selects_only_transitive_dependencies(self):
        self.assertEqual(dev.selection(self.model, ['auth'], ['full']), {'auth', 'redis', 'provision', 'postgres'})
        self.assertEqual(dev.selection(self.model, ['grafana'], []), {'grafana', 'loki'})

    def test_profiles_never_start_tools(self):
        core = {'auth', 'redis', 'provision', 'postgres'}
        self.assertEqual(dev.selection(self.model, [], ['core']), core)
        self.assertEqual(dev.selection(self.model, [], ['analytics']), core | {'analytics'})
        self.assertEqual(dev.selection(self.model, [], ['full']), core | {'analytics', 'grafana', 'loki'})

    def test_unknown_service_fails(self):
        with self.assertRaisesRegex(RuntimeError, 'Неизвестные'):
            dev.selection(self.model, ['missing'], [])

    def test_frontdoor_transitively_selects_storage_bootstrap(self):
        model = {'frontdoor': {'depends_on': {'files-control': {}}}, 'files-control': {},
                 **{name: {'depends_on': {'volume-init': {}}} for name in
                    ('bao', 'quarantine', 'internal-clean', 'delivery-a', 'delivery-b')},
                 'volume-init': {}}
        self.assertEqual(dev.selection(model, ['frontdoor'], []), set(model))


class StateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.state = self.root / 'state'
        self.state.mkdir()
        self.account = self.state / 'account'
        self.account.mkdir()
        self.patcher = patch.multiple(dev, ROOT=self.root, STATE=self.state, ACCOUNT=self.account,
                                      FILES=self.state / 'files', RYBBIT=self.state / 'rybbit')
        self.patcher.start()
        self.addCleanup(self.patcher.stop)

    def test_existing_resources_without_configuration_fail_before_mutation(self):
        with patch.object(dev, 'run', return_value='existing-volume\n') as run:
            with self.assertRaisesRegex(RuntimeError, 'без конфигурации'):
                dev.check_owned_state()
        self.assertEqual(run.call_count, 1)

    def test_partial_state_with_volumes_cannot_regenerate_credentials(self):
        (self.state / 'owner.json').write_text(json.dumps({'workspace': str(self.root), 'project': dev.PROJECT}))
        with patch.object(dev, 'run', return_value='existing-volume\n'):
            with self.assertRaisesRegex(RuntimeError, 'неполная'):
                dev.check_owned_state()

    def test_symlink_rejected(self):
        (self.state / 'alias').symlink_to(self.root / 'outside')
        with patch.object(dev, 'run') as run:
            with self.assertRaisesRegex(RuntimeError, 'Символическая'):
                dev.check_owned_state()
        run.assert_not_called()

    def test_compose_uses_saved_secrets_over_caller_environment(self):
        (self.state / 'compose.env').write_text("POSTGRES_PASSWORD='saved-local-password'\n")
        with patch.dict(dev.ENV, POSTGRES_PASSWORD='unrelated-shell-password'), patch.object(dev, 'run') as run:
            dev.compose('config', '--quiet')
        self.assertEqual(run.call_args.kwargs['env']['POSTGRES_PASSWORD'], 'saved-local-password')

    def test_certificate_renewal_preserves_persistent_credentials(self):
        names = [f'{service}/{name}' for service in ('auth', 'user', 'gateway-in', 'gateway-out', 'frontdoor', 'nats', 'provision')
                 for name in ('cert.pem', 'key.pem', 'ca.pem')]
        names += ['auth/keys.json', 'browser/ca.pem', 'ready.json']
        preserved = ['auth/env', 'auth/mail.key', 'redis/redis.conf', 'postgres-primary/env', '.owner']
        for name in names + preserved:
            target = self.account / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text('old-' + name)
        (self.account / 'ready.json').write_text(json.dumps({'Port': '18443', 'Topology': 3}))
        def generate(*args, **kwargs):
            destination = Path(kwargs['env']['FIXTURE_ROOT'])
            for name in names + preserved:
                target = destination / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text('new-' + name)
        with patch.object(dev, 'run', side_effect=generate):
            dev.renew_account_certificates()
        for name in names:
            self.assertEqual((self.account / name).read_text(), 'new-' + name)
        for name in preserved:
            self.assertEqual((self.account / name).read_text(), 'old-' + name)
        self.assertFalse((self.state / 'account-renewing').exists())
        self.assertEqual((self.account / 'auth/key.pem').stat().st_mode & 0o777, 0o600)

    def test_interrupted_renewal_leaves_retry_marker(self):
        (self.account / 'ready.json').write_text(json.dumps({'Port': '18443', 'Topology': 3}))
        with patch.object(dev, 'run', side_effect=OSError):
            with self.assertRaises(OSError):
                dev.renew_account_certificates()
        self.assertTrue((self.state / 'account-renewing').exists())


if __name__ == '__main__':
    unittest.main()
