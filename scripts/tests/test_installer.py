"""Isolated shell integration tests: no real root paths, network or services.

Only the test copy rewrites fixed OS paths into a TemporaryDirectory. Production
installer has no test-root override. All docker/systemd/curl calls are fakes.
"""
import bz2
import hashlib
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]

FAKE = r'''#!/usr/bin/env python3
import hashlib, json, os, pathlib, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['VM_COMMAND_LOG'], 'a') as log:
    log.write(json.dumps([name] + args) + '\n')
joined = ' '.join(args)
if name == 'docker':
    failure = os.environ.get('VM_FAIL', '')
    if failure and failure in joined:
        sys.exit(1)
    if 'pg_dump' in args:
        sys.stdout.write('PGDMP synthetic database fixture\n')
    if 'pg_restore' in args:
        if not sys.stdin.read().startswith('PGDMP'):
            sys.exit(1)
        sys.stdout.write('validated dump\n')
elif name == 'sha256sum':
    for file in args:
        print(hashlib.sha256(pathlib.Path(file).read_bytes()).hexdigest(), ' ', file, sep='')
elif name == 'curl':
    if os.environ.get('VM_FAIL') == 'health':
        sys.exit(1)
elif name == 'systemctl':
    if os.environ.get('VM_FAIL') == 'agent-start' and 'start' in args:
        sys.exit(1)
    if 'start' in args:
        state = pathlib.Path(os.environ['VM_AGENT_STATE'])
        if not state.exists():
            state.write_text('{"identity":{"agent_id":"fixture"}}')
elif name == 'uname':
    print('x86_64' if '-m' in args else 'Linux')
elif name == 'restic':
    print('restic 0.18.0')
elif name == 'mv':
    # macOS lacks GNU mv -T. The integration fixture only replaces symlinks/files.
    args = [a for a in args if a not in ('-Tf', '-f')]
    source, dest = map(pathlib.Path, args)
    if source.is_dir() and not source.is_symlink():
        import shutil
        shutil.move(str(source), str(dest))
    else:
        source.replace(dest)
'''


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='vaultmesh-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'fake-bin'
        self.bin.mkdir()
        self.osroot = self.root / 'os'
        for part in ['usr/local/bin', 'etc/systemd/system', 'var/lib/vaultmesh-agent']:
            (self.osroot / part).mkdir(parents=True, exist_ok=True)
        self.agent_state = self.osroot / 'var/lib/vaultmesh-agent/state.json'
        self.agent_env = self.osroot / 'etc/vaultmesh-agent.env'
        self.agent_binary = self.osroot / 'usr/local/bin/vaultmesh-agent'
        self.service = self.osroot / 'etc/systemd/system/vaultmesh-agent.service'
        self.script = self.root / 'install.sh'
        source = (REPO / 'install.sh').read_text()
        for path in ['/usr/local/bin/', '/etc/systemd/system/', '/etc/vaultmesh-agent.env', '/var/lib/vaultmesh-agent']:
            source = source.replace(path, str(self.osroot) + path)
        self.script.write_text(source)
        self.install = self.root / 'installation'
        self.install.mkdir()
        self.tmp = self.root / 'download'
        self.tmp.mkdir()
        self.fixtures = self.root / 'assets'
        self.fixtures.mkdir()
        self.log = self.root / 'commands.jsonl'
        for name in ['docker', 'systemctl', 'curl', 'sha256sum', 'uname', 'restic', 'mv']:
            path = self.bin / name
            path.write_text(FAKE)
            path.chmod(0o755)
        self.env = dict(os.environ, PATH=str(self.bin) + os.pathsep + os.environ['PATH'],
                        VM_COMMAND_LOG=str(self.log), VM_AGENT_STATE=str(self.agent_state))
        self.bundle('v0.2.0')

    def run_shell(self, code, failure='', ok=True):
        prefix = '\n'.join([
            'set -eu', 'export VAULTMESH_INSTALLER_SOURCE_ONLY=1',
            f'. {shlex.quote(str(self.script))}',
            f'INSTALL_DIR={shlex.quote(str(self.install))}',
            f'TMP={shlex.quote(str(self.tmp))}',
            'VERSION=v0.2.0', 'MODE=external',
            "PUBLIC_URL=https://backup.example.test",
            f'download() {{ cp {shlex.quote(str(self.fixtures))}/"$(basename "$1")" "$2"; }}',
            'trap finish EXIT',
        ])
        result = subprocess.run(['sh', '-c', prefix + '\n' + code], env=dict(self.env, VM_FAIL=failure),
                                capture_output=True, text=True, timeout=20)
        if ok:
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def commands(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def bundle(self, version, bad_member=None, link=False):
        archive = self.fixtures / f'vaultmesh-deploy-{version}.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            for name in ['install.sh', 'VERSION', 'compose.yaml', 'compose.managed.yaml',
                         'compose.external.yaml', 'Caddyfile', 'deploy/systemd/vaultmesh-agent.service']:
                data = (version if name == 'VERSION' else '# fixture') + '\n'
                info = tarfile.TarInfo(name)
                info.size = len(data.encode())
                info.mode = 0o755 if name == 'install.sh' else 0o644
                tar.addfile(info, io.BytesIO(data.encode()))
            if bad_member:
                info = tarfile.TarInfo(bad_member)
                if link:
                    info.type = tarfile.SYMTYPE
                    info.linkname = '/etc/passwd'
                tar.addfile(info, io.BytesIO(b''))
        self.checksum(archive)
        binary = self.fixtures / 'vaultmesh-agent-linux-amd64'
        binary.write_text(f'#!/bin/sh\nprintf "vaultmesh-agent {version} (commit fixture)\\n"\n')
        self.checksum(binary)

    @staticmethod
    def checksum(file):
        file.with_name(file.name + '.sha256').write_text(hashlib.sha256(file.read_bytes()).hexdigest() + '  ' + file.name + '\n')

    def existing_control(self):
        old = self.install / 'releases/v0.1.2'
        old.mkdir(parents=True)
        (old / 'VERSION').write_text('v0.1.2\n')
        for name in ['compose.yaml', 'compose.external.yaml', 'install.sh']:
            (old / name).write_text('# original\n')
        (self.install / 'current').symlink_to('releases/v0.1.2')
        (self.install / '.vaultmesh-installation').write_text('1\n')
        (self.install / '.env').write_text('VAULTMESH_PROXY_MODE=external\nVAULTMESH_PUBLIC_API_URL=https://backup.example.test\nVAULTMESH_MASTER_KEY=fixture-key\n')

    def existing_agent(self):
        self.agent_state.write_text('{"identity":{"agent_id":"original"},"outbox":["pending"]}')
        self.agent_env.write_text('VAULTMESH_SERVER_URL=https://original.test\nVAULTMESH_RESTIC_PATH=/custom/restic\n')
        self.agent_binary.write_text('#!/bin/sh\necho "vaultmesh-agent v0.1.2 (commit old)"\n')
        self.agent_binary.chmod(0o755)
        self.service.write_text('# custom unit retained\n')

    def test_help_needs_no_root_or_linux(self):
        result = subprocess.run(['sh', str(REPO / 'install.sh'), '--help'], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0)
        self.assertIn('upgrade-agent', result.stdout)

    def test_unknown_mode_fails_without_installing(self):
        result = subprocess.run(['sh', str(REPO / 'install.sh'), 'typo'], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('未知命令', result.stderr)

    def test_input_validation_and_version_order(self):
        self.run_shell('''
valid_version v0.2.0
valid_version v0.2.0-rc.1
! valid_version v01.2.0
! valid_version 'v1.2.3\nBAD=1'
! valid_domain 'example.com\nBAD=1'
! valid_url 'https://example.com\nBAD=1'
! valid_url 'https://user:secret@example.com'
! valid_url 'http://example.com'
valid_url 'http://[::1]:8080'
newer_version v0.1.2 v0.2.0
newer_version v0.2.0-rc.9 v0.2.0-rc.10
newer_version v0.2.0-rc.1 v0.2.0
! newer_version v0.2.0 v0.2.0-rc.1
! newer_version v0.2.0 v0.1.9
! newer_version v0.2.0 v0.2.0
''')

    def test_checksum_mismatch_before_compose(self):
        archive = self.fixtures / 'vaultmesh-deploy-v0.2.0.tar.gz'
        archive.write_bytes(b'changed after checksum')
        result = self.run_shell('fetch_bundle', ok=False)
        self.assertIn('SHA256 校验失败', result.stderr)
        self.assertFalse(any(cmd[0] == 'docker' for cmd in self.commands()))

    def test_archive_traversal_rejected(self):
        self.bundle('v0.2.0', '../escaped')
        self.run_shell('fetch_bundle', ok=False)
        self.assertFalse((self.root / 'escaped').exists())

    def test_archive_symlink_rejected(self):
        self.bundle('v0.2.0', 'bad-link', link=True)
        self.run_shell('fetch_bundle', ok=False)

    def test_archive_version_mismatch(self):
        self.bundle('v0.3.0')
        source = self.fixtures / 'vaultmesh-deploy-v0.3.0.tar.gz'
        target = self.fixtures / 'vaultmesh-deploy-v0.2.0.tar.gz'
        shutil.copyfile(source, target)
        self.checksum(target)
        self.run_shell('fetch_bundle', ok=False)

    def test_fresh_install_uses_release_without_build(self):
        self.run_shell('ACTION=install; control_action')
        self.assertEqual((self.install / 'current/VERSION').read_text().strip(), 'v0.2.0')
        self.assertEqual((self.install / '.env').stat().st_mode & 0o777, 0o600)
        self.assertTrue(any('up' in cmd and '--no-build' in cmd for cmd in self.commands()))
        self.assertFalse(any('build' in cmd for cmd in self.commands()))

    def test_existing_git_install_is_never_overwritten(self):
        (self.install / '.git').mkdir()
        original = self.install / '.env'
        original.write_text('original secret\n')
        self.run_shell('ACTION=install; control_action', ok=False)
        self.assertEqual(original.read_text(), 'original secret\n')

    def test_upgrade_keeps_secrets_and_backs_up_before_start(self):
        self.existing_control()
        before = (self.install / '.env').read_bytes()
        self.run_shell('ACTION=upgrade; control_action')
        self.assertEqual((self.install / '.env').read_bytes(), before)
        self.assertEqual((self.install / 'current/VERSION').read_text().strip(), 'v0.2.0')
        backup = next((self.install / 'backups').iterdir())
        self.assertEqual((backup / '.env').read_bytes(), before)
        self.assertEqual((backup / 'release/VERSION').read_text().strip(), 'v0.1.2')
        self.assertTrue((backup / 'postgres.list').exists())
        commands = self.commands()
        pull = next(i for i, c in enumerate(commands) if 'pull' in c)
        stop = next(i for i, c in enumerate(commands) if 'stop' in c)
        dump = next(i for i, c in enumerate(commands) if 'pg_dump' in c)
        up = next(i for i, c in enumerate(commands) if 'up' in c)
        self.assertLess(pull, stop)
        self.assertLess(stop, dump)
        self.assertLess(dump, up)
        self.assertIn('--no-deps', commands[up])
        self.assertNotIn('postgres', commands[up])

    def test_failed_pull_never_stops_old_control(self):
        self.existing_control()
        self.run_shell('ACTION=upgrade; control_action', failure='pull control', ok=False)
        self.assertFalse(any('stop' in cmd for cmd in self.commands()))
        self.assertEqual((self.install / 'current/VERSION').read_text().strip(), 'v0.1.2')

    def test_failed_backup_restarts_old_control_without_migration(self):
        self.existing_control()
        self.run_shell('ACTION=upgrade; control_action', failure='pg_dump', ok=False)
        self.assertTrue(any('start' in cmd and 'control' in cmd for cmd in self.commands()))
        self.assertFalse(any('up' in cmd for cmd in self.commands()))

    def test_failed_health_stops_new_control_without_database_rollback(self):
        self.existing_control()
        result = self.run_shell('ACTION=upgrade; control_action', failure='health', ok=False)
        self.assertIn('不自动降级数据库', result.stderr)
        self.assertEqual((self.install / 'current/VERSION').read_text().strip(), 'v0.1.2')
        self.assertFalse(any('dropdb' in cmd or 'start' in cmd for cmd in self.commands()))

    def test_old_version_downgrade_rejected(self):
        self.existing_control()
        self.run_shell('ACTION=upgrade; VERSION=v0.1.1; control_action', ok=False)
        self.assertFalse(any('pull' in cmd or 'stop' in cmd for cmd in self.commands()))

    def test_agent_install_rejects_existing_identity(self):
        self.existing_agent()
        before = self.agent_state.read_bytes()
        self.run_shell('ACTION=install-agent; AGENT_URL=https://backup.example.test; AGENT_TOKEN=enroll_fixture; agent_action', ok=False)
        self.assertEqual(self.agent_state.read_bytes(), before)
        self.assertFalse(any(cmd[0] == 'systemctl' for cmd in self.commands()))

    def test_agent_upgrade_preserves_identity_configuration_and_custom_unit(self):
        self.existing_agent()
        before = [p.read_bytes() for p in [self.agent_state, self.agent_env, self.service]]
        self.run_shell('ACTION=upgrade-agent; agent_action')
        self.assertEqual([p.read_bytes() for p in [self.agent_state, self.agent_env, self.service]], before)
        backups = list(self.agent_state.parent.glob('upgrade-backup.*'))
        self.assertEqual(len(backups), 1)
        self.assertEqual((backups[0] / 'state.json').read_bytes(), before[0])
        self.assertIn('v0.2.0', self.agent_binary.read_text())

    def test_agent_failed_start_keeps_state_and_old_binary_backup(self):
        self.existing_agent()
        before = self.agent_state.read_bytes()
        self.run_shell('ACTION=upgrade-agent; agent_action', failure='agent-start', ok=False)
        self.assertEqual(self.agent_state.read_bytes(), before)
        backup = next(self.agent_state.parent.glob('upgrade-backup.*'))
        self.assertIn('v0.1.2', (backup / 'vaultmesh-agent').read_text())

    def test_new_agent_registration_strips_token(self):
        self.run_shell('ACTION=install-agent; AGENT_URL=https://backup.example.test; AGENT_TOKEN=enroll_fixture; agent_action')
        self.assertIn('agent_id', self.agent_state.read_text())
        self.assertNotIn('enroll_fixture', self.agent_env.read_text())
        self.assertEqual(self.agent_env.stat().st_mode & 0o777, 0o600)

    def restic_asset(self):
        name = 'restic_0.18.0_linux_amd64.bz2'
        contents = bz2.compress(b'#!/bin/sh\necho "restic 0.18.0"\n')
        (self.fixtures / name).write_bytes(contents)
        (self.fixtures / 'SHA256SUMS').write_text(hashlib.sha256(contents).hexdigest() + '  ' + name + '\n')
        return self.fixtures / name

    def test_missing_restic_installs_verified_binary_before_agent_start(self):
        self.restic_asset()
        self.run_shell('has_restic() { return 1; }; ACTION=install-agent; AGENT_URL=https://backup.example.test; AGENT_TOKEN=enroll_fixture; agent_action')
        restic = self.osroot / 'usr/local/bin/restic'
        self.assertTrue(restic.stat().st_mode & 0o111)
        self.assertIn('restic 0.18.0', restic.read_text())

    def test_bad_restic_checksum_does_not_stop_existing_agent(self):
        self.existing_agent()
        self.restic_asset().write_bytes(b'tampered')
        before = self.agent_state.read_bytes()
        self.run_shell('has_restic() { return 1; }; ACTION=upgrade-agent; agent_action', ok=False)
        self.assertFalse(any('stop' in c for c in self.commands()))
        self.assertEqual(self.agent_state.read_bytes(), before)

    def test_existing_restic_not_replaced(self):
        restic = self.osroot / 'usr/local/bin/restic'
        restic.write_text('administrator managed tool')
        self.existing_agent()
        self.run_shell('ACTION=upgrade-agent; agent_action')
        self.assertEqual(restic.read_text(), 'administrator managed tool')

    def test_armv6_is_not_misclassified_as_armv7(self):
        self.run_shell('uname() { printf "armv6l\\n"; }; ACTION=install-agent; agent_action', ok=False)
        self.assertFalse(any(c[0] == 'systemctl' for c in self.commands()))

    def test_compose_does_not_consume_callers_stdin(self):
        self.existing_control()
        self.run_shell("ACTION=status; printf 'still-readable\\n' | { current_compose ps; read -r line; test \"$line\" = still-readable; }")

    def test_production_package_contains_templates_docs_and_no_secrets(self):
        target = self.root / 'package'
        result = subprocess.run(['sh', str(REPO / 'scripts/package-release.sh'), 'v0.2.0', str(target)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        with tarfile.open(target / 'vaultmesh-deploy-v0.2.0.tar.gz') as tar:
            names = {name.removeprefix('./') for name in tar.getnames()}
            self.assertTrue({'install.sh', 'compose.yaml', 'Caddyfile', 'VERSION', 'COMMIT', 'LICENSE', 'docs/INSTALL.md', 'docs/UPGRADE.md'} <= names)
            self.assertFalse({'.env', '.git', 'node_modules', 'state.json'} & names)
            self.assertEqual(tar.getmember('./install.sh').mode & 0o111, 0o111)
        checksum = (target / 'vaultmesh-deploy-v0.2.0.tar.gz.sha256').read_text().split()[0]
        self.assertEqual(checksum, hashlib.sha256((target / 'vaultmesh-deploy-v0.2.0.tar.gz').read_bytes()).hexdigest())


if __name__ == '__main__':
    unittest.main()
