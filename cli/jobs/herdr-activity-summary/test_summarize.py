import importlib.util
import io
import json
from pathlib import Path
import sqlite3
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
from datetime import datetime, timedelta, timezone

spec = importlib.util.spec_from_file_location("activity", Path(__file__).with_name("summarize.py"))
activity = importlib.util.module_from_spec(spec)
spec.loader.exec_module(activity)


def pane(output="old output", **extra):
    return {"tab": "coding", "output": output, "agent": "pi", "status": "working", **extra}


class ActivityTests(unittest.TestCase):
    def setUp(self):
        self.db = sqlite3.connect(":memory:")
        self.addCleanup(self.db.close)
        self.db.executescript(activity.SCHEMA)
        self.options = SimpleNamespace(interval_seconds=300, timezone="America/Los_Angeles", model="test-model", ollama_url="http://127.0.0.1:1")
        self.now = datetime(2026, 9, 9, 16, 0, tzinfo=timezone.utc)  # 9 AM Pacific
        self.panes = {"p1": pane()}
        self.model = Mock(return_value="Avyay worked on tests in Herdr.")

    def sample(self):
        return activity.sample(self.db, self.options, lambda _: self.panes, self.model, lambda: self.now)

    def count(self):
        return self.db.execute("SELECT count(*) FROM activity_summaries").fetchone()[0]

    def baseline(self):
        self.assertFalse(self.sample())
        self.now += timedelta(minutes=5)

    def test_first_sample_is_only_a_baseline(self):
        self.assertFalse(self.sample())
        self.assertEqual(self.count(), 0)
        self.model.assert_not_called()
        self.assertIsNotNone(self.db.execute("SELECT * FROM snapshot").fetchone())

    def test_unchanged_snapshot_advances_checkpoint_without_model_or_row(self):
        self.baseline()
        self.assertFalse(self.sample())
        self.assertEqual(self.count(), 0)
        self.model.assert_not_called()
        self.assertEqual(self.db.execute("SELECT captured_at FROM snapshot").fetchone()[0], activity.timestamp(self.now))

    def test_activity_appends_one_row_and_only_new_output_reaches_model(self):
        self.baseline()
        self.panes = {"p1": pane("old output\nnew work")}
        self.assertTrue(self.sample())
        self.assertEqual(self.count(), 1)
        evidence = self.model.call_args.args[0]
        self.assertEqual(evidence["changes"][0]["new_or_redrawn_output"], "new work")
        self.assertEqual(evidence["coverage"], "interval")
        self.assertEqual(self.db.execute("SELECT summary, model FROM activity_summaries").fetchone(), ("Avyay worked on tests in Herdr.", "test-model"))

    def test_model_can_skip_noise_without_writing_activity(self):
        self.baseline()
        self.panes = {"p1": pane("a redraw")}
        self.model.return_value = None
        self.assertFalse(self.sample())
        self.assertEqual(self.count(), 0)
        self.assertIn("a redraw", self.db.execute("SELECT panes_json FROM snapshot").fetchone()[0])

    def test_duplicate_bucket_does_not_resample_even_if_no_row_was_written(self):
        self.assertFalse(self.sample())
        self.panes = {"p1": pane("new work")}
        self.assertFalse(self.sample())
        self.model.assert_not_called()

    def test_failure_keeps_checkpoint_and_does_not_insert_a_row(self):
        self.baseline()
        before = self.db.execute("SELECT * FROM snapshot").fetchone()
        self.panes = {"p1": pane("new work")}
        self.model.side_effect = TimeoutError()
        with self.assertRaises(TimeoutError):
            self.sample()
        self.assertEqual(self.count(), 0)
        self.assertEqual(self.db.execute("SELECT * FROM snapshot").fetchone(), before)

    def test_discovery_failure_does_not_become_no_activity(self):
        with self.assertRaises(RuntimeError):
            activity.sample(self.db, self.options, Mock(side_effect=RuntimeError()), self.model, lambda: self.now)
        self.assertEqual(self.count(), 0)
        self.assertIsNone(self.db.execute("SELECT * FROM snapshot").fetchone())

    def test_long_gap_rebaselines_instead_of_attributing_stale_work(self):
        self.baseline()
        self.now += timedelta(hours=1)
        self.panes = {"p1": pane("unknown-age work")}
        self.assertFalse(self.sample())
        self.assertEqual(self.count(), 0)
        self.model.assert_not_called()

    def test_quiet_hours_do_not_observe_or_write(self):
        for hour, minute in [(15, 59), (4, 0), (6, 0)]:
            with self.subTest(hour=hour, minute=minute):
                now = self.now.replace(hour=hour, minute=minute)
                collector = Mock(side_effect=AssertionError("should not read Herdr"))
                self.assertFalse(activity.sample(self.db, self.options, collector, self.model, lambda: now))
        self.assertIsNone(self.db.execute("SELECT * FROM snapshot").fetchone())

    def test_daylight_saving_timezone_is_used(self):
        self.now = datetime(2026, 12, 9, 16, 55, tzinfo=timezone.utc)  # 8:55 AM PST
        self.assertFalse(self.sample())
        self.assertIsNone(self.db.execute("SELECT * FROM snapshot").fetchone())
        self.now += timedelta(minutes=5)
        self.assertFalse(self.sample())  # 9 AM baseline
        self.assertIsNotNone(self.db.execute("SELECT * FROM snapshot").fetchone())

    def test_new_pane_does_not_pass_old_scrollback_to_model(self):
        result = activity.changes({}, {"p1": pane("old private content")})
        self.assertNotIn("old private content", json.dumps(result))
        self.assertIn("unknown age", result[0]["event"])

    def test_model_response_validation(self):
        for content, expected in [('{"summary": null}', None), ('{"summary": "Worked on tests."}', "Worked on tests.")]:
            with self.subTest(content=content):
                response = io.BytesIO(json.dumps({"message": {"content": content}}).encode())
                with patch.object(activity.urllib.request, "urlopen", return_value=response):
                    self.assertEqual(activity.summarize({}, self.options), expected)
        for content in ['{}', 'not json', '{"summary": ""}', '{"summary": 123}']:
            with self.subTest(content=content):
                response = io.BytesIO(json.dumps({"message": {"content": content}}).encode())
                with patch.object(activity.urllib.request, "urlopen", return_value=response):
                    with self.assertRaises(ValueError):
                        activity.summarize({}, self.options)

    def test_model_evidence_is_bounded(self):
        self.panes = {str(i): pane() for i in range(50)}
        self.baseline()
        self.panes = {str(i): pane("x" * 4000) for i in range(50)}
        self.assertTrue(self.sample())
        evidence = self.model.call_args.args[0]
        self.assertGreater(evidence["omitted_changed_panes"], 0)
        self.assertLess(len(json.dumps(evidence)), 25000)


if __name__ == "__main__":
    unittest.main()
