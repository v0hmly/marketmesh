"""Legacy diagnostic commands must not bring back a second persistent stack."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class LegacyTests(unittest.TestCase):
    def test_commands_that_used_to_start_dependencies_fail_before_docker(self):
        source = Path(__file__).resolve().parents[1] / 'compose/scripts/local.sh'
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            script = root / 'compose/scripts/local.sh'
            script.parent.mkdir(parents=True)
            shutil.copyfile(source, script)
            binary = root / 'bin'
            binary.mkdir()
            docker = binary / 'docker'
            docker.write_text('#!/bin/sh\ntouch "$DOCKER_CALLED"\nexit 89\n')
            docker.chmod(0o755)
            marker = root / 'docker-called'
            env = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ['PATH'], DOCKER_CALLED=str(marker))
            for command in ('up', 'verify', 'ready', 'smoke', 'persistence', 'psql-rw', 'psql-ro',
                            'observability-up', 'observability-verify', 'observability-ready',
                            'observability-smoke', 'observability-persistence', 'observability-outage'):
                with self.subTest(command=command):
                    result = subprocess.run(['bash', str(script), command], env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn('task dev:up', result.stderr)
                    self.assertFalse(marker.exists())
                    self.assertFalse((root / 'compose/.env').exists())


if __name__ == '__main__':
    unittest.main()
