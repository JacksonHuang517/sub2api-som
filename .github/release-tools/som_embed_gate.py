#!/usr/bin/env python3
"""Verify the active build, not a stale archived Profile chunk."""
import re
from pathlib import Path


def verify(root):
    root = Path(root)
    html = (root / 'index.html').read_text()
    entries = re.findall(r'src="(/assets/[^"\n]+\.js)"', html)
    if len(entries) != 1:
        raise ValueError('ambiguous application entry')
    entry = (root / entries[0].lstrip('/')).read_text()
    profiles = set(re.findall(r'ProfileView-[A-Za-z0-9_-]+\.js', entry))
    if len(profiles) != 1:
        raise ValueError('active Profile mapping is missing or ambiguous')
    profile = (root / 'assets' / next(iter(profiles))).read_text()
    for required in ('关联飞书', '订阅用量', 'warning_percent'):
        if required not in profile:
            raise ValueError('active Profile lacks required Feishu subscription feature: ' + required)
    styles = re.findall(r'href="(/assets/[^"\n]+\.css)"', html)
    if not any('--som-canvas' in (root / path.lstrip('/')).read_text() for path in styles):
        raise ValueError('SOM theme missing from active CSS')
    if '/mock/session' in profile or 'local-fixture' in profile:
        raise ValueError('mock client entered production Profile')


if __name__ == '__main__':
    verify(Path('backend/internal/web/dist'))
    print('PASS: active SOM theme and Feishu subscription UI')
