#!/usr/bin/env python3
"""Validate upstream merges before PRs or explicitly enabled SOM releases.

Scheduled runs require SOM_SYNC_ENABLED=true and SOM_CUSTOM_BRANCH=som-custom.
SOM_AUTO_RELEASE=true additionally allows fast-forwarding that branch and
publishing an annotated tag. GITHUB_TOKEN requires contents, pull-requests and
actions write. Credentials are read from environment only, never written.
"""
import argparse
import json
import os
import re
import subprocess
import urllib.request
from pathlib import Path

VERSION_FILE = Path('backend/cmd/server/VERSION')
SHA_RE = re.compile(r'[0-9a-f]{40}')
STABLE_TAG_RE = re.compile(r'v(\d+\.\d+\.\d+)')


def run(*args, capture=False, check=True):
    return subprocess.run(args, check=check, text=True, stdout=subprocess.PIPE if capture else None)


def settings():
    base = os.environ.get('SOM_CUSTOM_BRANCH', '')
    repo = os.environ.get('SOM_UPSTREAM_REPO', 'Wei-Shaw/sub2api')
    branch = os.environ.get('SOM_UPSTREAM_BRANCH', '').strip()
    if base != 'som-custom':
        raise ValueError('SOM_CUSTOM_BRANCH must explicitly equal som-custom')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9][A-Za-z0-9_.-]*', repo) or '..' in repo:
        raise ValueError('invalid SOM_UPSTREAM_REPO')
    if branch:
        if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_./-]*', branch) or '..' in branch:
            raise ValueError('invalid SOM_UPSTREAM_BRANCH')
        run('git', 'check-ref-format', 'refs/heads/' + branch)
    return base, repo, branch


def auto_enabled():
    return os.environ.get('SOM_AUTO_RELEASE', '').lower() == 'true'


def version_tuple(version):
    match = re.fullmatch(r'v?(\d+)\.(\d+)\.(\d+)(?:-som\.\d+)?', version.strip())
    if not match:
        raise ValueError('expected x.y.z or x.y.z-som.N version')
    return tuple(int(part) for part in match.groups())


def stable_tag(release):
    tag = release.get('tag_name', '')
    if release.get('draft') or release.get('prerelease') or not STABLE_TAG_RE.fullmatch(tag):
        raise ValueError('latest upstream release must have a stable vX.Y.Z tag')
    return tag


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        # Never forward the in-memory API credential to another host or URL.
        return None


def latest_release_tag(repo):
    request = urllib.request.Request(
        f'https://api.github.com/repos/{repo}/releases/latest',
        headers={'Accept': 'application/vnd.github+json', 'User-Agent': 'SOM-Upstream-Sync'},
    )
    token = os.environ.get('GH_TOKEN', '')
    if token:
        request.add_header('Authorization', 'Bearer ' + token)
    with urllib.request.build_opener(NoRedirect()).open(request, timeout=30) as response:
        return stable_tag(json.load(response))


def output_plan(plan):
    Path(os.environ['SOM_SYNC_PLAN']).write_text(json.dumps(plan))
    with Path(os.environ['GITHUB_OUTPUT']).open('a') as output:
        output.write('has_changes=true\n')


def merge_source(upstream, version):
    result = run('git', 'merge', '--no-ff', '--no-commit', upstream, check=False)
    if result.returncode:
        conflicts = run('git', 'diff', '--name-only', '--diff-filter=U', capture=True).stdout.splitlines()
        if conflicts != [str(VERSION_FILE)]:
            run('git', 'merge', '--abort', check=False)
            raise ValueError('upstream merge conflicts require manual review')
    # VERSION is distribution metadata, resolved before all validation/builds.
    VERSION_FILE.write_text(version + '\n')
    run('git', 'add', '--', str(VERSION_FILE))
    run('git', 'commit', '-m', f'chore: merge upstream {upstream[:12]} for SOM {version}')


def merge():
    base, repo, branch = settings()
    if run('git', 'status', '--porcelain', capture=True).stdout.strip():
        raise ValueError('working tree must be clean')
    run('git', 'fetch', 'origin', f'{base}:refs/remotes/origin/{base}')
    base_sha = run('git', 'rev-parse', f'origin/{base}', capture=True).stdout.strip()
    current = run('git', 'show', f'{base_sha}:{VERSION_FILE}', capture=True).stdout.strip()
    ref = branch or latest_release_tag(repo)
    run('git', 'fetch', f'https://github.com/{repo}.git', branch or 'refs/tags/' + ref)
    upstream = run('git', 'rev-parse', 'FETCH_HEAD^{commit}', capture=True).stdout.strip()
    upstream_version = ref[1:] if not branch else run('git', 'show', f'{upstream}:{VERSION_FILE}', capture=True).stdout.strip()
    upstream_base = version_tuple(upstream_version)
    current_base = version_tuple(current)
    newer = upstream_base > current_base
    target_version = '.'.join(map(str, upstream_base)) + '-som.1'
    integrated = run('git', 'merge-base', '--is-ancestor', upstream, base_sha, check=False).returncode == 0
    # Retry source/tag/dispatch publication only for the exact distribution version
    # already integrated by a previous successful merge. No new source commit.
    retry = integrated and auto_enabled() and current == target_version
    if integrated and not retry and not newer:
        return
    head = 'som-sync/' + upstream[:12]
    run('git', 'checkout', '-b', head, base_sha)
    run('git', 'config', 'user.name', 'github-actions[bot]')
    run('git', 'config', 'user.email', '41898282+github-actions[bot]@users.noreply.github.com')
    version = target_version if newer else current
    if not retry:
        merge_source(upstream, version)
    prepared_sha = run('git', 'rev-parse', 'HEAD', capture=True).stdout.strip()
    output_plan({'base': base, 'base_sha': base_sha, 'head': head, 'upstream': upstream,
                 'repo': repo, 'prepared_sha': prepared_sha, 'version': version,
                 'auto_eligible': newer or retry, 'retry': retry})


