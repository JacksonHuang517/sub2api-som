import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('compat', Path(__file__).with_name('merge_frontend_compat.py'))
compat = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compat)


class CompatibilityTest(unittest.TestCase):
    def archive(self, root, files):
        archive = Path(root) / 'compat.tar.gz'
        with tarfile.open(archive, 'w:gz') as bundle:
            for name, data in files.items():
                member = tarfile.TarInfo(name)
                member.size = len(data)
                bundle.addfile(member, io.BytesIO(data))
        return archive

    def test_collision_checks_precede_all_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'assets').mkdir()
            (root / 'assets/existing.js').write_bytes(b'new')
            archive = self.archive(root, {'assets/old.js': b'old', 'assets/existing.js': b'different'})
            with self.assertRaises(ValueError):
                compat.merge(archive, root)
            self.assertFalse((root / 'assets/old.js').exists())

    def test_path_escape_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = self.archive(directory, {'../outside.js': b'unsafe'})
            with self.assertRaises(ValueError):
                compat.merge(archive, directory)

    def test_same_hash_asset_retained(self):
        with tempfile.TemporaryDirectory() as directory:
            archive = self.archive(directory, {'assets/old.js': b'old'})
            self.assertEqual(compat.merge(archive, directory), 1)
            self.assertEqual(compat.merge(archive, directory), 1)
