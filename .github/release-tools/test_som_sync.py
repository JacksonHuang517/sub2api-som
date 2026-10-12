import contextlib
import importlib.util
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('som_sync', Path(__file__).with_name('som_sync.py'))
sync = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sync)


def result(args=(), stdout='', code=0):
    return subprocess.CompletedProcess(args, code, stdout)


class SettingsTests(unittest.TestCase):
    @patch.object(sync, 'run')
    def test_default_stable_release_and_no_auto(self, run):
        with patch.dict(os.environ, {'SOM_CUSTOM_BRANCH': 'som-custom'}, clear=True):
            self.assertEqual(sync.settings(), ('som-custom', 'Wei-Shaw/sub2api', ''))
            self.assertFalse(sync.auto_enabled())
        run.assert_not_called()

    @patch.object(sync, 'run')
    def test_explicit_ref(self, run):
        with patch.dict(os.environ, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_UPSTREAM_BRANCH': 'release/test'}, clear=True):
            self.assertEqual(sync.settings()[2], 'release/test')
        run.assert_called_once_with('git', 'check-ref-format', 'refs/heads/release/test')

    def test_invalid_settings(self):
        for values in ({}, {'SOM_CUSTOM_BRANCH': 'main'}, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_UPSTREAM_REPO': 'https://github.com/a/b'}, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_UPSTREAM_BRANCH': '--upload-pack=x'}, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_UPSTREAM_BRANCH': 'main..other'}, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_UPSTREAM_BRANCH': 'main;echo unsafe'}):
            with patch.dict(os.environ, values, clear=True):
                with self.assertRaises(ValueError):
                    sync.settings()

    def test_stable_tag_rejects_prereleases_and_non_versions(self):
        self.assertEqual(sync.stable_tag({'tag_name': 'v0.2.16', 'draft': False, 'prerelease': False}), 'v0.2.16')
        for release in ({'tag_name': 'v0.2.16-rc.1'}, {'tag_name': 'v0.2.16', 'prerelease': True}, {'tag_name': 'v0.2.16', 'draft': True}, {'tag_name': '--bad'}, {'tag_name': 'v0.2.16/unsafe'}):
            with self.assertRaises(ValueError):
                sync.stable_tag(release)

    @patch.object(sync.urllib.request, 'build_opener')
    def test_fixed_api_and_memory_only_token(self, opener):
        import io
        opener.return_value.open.return_value.__enter__.return_value = io.BytesIO(b'{"tag_name":"v0.2.16"}')
        with patch.dict(os.environ, {'GH_TOKEN': 'test-credential'}, clear=True):
            self.assertEqual(sync.latest_release_tag('owner/repo'), 'v0.2.16')
        request = opener.return_value.open.call_args.args[0]
        self.assertEqual(request.full_url, 'https://api.github.com/repos/owner/repo/releases/latest')
        self.assertEqual(request.get_header('Authorization'), 'Bearer test-credential')
        self.assertIsNone(sync.NoRedirect().redirect_request(None, None, 302, '', {}, 'https://other.example'))

    def test_base_version_comparison(self):
        self.assertGreater(sync.version_tuple('0.2.16'), sync.version_tuple('0.2.15-som.99'))
        self.assertGreater(sync.version_tuple('0.10.1'), sync.version_tuple('0.9.99'))
        self.assertEqual(sync.version_tuple('0.2.15'), sync.version_tuple('0.2.15-som.1'))
        with self.assertRaises(ValueError):
            sync.version_tuple('0.2.16-rc.1')


class PreparationTests(unittest.TestCase):
    def prepare(self, current, integrated=False, auto=False):
        def fake_run(*args, **kwargs):
            if args[:2] == ('git', 'rev-parse'):
                if args[2] == 'origin/som-custom':
                    return result(stdout='a' * 40)
                return result(stdout=('b' if args[2].startswith('FETCH_HEAD') else 'c') * 40)
            if args[:2] == ('git', 'show'):
                return result(stdout=current)
            if args[:2] == ('git', 'merge-base'):
                return result(code=0 if integrated else 1)
            return result()
        with patch.dict(os.environ, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_AUTO_RELEASE': 'true' if auto else 'false'}, clear=True), patch.object(sync, 'latest_release_tag', return_value='v0.2.16') as latest, patch.object(sync, 'run', side_effect=fake_run) as run, patch.object(sync, 'merge_source') as merge_source, patch.object(sync, 'output_plan') as output:
            sync.merge()
            latest.assert_called_once_with('Wei-Shaw/sub2api')
            run.assert_any_call('git', 'fetch', 'https://github.com/Wei-Shaw/sub2api.git', 'refs/tags/v0.2.16')
            return merge_source, output

    def test_new_stable_base_sets_distribution_version_before_validation(self):
        merge_source, output = self.prepare('0.2.15-som.9')
        merge_source.assert_called_once_with('b' * 40, '0.2.16-som.1')
        plan = output.call_args.args[0]
        self.assertEqual(plan['base_sha'], 'a' * 40)
        self.assertEqual(plan['prepared_sha'], 'c' * 40)
        self.assertTrue(plan['auto_eligible'])
        self.assertFalse(plan['retry'])

    def test_new_tag_already_in_source_still_advances_version(self):
        merge_source, output = self.prepare('0.2.15-som.1', integrated=True, auto=True)
        merge_source.assert_called_once_with('b' * 40, '0.2.16-som.1')
        self.assertTrue(output.call_args.args[0]['auto_eligible'])

    def test_same_or_older_base_never_auto_releases(self):
        for current in ('0.2.16-som.2', '0.2.17-som.1'):
            merge_source, output = self.prepare(current, auto=True)
            merge_source.assert_called_once_with('b' * 40, current)
            self.assertFalse(output.call_args.args[0]['auto_eligible'])

    def test_retry_integrated_version_has_no_second_source_commit(self):
        merge_source, output = self.prepare('0.2.16-som.1', integrated=True, auto=True)
        merge_source.assert_not_called()
        self.assertTrue(output.call_args.args[0]['retry'])

    def test_already_integrated_no_auto_is_noop(self):
        merge_source, output = self.prepare('0.2.16-som.1', integrated=True)
        merge_source.assert_not_called()
        output.assert_not_called()


class MergeConflictTests(unittest.TestCase):
    def fixture(self, directory, other_conflict=False):
        def git(*args):
            return subprocess.run(('git', '-C', directory, *args), check=True, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE).stdout.strip()
        git('init', '-b', 'som-custom')
        git('config', 'user.name', 'Test')
        git('config', 'user.email', 'test@example.invalid')
        root = Path(directory)
        (root / 'VERSION').write_text('0.2.15\n')
        (root / 'source').write_text('base\n')
        git('add', '.')
        git('commit', '-m', 'base')
        git('checkout', '-b', 'upstream')
        (root / 'VERSION').write_text('0.2.16\n')
        (root / 'source').write_text('upstream\n')
        git('add', '.')
        git('commit', '-m', 'upstream')
        upstream = git('rev-parse', 'HEAD')
        git('checkout', 'som-custom')
        (root / 'VERSION').write_text('0.2.15-som.1\n')
        if other_conflict:
            (root / 'source').write_text('custom\n')
        (root / 'custom').write_text('preserved\n')
        git('add', '.')
        git('commit', '-m', 'custom')
        return git, upstream

    def check_merge(self, other_conflict):
        with tempfile.TemporaryDirectory() as directory:
            git, upstream = self.fixture(directory, other_conflict)
            before = git('rev-parse', 'HEAD')
            cwd = os.getcwd()
            def quiet_run(*args, capture=False, check=True):
                return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            try:
                os.chdir(directory)
                with patch.object(sync, 'VERSION_FILE', Path('VERSION')), patch.object(sync, 'run', side_effect=quiet_run):
                    if other_conflict:
                        with self.assertRaisesRegex(ValueError, 'manual review'):
                            sync.merge_source(upstream, '0.2.16-som.1')
                    else:
                        sync.merge_source(upstream, '0.2.16-som.1')
            finally:
                os.chdir(cwd)
            self.assertEqual(git('status', '--porcelain'), '')
            if other_conflict:
                self.assertEqual(git('rev-parse', 'HEAD'), before)
                self.assertEqual((Path(directory) / 'VERSION').read_text(), '0.2.15-som.1\n')
            else:
                self.assertEqual((Path(directory) / 'VERSION').read_text(), '0.2.16-som.1\n')
                self.assertEqual((Path(directory) / 'custom').read_text(), 'preserved\n')
                self.assertEqual((Path(directory) / 'source').read_text(), 'upstream\n')
                git('merge-base', '--is-ancestor', upstream, 'HEAD')

    def test_only_version_conflict_resolved_before_validation(self):
        self.check_merge(False)

    def test_other_conflict_aborts_without_changing_source(self):
        self.check_merge(True)


class PublishTests(unittest.TestCase):
    def plan(self):
        return {'base': 'som-custom', 'base_sha': 'a' * 40, 'head': 'som-sync/' + 'b' * 12,
                'upstream': 'b' * 40, 'prepared_sha': 'c' * 40, 'repo': 'Wei-Shaw/sub2api',
                'version': '0.2.16-som.1', 'auto_eligible': True, 'retry': False}

    @patch.object(sync, 'run')
    def test_cas_refuses_any_write(self, run):
        run.return_value = result(stdout='d' * 40 + '\trefs/heads/som-custom\n')
        with self.assertRaisesRegex(ValueError, 'changed since preparation'):
            sync.publish_release(self.plan())
        self.assertEqual(len(run.call_args_list), 1)

    @patch.object(sync, 'dispatch_release')
    @patch.object(sync, 'remote_tag_sha')
    @patch.object(sync, 'run')
    def test_new_release_normal_push_annotated_tag_dispatch(self, run, remote_tag, dispatch):
        run.return_value = result(stdout='a' * 40 + '\trefs/heads/som-custom\n')
        remote_tag.return_value = None
        plan = self.plan()
        sync.publish_release(plan)
        run.assert_any_call('git', 'push', 'origin', 'HEAD:refs/heads/som-custom')
        run.assert_any_call('git', 'tag', '-a', 'v0.2.16-som.1', '-m', 'SOM distribution 0.2.16-som.1 from upstream ' + 'b' * 40, 'c' * 40)
        run.assert_any_call('git', 'push', 'origin', 'refs/tags/v0.2.16-som.1:refs/tags/v0.2.16-som.1')
        dispatch.assert_called_once_with(plan)
        self.assertFalse(any('--force' in call.args for call in run.call_args_list))

    @patch.object(sync, 'dispatch_release')
    @patch.object(sync, 'remote_tag_sha')
    @patch.object(sync, 'run')
    def test_existing_tag_retry_does_not_rewrite_source_or_tag(self, run, remote_tag, dispatch):
        run.return_value = result(stdout='a' * 40 + '\trefs/heads/som-custom\n')
        remote_tag.return_value = 'c' * 40
        plan = self.plan()
        plan['retry'] = True
        sync.publish_release(plan)
        dispatch.assert_called_once_with(plan)
        self.assertFalse(any(call.args[:2] in [('git', 'push'), ('git', 'tag')] for call in run.call_args_list))

    @patch.object(sync, 'remote_tag_sha', return_value='d' * 40)
    @patch.object(sync, 'run')
    def test_existing_tag_at_other_commit_rejected(self, run, remote_tag):
        run.return_value = result(stdout='a' * 40 + '\trefs/heads/som-custom\n')
        with self.assertRaisesRegex(ValueError, 'different commit'):
            sync.publish_release(self.plan())
        self.assertFalse(any(call.args[:2] == ('git', 'push') for call in run.call_args_list))

    @patch.object(sync, 'run')
    def test_existing_stable_release_skips_dispatch(self, run):
        run.return_value = result(stdout='{"tagName":"v0.2.16-som.1","isDraft":false,"isPrerelease":false}')
        sync.dispatch_release(self.plan())
        self.assertEqual(len(run.call_args_list), 1)

    @patch.object(sync, 'run')
    def test_missing_release_dispatches_explicit_workflow(self, run):
        run.side_effect = [result(code=1), result(stdout='[]'), result()]
        sync.dispatch_release(self.plan())
        run.assert_called_with('gh', 'workflow', 'run', 'release.yml', '--ref', 'som-custom', '-f', 'tag=v0.2.16-som.1', '-f', 'simple_release=false', '-f', 'dry_run=false')

    def test_default_mode_and_non_new_upstream_use_pr(self):
        for auto, eligible in [('', True), ('true', False)]:
            with tempfile.TemporaryDirectory() as directory:
                plan = self.plan()
                plan['auto_eligible'] = eligible
                plan_file = Path(directory) / 'plan.json'
                plan_file.write_text(json.dumps(plan))
                version_file = Path(directory) / 'VERSION'
                version_file.write_text(plan['version'])
                def fake_run(*args, **kwargs):
                    if args[:2] == ('git', 'branch'):
                        return result(stdout=plan['head'])
                    if args[:2] == ('git', 'rev-parse'):
                        return result(stdout=plan['prepared_sha'])
                    return result()
                with patch.dict(os.environ, {'SOM_CUSTOM_BRANCH': 'som-custom', 'SOM_SYNC_PLAN': str(plan_file), 'SOM_AUTO_RELEASE': auto}, clear=True), patch.object(sync, 'VERSION_FILE', version_file), patch.object(sync, 'run', side_effect=fake_run), patch.object(sync, 'publish_pr') as pr, patch.object(sync, 'publish_release') as release:
                    sync.publish()
                    pr.assert_called_once_with(plan)
                    release.assert_not_called()


if __name__ == '__main__':
    unittest.main()
