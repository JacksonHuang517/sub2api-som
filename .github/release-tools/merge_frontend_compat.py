#!/usr/bin/env python3
"""Retain the deployed SOM overlay's historical hashed assets in custom builds."""
from pathlib import Path
import re
import tarfile


def merge(archive, destination):
    destination = Path(destination)
    files = {}
    with tarfile.open(archive, 'r:gz') as bundle:
        for member in bundle:
            if not member.isfile() or not re.fullmatch(r'assets/[A-Za-z0-9_.-]+\.(js|css)', member.name):
                raise ValueError('invalid compatibility asset')
            if member.name in files or member.size > 8 * 1024 * 1024 or len(files) >= 512:
                raise ValueError('invalid compatibility archive size or duplicate')
            files[member.name] = bundle.extractfile(member).read()
    if sum(map(len, files.values())) > 64 * 1024 * 1024:
        raise ValueError('compatibility archive too large')
    # Check all collisions before writing any file.
    for name, data in files.items():
        path = destination / name
        if path.exists() and path.read_bytes() != data:
            raise ValueError('hashed asset collision: ' + name)
    for name, data in files.items():
        path = destination / name
        path.parent.mkdir(parents=True, exist_ok=True)
        if not path.exists():
            path.write_bytes(data)
    return len(files)


if __name__ == '__main__':
    count = merge('integrations/frontend-compat/legacy-assets.tar.gz', 'backend/internal/web/dist')
    print(f'PASS: retained {count} historical hashed assets')
