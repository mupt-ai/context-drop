import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("notify", Path(__file__).with_name("notify.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class NotificationTests(unittest.TestCase):
    def test_cooldown_including_failed_sends(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(module.subprocess, "run") as send:
            send.return_value.returncode = 1
            state = Path(directory)
            self.assertEqual(module.notify(["send", "--text", "failure"], state, "fake", 100000), 1)
            self.assertEqual(module.notify(["send"], state, "fake", 100300), 0)
            self.assertEqual(send.call_count, 1)
            module.notify(["send"], state, "fake", 186400)
            self.assertEqual(send.call_count, 2)


if __name__ == "__main__":
    unittest.main()
