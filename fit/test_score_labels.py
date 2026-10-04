import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from score_labels import score_episode


class EpisodeScoringTests(unittest.TestCase):
    def test_counts_missed_ads_false_cuts_and_short_errors_without_double_counting(self):
        label = {
            "duration": 100,
            "segments": [
                {"start": 15, "end": 20, "borderline": True},
                {"start": 20, "end": 40, "borderline": False},
                {"start": 30, "end": 40, "borderline": False},
                {"start": 40, "end": 45, "borderline": True},
                {"start": 70, "end": 80, "borderline": False},
            ],
        }
        record = {"detection": {"Regions": [
            {"Start": 10, "End": 22},
            {"Start": 18, "End": 25},
            {"Start": 60, "End": 63},
        ]}}
        metrics = score_episode(label, record)["regions"]
        self.assertEqual(metrics["content_removed_seconds"], 8)
        self.assertEqual(metrics["required_retained_seconds"], 25)
        self.assertEqual(metrics["worst_content_cut_seconds"], 5)
        self.assertEqual(metrics["required_seconds"], 30)
        self.assertEqual(metrics["missed_blocks"], 1)
        self.assertEqual(metrics["partially_cut_blocks"], 1)

    def test_reports_merging_and_rendering_content_loss_separately(self):
        label = {"duration": 100, "segments": [
            {"start": 20, "end": 30, "borderline": False},
            {"start": 31, "end": 40, "borderline": False},
        ]}
        record = {
            "detection": {
                "Regions": [{"Start": 20, "End": 30}, {"Start": 31, "End": 40}],
                "Segments": [{"Start": 20, "End": 40}],
            },
            "keep": [{"Start": 0, "End": 18}, {"Start": 43, "End": 100}],
        }
        views = score_episode(label, record)
        self.assertEqual(views["regions"]["content_removed_seconds"], 0)
        self.assertEqual(views["detector"]["content_removed_seconds"], 1)
        self.assertEqual(views["rendered"]["content_removed_seconds"], 6)
        self.assertEqual(views["rendered"]["required_retained_seconds"], 0)
        self.assertEqual(views["detector"]["fully_cut_blocks"], 2)

    def test_empty_detection_counts_all_required_material_as_retained(self):
        label = {"duration": 100, "segments": [
            {"start": 20, "end": 30, "borderline": False},
            {"start": 30, "end": 50, "borderline": True},
        ]}
        views = score_episode(label, {"detection": {"Regions": None, "Segments": None}})
        self.assertNotIn("rendered", views)
        self.assertEqual(views["detector"]["content_removed_seconds"], 0)
        self.assertEqual(views["detector"]["required_retained_seconds"], 10)
        self.assertEqual(views["detector"]["missed_blocks"], 1)

    def test_counts_false_cuts_on_ad_free_episodes_and_clamps_to_duration(self):
        metrics = score_episode(
            {"duration": 100, "segments": []},
            {"detection": {"Regions": [{"Start": -2, "End": 5}, {"Start": 95, "End": 105}]}},
        )["regions"]
        self.assertEqual(metrics["content_removed_seconds"], 10)
        self.assertEqual(metrics["required_retained_seconds"], 0)

    def test_invalid_record_shapes_are_not_scored_as_empty_cuts(self):
        for record in (None, [], {"detection": {"Regions": False}},
                       {"detection": {"Regions": [], "Segments": {}}},
                       {"detection": {"Regions": []}, "keep": {}}):
            with self.subTest(record=record):
                with self.assertRaises(ValueError):
                    score_episode({"duration": 100, "segments": []}, record)

    def test_cli_rejects_unmatched_selections_and_marks_unknown_label_provenance(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            labels = root / "labels"
            labels.mkdir()
            (labels / "present.json").write_text(json.dumps({
                "video_id": "present", "channel": "show", "duration": 100, "segments": [],
            }))
            record = root / "renders" / "show" / "present" / "render.m4a.json"
            record.parent.mkdir(parents=True)
            record.write_text(json.dumps({"detection": {"Regions": [], "Segments": []}}))
            result = subprocess.run([
                sys.executable, str(Path(__file__).with_name("score_labels.py")),
                str(root / "renders"), "-labels", str(labels), "--video", "present,absent",
                "--json", str(root / "scores.json"),
            ], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            report = json.loads((root / "scores.json").read_text())
            self.assertFalse(report["complete"])
            self.assertEqual(report["unmatched_selectors"], ["absent"])
            self.assertIsNone(report["episodes"][0]["labels_changed_since_recording"])

    def test_cli_reports_missing_episodes_and_fails_instead_of_silently_skipping(self):
        with tempfile.TemporaryDirectory() as root:
            root = Path(root)
            labels = root / "labels"
            labels.mkdir()
            (labels / "missing.json").write_text(json.dumps({
                "video_id": "missing", "channel": "show", "duration": 100, "segments": [],
            }))
            result = subprocess.run([
                sys.executable, str(Path(__file__).with_name("score_labels.py")),
                str(root / "renders"), "-labels", str(labels), "--json", str(root / "scores.json"),
            ], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1, result.stderr)
            report = json.loads((root / "scores.json").read_text())
            self.assertFalse(report["complete"])
            self.assertEqual(report["missing"], ["show missing"])
            self.assertEqual(report["episodes"], [])


if __name__ == "__main__":
    unittest.main()
