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
    if args == ['ps', '--format', '{{.Ports}}']:
        print(os.environ.get('VM_DOCKER_PORTS', ''))
    if os.environ.get('VM_FAIL') == 'existing-stack' and args and args[0] == 'ps':
        print('existing-container')
    if os.environ.get('VM_FAIL') == 'existing-volume' and args[:2] == ['volume', 'ls']:
        print('vaultmesh_vaultmesh-postgres')
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
elif name == 'ss':
    if os.environ.get('VM_FAIL') == 'listeners':
        sys.exit(1)
    port = args[-1].split(':')[-1]
    if port in os.environ.get('VM_BUSY_PORTS', '').split(','):
        print(f'LISTEN 0 128 0.0.0.0:{port} 0.0.0.0:*')
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
        for name in ['docker', 'systemctl', 'curl', 'sha256sum', 'uname', 'restic', 'mv', 'ss']:
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
            'VERSION=v0.2.0',
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

    def bundle(self, version, bad_member=None, link=False, proxy_api=True):
        archive = self.fixtures / f'vaultmesh-deploy-{version}.tar.gz'
        with tarfile.open(archive, 'w:gz') as tar:
            names = ['install.sh', 'VERSION', 'compose.yaml', 'compose.managed.yaml',
                     'compose.external.yaml', 'Caddyfile', 'deploy/systemd/vaultmesh-agent.service']
            if proxy_api:
                names.append('INSTALLER_API')
            for name in names:
                data = (version if name == 'VERSION' else '2' if name == 'INSTALLER_API' else '# fixture') + '\n'
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
        (old / 'INSTALLER_API').write_text('2\n')
        for name in ['compose.yaml', 'compose.external.yaml', 'compose.managed.yaml', 'install.sh']:
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
valid_port 3000
valid_port 65535
! valid_port 0
! valid_port 443
! valid_port 65536
! valid_port 03000
! valid_port '3000\nBAD=1'
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

    def test_default_install_without_domain_uses_loopback_and_no_tls_claim(self):
        self.run_shell('PUBLIC_URL=; ACTION=install; control_action')
        settings = (self.install / '.env').read_text()
        self.assertIn('VAULTMESH_PROXY_MODE=external\n', settings)
        self.assertIn('VAULTMESH_PUBLIC_API_URL=http://127.0.0.1:3000\n', settings)
        self.assertIn('VAULTMESH_COOKIE_SECURE=false\n', settings)
        self.assertNotIn('VAULTMESH_HTTPS_ENABLED', settings)
        self.assertFalse(any('compose.managed.yaml' in ' '.join(c) for c in self.commands()))

    def test_default_port_skips_host_and_docker_nat_listeners(self):
        self.run_shell('export VM_BUSY_PORTS=80,443,3000; export VM_DOCKER_PORTS="0.0.0.0:3001->80/tcp"; PUBLIC_URL=; ACTION=install; control_action')
        self.assertIn('VAULTMESH_HTTP_PORT=3002\n', (self.install / '.env').read_text())
        self.assertTrue(any('http://127.0.0.1:3002/healthz' in c for c in self.commands()))

    def test_explicit_port_is_kept_and_conflict_fails_before_initialization(self):
        self.run_shell('export VM_BUSY_PORTS=8300; HTTP_PORT=8300; ACTION=install; control_action', ok=False)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse((self.install / 'releases').exists())
        self.assertFalse(any('pull' in c or 'up' in c or 'stop' in c for c in self.commands()))

    def test_custom_port_without_domain_has_matching_health_and_origin(self):
        self.run_shell('HTTP_PORT=8300; PUBLIC_URL=; ACTION=install; control_action')
        settings = (self.install / '.env').read_text()
        self.assertIn('VAULTMESH_HTTP_PORT=8300\n', settings)
        self.assertIn('VAULTMESH_PUBLIC_API_URL=http://127.0.0.1:8300\n', settings)
        self.assertTrue(any('http://127.0.0.1:8300/healthz' in c for c in self.commands()))

    def test_auto_port_exhaustion_does_not_initialize(self):
        occupied = ','.join(str(port) for port in range(3000, 3100))
        self.run_shell(f'export VM_BUSY_PORTS={occupied}; ACTION=install; control_action', ok=False)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse(any('pull' in c or 'up' in c for c in self.commands()))

    def test_domain_does_not_implicitly_claim_https_ports(self):
        self.run_shell('export VM_BUSY_PORTS=80,443; PUBLIC_URL=; DOMAIN=backup.example.test; ACTION=install; control_action')
        settings = (self.install / '.env').read_text()
        self.assertIn('VAULTMESH_PROXY_MODE=external\n', settings)
        self.assertIn('VAULTMESH_COOKIE_SECURE=true\n', settings)

    def test_managed_mode_checks_https_ports_before_creating_configuration(self):
        self.run_shell('export VM_BUSY_PORTS=443; MODE=managed; PUBLIC_URL=; DOMAIN=backup.example.test; ACTION=install; control_action', ok=False)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse((self.install / 'releases').exists())
        self.assertFalse(any('pull' in c or 'up' in c or 'stop' in c for c in self.commands()))

    def test_managed_mode_remains_explicitly_available(self):
        self.run_shell('MODE=managed; PUBLIC_URL=; DOMAIN=backup.example.test; ACTION=install; control_action')
        settings = (self.install / '.env').read_text()
        self.assertIn('VAULTMESH_PROXY_MODE=managed\n', settings)
        self.assertIn('VAULTMESH_COOKIE_SECURE=true\n', settings)
        self.assertTrue(any('https://backup.example.test/healthz' in c for c in self.commands()))

    def test_port_inventory_failure_is_not_treated_as_free(self):
        self.run_shell('ACTION=install; control_action', failure='listeners', ok=False)
        self.assertFalse((self.install / '.env').exists())

    def test_docker_nat_inventory_failure_is_not_treated_as_free(self):
        self.run_shell('ACTION=install; control_action', failure='ps --format', ok=False)
        self.assertFalse((self.install / '.env').exists())

    def test_invalid_origin_fails_without_initializing_database(self):
        self.run_shell('PUBLIC_URL=http://203.0.113.10:3000; ACTION=install; control_action', ok=False)
        self.assertFalse((self.install / '.env').exists())

    def test_old_bundle_is_rejected_before_new_proxy_installation(self):
        self.bundle('v0.2.0', proxy_api=False)
        result = self.run_shell('PUBLIC_URL=; ACTION=install; control_action', ok=False)
        self.assertIn('INSTALLER_API=2', result.stderr)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse(any('up' in c for c in self.commands()))

    def test_configure_proxy_keeps_credentials_and_version(self):
        self.existing_control()
        settings = self.install / '.env'
        with settings.open('a') as file:
            file.write('POSTGRES_PASSWORD=original-db-secret\nVAULTMESH_ADMIN_PASSWORD=original-admin-secret\nCUSTOM_SETTING=keep-me\n')
        original = settings.read_bytes()
        self.run_shell('HTTP_PORT=8300; PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action')
        updated = settings.read_text()
        for key in ['VAULTMESH_MASTER_KEY=fixture-key', 'POSTGRES_PASSWORD=original-db-secret',
                    'VAULTMESH_ADMIN_PASSWORD=original-admin-secret', 'CUSTOM_SETTING=keep-me']:
            self.assertIn(key + '\n', updated)
        self.assertIn('VAULTMESH_HTTP_PORT=8300\n', updated)
        self.assertIn('VAULTMESH_PUBLIC_API_URL=https://new.example.test\n', updated)
        self.assertIn('VAULTMESH_COOKIE_SECURE=true\n', updated)
        backup = next((self.install / 'backups').iterdir())
        self.assertEqual((backup / '.env').read_bytes(), original)
        self.assertEqual((self.install / 'current/VERSION').read_text(), 'v0.1.2\n')
        self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
        commands = self.commands()
        self.assertFalse(any('pull' in c or 'pg_dump' in c or 'dropdb' in c for c in commands))
        up = next(c for c in commands if 'up' in c)
        self.assertIn('--no-deps', up)
        self.assertNotIn('postgres', up)

    def test_configure_proxy_reuses_own_port(self):
        self.existing_control()
        self.run_shell('export VM_BUSY_PORTS=3000; PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action')
        self.assertFalse(any(c[0] == 'ss' for c in self.commands()))

    def test_configure_proxy_conflict_keeps_original_configuration(self):
        self.existing_control()
        before = (self.install / '.env').read_bytes()
        self.run_shell('export VM_BUSY_PORTS=8300; HTTP_PORT=8300; PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action', ok=False)
        self.assertEqual((self.install / '.env').read_bytes(), before)
        self.assertFalse(any('up' in c for c in self.commands()))

    def test_configure_proxy_invalid_compose_keeps_original_configuration(self):
        self.existing_control()
        before = (self.install / '.env').read_bytes()
        self.run_shell('PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action', failure='config --quiet', ok=False)
        self.assertEqual((self.install / '.env').read_bytes(), before)

    def test_configure_proxy_health_failure_preserves_recovery_copy(self):
        self.existing_control()
        before = (self.install / '.env').read_bytes()
        result = self.run_shell('PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action', failure='health', ok=False)
        self.assertIn('入口配置未完成', result.stderr)
        backup = next((self.install / 'backups').iterdir())
        self.assertEqual((backup / '.env').read_bytes(), before)
        self.assertEqual((self.install / 'current/VERSION').read_text(), 'v0.1.2\n')

    def test_old_installation_requires_documented_proxy_recovery(self):
        self.existing_control()
        (self.install / 'current/INSTALLER_API').unlink()
        before = (self.install / '.env').read_bytes()
        self.run_shell('PUBLIC_URL=https://new.example.test; ACTION=configure-proxy; control_action', ok=False)
        self.assertEqual((self.install / '.env').read_bytes(), before)

    def test_upgrade_cannot_silently_consume_proxy_options(self):
        result = subprocess.run(['sh', str(REPO / 'install.sh'), 'upgrade', '--port', '8300'], capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('升级不会改变现有入口', result.stderr)

    def test_existing_git_install_is_never_overwritten(self):
        (self.install / '.git').mkdir()
        original = self.install / '.env'
        original.write_text('original secret\n')
        self.run_shell('ACTION=install; control_action', ok=False)
        self.assertEqual(original.read_text(), 'original secret\n')

    def test_different_directory_cannot_adopt_existing_compose_stack(self):
        self.run_shell('ACTION=install; control_action', failure='existing-stack', ok=False)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse(any('up' in c or 'stop' in c for c in self.commands()))

    def test_fresh_install_cannot_reuse_orphaned_postgres_volume(self):
        self.run_shell('ACTION=install; control_action', failure='existing-volume', ok=False)
        self.assertFalse((self.install / '.env').exists())
        self.assertFalse(any('up' in c or 'stop' in c for c in self.commands()))

    def test_container_inventory_error_aborts_installation(self):
        self.run_shell('ACTION=install; control_action', failure='ps -a', ok=False)
        self.assertFalse((self.install / '.env').exists())

    def test_volume_inventory_error_aborts_installation(self):
        self.run_shell('ACTION=install; control_action', failure='volume ls', ok=False)
        self.assertFalse((self.install / '.env').exists())

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

    def test_upgrade_preserves_custom_http_port(self):
        self.existing_control()
        settings = self.install / '.env'
        with settings.open('a') as file:
            file.write('VAULTMESH_HTTP_PORT=8300\n')
        before = settings.read_bytes()
        self.run_shell('ACTION=upgrade; control_action')
        self.assertEqual(settings.read_bytes(), before)
        self.assertTrue(any('http://127.0.0.1:8300/healthz' in c for c in self.commands()))

    def test_upgrade_preserves_managed_mode(self):
        self.existing_control()
        settings = self.install / '.env'
        settings.write_text(settings.read_text().replace('VAULTMESH_PROXY_MODE=external', 'VAULTMESH_PROXY_MODE=managed'))
        before = settings.read_bytes()
        self.run_shell('ACTION=upgrade; control_action')
        self.assertEqual(settings.read_bytes(), before)
        self.assertTrue(any('https://backup.example.test/healthz' in c for c in self.commands()))
        self.assertTrue(any('compose.managed.yaml' in ' '.join(c) for c in self.commands()))

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
            self.assertTrue({'install.sh', 'compose.yaml', 'Caddyfile', 'VERSION', 'COMMIT', 'LICENSE', 'INSTALLER_API', 'docs/INSTALL.md', 'docs/UPGRADE.md'} <= names)
            self.assertFalse({'.env', '.git', 'node_modules', 'state.json'} & names)
            self.assertEqual(tar.getmember('./install.sh').mode & 0o111, 0o111)
        checksum = (target / 'vaultmesh-deploy-v0.2.0.tar.gz.sha256').read_text().split()[0]
        self.assertEqual(checksum, hashlib.sha256((target / 'vaultmesh-deploy-v0.2.0.tar.gz').read_bytes()).hexdigest())


if __name__ == '__main__':
    unittest.main()
