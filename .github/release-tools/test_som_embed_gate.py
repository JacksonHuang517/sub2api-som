import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location('gate', Path(__file__).with_name('som_embed_gate.py'))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class EmbedGateTest(unittest.TestCase):
    def fixture(self, directory, text):
        root = Path(directory)
        (root / 'assets').mkdir()
        (root / 'index.html').write_text('<script src="/assets/index-new.js"></script><link href="/assets/index-new.css">')
        (root / 'assets/index-new.js').write_text('import("./ProfileView-current.js")')
        (root / 'assets/index-new.css').write_text(':root{--som-canvas:#fff}')
        (root / 'assets/ProfileView-current.js').write_text(text)
        (root / 'assets/ProfileView-old.js').write_text('关联飞书 订阅用量 warning_percent')
        return root

    def test_stale_profile_cannot_satisfy_gate(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                gate.verify(self.fixture(directory, 'old upstream profile'))

    def test_active_custom_build_passes(self):
        with tempfile.TemporaryDirectory() as directory:
            gate.verify(self.fixture(directory, '关联飞书 订阅用量 warning_percent'))

    def test_mock_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(ValueError):
                gate.verify(self.fixture(directory, '关联飞书 订阅用量 warning_percent /mock/session'))
