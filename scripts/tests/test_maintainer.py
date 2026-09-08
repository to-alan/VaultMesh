"""Exercise push/tag guardrails against a fake git, never a real remote."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[2]
GIT = r'''#!/usr/bin/env python3
import json, os, sys
a = sys.argv[1:]
with open(os.environ['VM_GIT_LOG'], 'a') as f:
    f.write(json.dumps(a) + '\n')
if a[0] == 'status':
    print(os.environ.get('VM_DIRTY', ''), end='')
elif a[0] in ('symbolic-ref', 'branch'):
    print(os.environ.get('VM_BRANCH', 'main'))
elif a[:2] == ['rev-parse', '--verify']:
    sys.exit(0 if os.environ.get('VM_TAG_EXISTS') else 1)
elif a[0] == 'rev-parse':
    print('different' if a[-1] == 'origin/main' and os.environ.get('VM_DIVERGED') else 'same-head')
elif a[0] == 'merge-base':
    sys.exit(1 if os.environ.get('VM_DIVERGED') else 0)
elif a[0] == 'push' and os.environ.get('VM_PUSH_FAIL'):
    sys.exit(1)
'''


class MaintainerTests(unittest.TestCase):
    def run_script(self, name, *args, ok=True, **options):
        with tempfile.TemporaryDirectory(prefix='vaultmesh-git-test-') as directory:
            root = Path(directory)
            git = root / 'git'
            git.write_text(GIT)
            git.chmod(0o755)
            log = root / 'git.jsonl'
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ['PATH'], VM_GIT_LOG=str(log), **options)
            result = subprocess.run(['sh', str(REPO / 'scripts' / name), *args], env=env, capture_output=True, text=True)
            commands = [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []
        self.assertEqual(result.returncode == 0, ok, result.stdout + result.stderr)
        return commands

    def test_push_clean_branch_sets_upstream_without_force(self):
        commands = self.run_script('push.sh')
        self.assertIn(['push', '--set-upstream', 'origin', 'main'], commands)
        self.assertFalse(any('--force' in c or 'tag' in c or 'add' in c for c in commands))

    def test_push_dirty_tree_does_not_fetch_or_push(self):
        commands = self.run_script('push.sh', ok=False, VM_DIRTY=' M .env.example')
        self.assertFalse(any(c[0] in ['fetch', 'push'] for c in commands))

    def test_push_diverged_remote_refuses_force(self):
        commands = self.run_script('push.sh', ok=False, VM_DIVERGED='1')
        self.assertFalse(any(c[0] == 'push' for c in commands))

    def test_release_explicit_new_tag_only(self):
        commands = self.run_script('release.sh', 'v0.2.0-rc.1')
        self.assertIn(['tag', '-a', 'v0.2.0-rc.1', '-m', 'VaultMesh v0.2.0-rc.1'], commands)
        self.assertIn(['push', 'origin', 'refs/tags/v0.2.0-rc.1'], commands)

    def test_release_rejects_invalid_version_without_git(self):
        self.assertEqual(self.run_script('release.sh', 'latest', ok=False), [])

    def test_release_rejects_existing_tag(self):
        commands = self.run_script('release.sh', 'v0.2.0', ok=False, VM_TAG_EXISTS='1')
        self.assertFalse(any(c[0] in ['push', 'tag'] for c in commands))

    def test_release_rejects_unpushed_changes(self):
        commands = self.run_script('release.sh', 'v0.2.0', ok=False, VM_DIVERGED='1')
        self.assertFalse(any(c[0] in ['push', 'tag'] for c in commands))

    def test_release_rejects_feature_branch(self):
        commands = self.run_script('release.sh', 'v0.2.0', ok=False, VM_BRANCH='codex/feature')
        self.assertFalse(any(c[0] in ['push', 'tag'] for c in commands))


if __name__ == '__main__':
    unittest.main()