def remote_tag_sha(tag):
    result = run('git', 'ls-remote', '--tags', 'origin', f'refs/tags/{tag}', f'refs/tags/{tag}^{{}}', capture=True)
    refs = dict(line.split()[::-1] for line in result.stdout.splitlines())
    return refs.get(f'refs/tags/{tag}^{{}}', refs.get(f'refs/tags/{tag}'))


def dispatch_release(plan):
    tag = 'v' + plan['version']
    release = run('gh', 'release', 'view', tag, '--json', 'tagName,isDraft,isPrerelease', capture=True, check=False)
    if release.returncode == 0:
        record = json.loads(release.stdout)
        if record.get('tagName') != tag or record.get('isDraft') or record.get('isPrerelease'):
            raise ValueError('existing release is not the expected stable distribution')
        return
    runs = run('gh', 'run', 'list', '--workflow', 'release.yml', '--branch', plan['base'],
               '--event', 'workflow_dispatch', '--limit', '30', '--json', 'status,headSha', capture=True)
    if any(record.get('headSha') == plan['prepared_sha'] and record.get('status') != 'completed'
           for record in json.loads(runs.stdout)):
        return
    # GITHUB_TOKEN tag pushes do not start push-triggered workflows; dispatch explicitly.
    run('gh', 'workflow', 'run', 'release.yml', '--ref', plan['base'], '-f', f'tag={tag}',
        '-f', 'simple_release=false', '-f', 'dry_run=false')


def publish_release(plan):
    # Compare immediately before normal fast-forward push. A concurrent divergent
    # update is also rejected by git itself; no force/lease override is used.
    remote = run('git', 'ls-remote', 'origin', f'refs/heads/{plan["base"]}', capture=True).stdout.split()
    if not remote or remote[0] != plan['base_sha']:
        raise ValueError('som-custom changed since preparation; refusing source push')
    tag = 'v' + plan['version']
    existing_tag = remote_tag_sha(tag)
    if existing_tag and existing_tag != plan['prepared_sha']:
        raise ValueError('release tag already exists at a different commit')
    if not plan['retry']:
        run('git', 'push', 'origin', f'HEAD:refs/heads/{plan["base"]}')
    if not existing_tag:
        run('git', 'tag', '-a', tag, '-m', f'SOM distribution {plan["version"]} from upstream {plan["upstream"]}', plan['prepared_sha'])
        run('git', 'push', 'origin', f'refs/tags/{tag}:refs/tags/{tag}')
    dispatch_release(plan)


def publish_pr(plan):
    base, head = plan['base'], plan['head']
    existing = run('gh', 'pr', 'list', '--base', base, '--head', head, '--state', 'open', '--json', 'number', capture=True)
    if json.loads(existing.stdout):
        return
    run('git', 'push', 'origin', f'HEAD:refs/heads/{head}')
    body = Path(os.environ['SOM_SYNC_PLAN']).with_suffix('.md')
    body.write_text(f'Merge {plan["repo"]} at `{plan["upstream"]}` into `{base}` while preserving SOM and Feishu customizations.\n\nValidation: Feishu sidecar and backend updater tests, frontend typecheck/critical/Feishu tests, frontend and embedded backend builds.\n\nReview and merge manually. Automatic release was disabled or the upstream base version did not advance.\n')
    run('gh', 'pr', 'create', '--base', base, '--head', head, '--title', f'chore: sync upstream {plan["upstream"][:12]} into SOM', '--body-file', str(body))


def publish():
    base, repo, _ = settings()
    plan = json.loads(Path(os.environ['SOM_SYNC_PLAN']).read_text())
    if plan['base'] != base or plan['repo'] != repo or any(not SHA_RE.fullmatch(plan[key]) for key in ('upstream', 'base_sha', 'prepared_sha')):
        raise ValueError('invalid sync plan')
    head = 'som-sync/' + plan['upstream'][:12]
    if plan['head'] != head or run('git', 'branch', '--show-current', capture=True).stdout.strip() != head:
        raise ValueError('checkout differs from validated sync plan')
    if run('git', 'rev-parse', 'HEAD', capture=True).stdout.strip() != plan['prepared_sha']:
        raise ValueError('source commit changed after preparation')
    run('git', 'merge-base', '--is-ancestor', plan['upstream'], 'HEAD')
    if run('git', 'status', '--porcelain', '--untracked-files=no', capture=True).stdout.strip():
        raise ValueError('validation changed tracked files')
    if VERSION_FILE.read_text().strip() != plan['version']:
        raise ValueError('VERSION differs from validated plan')
    version_tuple(plan['version'])
    if auto_enabled() and plan['auto_eligible']:
        publish_release(plan)
    else:
        publish_pr(plan)


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('merge', 'publish'))
    args = parser.parse_args()
    (merge if args.mode == 'merge' else publish)()
