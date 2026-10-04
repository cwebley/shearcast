"""Score whole episodes and boundary diagnostics against development labels.

No model calls: reads each labeled video's render.m4a.json and compares its
cut regions with the labels. Usage:

    python3 fit/score_labels.py [RENDERS_DIR] [-v] [-labels DIR] [--json PATH]

RENDERS_DIR defaults to the render cache and must hold <channel>/<id>/render.m4a.json.
-labels scores against another label directory, such as data/test-labels.
-v lists every edge off by more than 5s, every block with more than 5s of ad
left between its cuts, and every missed or extra region.

Interval metrics count all errors and report regions, merged detector cuts, and
rendered keep ranges separately. Missing records fail unless --allow-missing is
explicit. Use --video to select the same episodes as a subset redetect run.

For legacy edge diagnostics only, adjacent labels (gap of 3s or less) merge into one block, because a cut that
spans two back-to-back ads is right, not 60s early. Labels marked borderline
are optional: a block made only of them is not required, and an edge that lands
anywhere inside a borderline stretch at a block's end counts as correct.
Labeling instructions are in fit/label_prompt.md.
"""
import argparse
import glob
import hashlib
import json
import math
import os
import sys

LABELS = os.path.join(os.path.dirname(__file__), "..", "data", "opus-labels")
RENDERS = os.path.expanduser("~/Library/Caches/shearcast/renders")


def overlap(a, b):
    return min(a[1], b[1]) - max(a[0], b[0])


def blocks(segments, gap=3):
    out = []
    for s in sorted(segments, key=lambda s: s["start"]):
        if out and s["start"] - out[-1]["end"] <= gap:
            out[-1]["end"] = max(out[-1]["end"], s["end"])
            out[-1]["parts"].append(s)
        else:
            out.append({"start": s["start"], "end": s["end"], "parts": [s]})
    for b in out:
        hard = [p for p in b["parts"] if not p["borderline"]]
        b["required"] = bool(hard)
        # Any start from the block's start to its first required label is right,
        # and likewise any end from its last required label to the block's end.
        b["start_ok"] = (b["start"], min(p["start"] for p in hard) if hard else b["start"])
        b["end_ok"] = (max(p["end"] for p in hard) if hard else b["end"], b["end"])
    return out


def off(x, ok):
    """Signed distance from x to the acceptable range, zero inside it."""
    lo, hi = ok
    return x - lo if x < lo else x - hi if x > hi else 0.0


def interior_left(block, hits):
    """Seconds of required (non-borderline) label left uncut between the first
    and last cut overlapping the block. Edge scores only see the outermost
    cuts, so fragmented cuts would otherwise hide ads left between them."""
    lo, hi = min(g["Start"] for g in hits), max(g["End"] for g in hits)
    covered = sorted((max(g["Start"], lo), min(g["End"], hi)) for g in hits)
    gaps, at = [], lo
    for s, e in covered:
        if s > at:
            gaps.append((at, s))
        at = max(at, e)
    hard = [(p["start"], p["end"]) for p in block["parts"] if not p["borderline"]]
    return sum(max(0, overlap(g, h)) for g in gaps for h in hard)


def summary(name, errs, cut_sign):
    if not errs:
        return f"{name:5} n=0"
    ab = sorted(abs(e) for e in errs)
    cut = [abs(e) for e in errs if e * cut_sign > 5]
    left = [abs(e) for e in errs if e * cut_sign < -5]
    return (f"{name:5} n={len(ab)} median {ab[len(ab) // 2]:.1f}s within5s {sum(x <= 5 for x in ab)}/{len(ab)}"
            f"  content cut >5s: {len(cut)} ({sum(cut):.0f}s)  ad left >5s: {len(left)} ({sum(left):.0f}s)")


def union(intervals, duration):
    """Clamp and union intervals without filling genuine gaps between them."""
    ordered = []
    for start, end in intervals:
        if not math.isfinite(start) or not math.isfinite(end) or end < start:
            raise ValueError(f"invalid interval {start}-{end}")
        start, end = max(0, start), min(duration, end)
        if end > start:
            ordered.append((start, end))
    out = []
    for start, end in sorted(ordered):
        if out and start <= out[-1][1]:
            out[-1] = (out[-1][0], max(out[-1][1], end))
        else:
            out.append((start, end))
    return out


def difference(left, right):
    """Subtract two already-unioned interval sets."""
    out = []
    for start, end in left:
        at = start
        for lo, hi in right:
            if hi <= at:
                continue
            if lo >= end:
                break
            if lo > at:
                out.append((at, lo))
            at = max(at, hi)
            if at >= end:
                break
        if at < end:
            out.append((at, end))
    return out


def seconds(intervals):
    return sum(end - start for start, end in intervals)


def score_episode(label, record):
    """Score raw regions, detector cuts and rendered cuts independently.

    Required labels must be removed; borderline labels may be kept or removed.
    Everything outside their union is content. Every second counts, including
    entirely missed blocks, short errors and content between neighboring ads.
    """
    if not isinstance(record, dict):
        raise ValueError("record must be an object")
    duration = label["duration"]
    if not math.isfinite(duration) or duration <= 0:
        raise ValueError("label needs a positive finite duration")
    required = union([(s["start"], s["end"]) for s in label["segments"] if not s["borderline"]], duration)
    allowed = union([(s["start"], s["end"]) for s in label["segments"]], duration)
    detection = record.get("detection")
    if not isinstance(detection, dict) or "Regions" not in detection:
        raise ValueError("record has no detection regions")

    def ranges(rows):
        if rows is None:
            return []
        if not isinstance(rows, list) or any(not isinstance(s, dict) for s in rows):
            raise ValueError("interval collections must be arrays or null")
        return union([(s["Start"], s["End"]) for s in rows], duration)

    cuts = {"regions": ranges(detection["Regions"])}
    if "Segments" in detection:
        cuts["detector"] = ranges(detection["Segments"])
    if record.get("keep") is not None:
        cuts["rendered"] = difference([(0, duration)], ranges(record["keep"]))

    out = {}
    for view, removed in cuts.items():
        content_lost = difference(removed, allowed)
        unwanted_left = difference(required, removed)
        missed = partial = 0
        for block in required:
            left = seconds(difference([block], removed))
            if left == block[1] - block[0]:
                missed += 1
            elif left > 0:
                partial += 1
        out[view] = {
            "duration_seconds": duration,
            "content_seconds": duration - seconds(allowed),
            "required_seconds": seconds(required),
            "content_removed_seconds": seconds(content_lost),
            "required_retained_seconds": seconds(unwanted_left),
            "worst_content_cut_seconds": max((end - start for start, end in content_lost), default=0),
            "content_loss_intervals": content_lost,
            "required_retained_intervals": unwanted_left,
            "required_blocks": len(required),
            "missed_blocks": missed,
            "partially_cut_blocks": partial,
            "fully_cut_blocks": len(required) - missed - partial,
        }
    return out


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("renders", nargs="?", default=RENDERS)
    parser.add_argument("-labels", "--labels", default=LABELS)
    parser.add_argument("-v", action="store_true")
    parser.add_argument("--video", default="", help="comma-separated video IDs or channel slugs")
    parser.add_argument("--allow-missing", action="store_true", help="report missing episodes without failing")
    parser.add_argument("--json", help="write per-episode metrics, input hashes and coverage")
    parser.add_argument("--large-error-seconds", type=float, default=5, help="content-loss reporting threshold, never a scoring tolerance")
    args = parser.parse_args(argv)
    if not math.isfinite(args.large_error_seconds) or args.large_error_seconds < 0:
        parser.error("--large-error-seconds must be finite and nonnegative")
    selected = set(args.video.split(",")) if args.video else None
    matched = set()

    starts, ends, missed, extra, notes, required = [], [], [], [], [], 0
    interior = []
    episodes, missing, errors = [], [], []
    count = 0
    for path in sorted(glob.glob(os.path.join(args.labels, "*.json"))):
        try:
            with open(path, "rb") as f:
                label_data = f.read()
            label = json.loads(label_data)
            if selected and label["video_id"] not in selected and label["channel"] not in selected:
                continue
            if selected:
                matched.update(selected.intersection((label["video_id"], label["channel"])))
            count += 1
            name = f"{label['channel']} {label['video_id']}"
            rec = os.path.join(args.renders, label["channel"], label["video_id"], "render.m4a.json")
            if not os.path.exists(rec):
                missing.append(name)
                continue
            with open(rec, "rb") as f:
                record_data = f.read()
            record = json.loads(record_data)
            views = score_episode(label, record)
            if record.get("video_id", label["video_id"]) != label["video_id"] or record.get("channel", label["channel"]) != label["channel"]:
                raise ValueError("record identity disagrees with label")
            label_sha = hashlib.sha256(label_data).hexdigest()
            episodes.append({
                "video_id": label["video_id"], "channel": label["channel"],
                "label_sha256": label_sha,
                "record_sha256": hashlib.sha256(record_data).hexdigest(),
                "labels_changed_since_recording": record["label_sha256"] != label_sha if record.get("label_sha256") else None,
                "views": views,
            })
        except (OSError, ValueError, KeyError, TypeError) as error:
            errors.append(f"{path}: {error}")
            continue
        regions = record["detection"]["Regions"] or []
        truth = blocks(label["segments"])
        for b in truth:
            if not b["required"]:
                continue
            required += 1
            hits = [g for g in regions if overlap((g["Start"], g["End"]), (b["start"], b["end"])) > 0]
            if not hits:
                missed.append(f"{name} {b['start']:.0f}-{b['end']:.0f} ({b['end'] - b['start']:.0f}s)")
                continue
            s = off(min(g["Start"] for g in hits), b["start_ok"])
            e = off(max(g["End"] for g in hits), b["end_ok"])
            starts.append(s)
            ends.append(e)
            if abs(s) > 5 or abs(e) > 5:
                notes.append(f"{name} truth {b['start']:.0f}-{b['end']:.0f}: start {s:+.1f}s, end {e:+.1f}s")
            gap = interior_left(b, hits)
            if gap > 5:
                interior.append(gap)
                notes.append(f"{name} truth {b['start']:.0f}-{b['end']:.0f}: {gap:.0f}s of ad left between cuts")
        for g in regions:
            if not any(overlap((g["Start"], g["End"]), (b["start"], b["end"])) > 0 for b in truth):
                extra.append(f"{name} {g.get('Rule', 'unknown')} {g['Start']:.0f}-{g['End']:.0f}")

    unmatched = sorted(selected - matched) if selected else []
    if unmatched:
        errors.append(f"selectors match no labels: {', '.join(unmatched)}")
    print(f"{len(episodes)}/{count} selected labeled videos scored in {args.renders}; {len(missing)} missing, {len(errors)} invalid")
    totals = {}
    for view in ("regions", "detector", "rendered"):
        rows = [e["views"][view] for e in episodes if view in e["views"]]
        if not rows:
            print(f"{view}: unavailable")
            continue
        total = {key: sum(row[key] for row in rows) for key in (
            "duration_seconds", "content_seconds", "required_seconds", "content_removed_seconds",
            "required_retained_seconds", "required_blocks", "missed_blocks", "partially_cut_blocks", "fully_cut_blocks",
        )}
        total["episodes"] = len(rows)
        total["worst_content_cut_seconds"] = max(row["worst_content_cut_seconds"] for row in rows)
        total["episodes_with_content_loss"] = sum(row["content_removed_seconds"] > 0 for row in rows)
        total["episodes_with_large_content_cut"] = sum(row["worst_content_cut_seconds"] > args.large_error_seconds for row in rows)
        totals[view] = total
        print(f"{view}: {len(rows)} episodes; content removed {total['content_removed_seconds']:.2f}s; "
              f"required unwanted retained {total['required_retained_seconds']:.2f}s; "
              f"worst content cut {total['worst_content_cut_seconds']:.2f}s; "
              f"episodes with a content cut >{args.large_error_seconds:g}s: {total['episodes_with_large_content_cut']}; "
              f"entirely missed blocks {total['missed_blocks']}/{total['required_blocks']}")
    print("\nLegacy edge diagnostics, raw regions, >5s reporting threshold:")
    print(f"{required} required blocks: {len(starts)} detected, {len(missed)} missed, {len(extra)} extra regions")
    # A start that is early (negative) cuts content; an end that is late (positive) does.
    print(summary("start", starts, -1))
    print(summary("end", ends, 1))
    print(f"inner ad left >5s between cuts: {len(interior)} ({sum(interior):.0f}s)")
    if args.v:
        for episode in episodes:
            for view, metrics in episode["views"].items():
                if metrics["content_removed_seconds"] or metrics["required_retained_seconds"]:
                    print(f"  {episode['channel']} {episode['video_id']} {view}: "
                          f"content removed {metrics['content_removed_seconds']:.2f}s {metrics['content_loss_intervals']}; "
                          f"required retained {metrics['required_retained_seconds']:.2f}s {metrics['required_retained_intervals']}")
        for title, rows in (("edges off by more than 5s", notes), ("missed", missed), ("extra", extra)):
            print(f"\n{title}:")
            for r in rows:
                print(f"  {r}")
    for name in missing:
        print(f"missing: {name}")
    for error in errors:
        print(f"invalid: {error}", file=sys.stderr)
    if count == 0:
        errors.append("no labeled episodes selected")
        print(errors[-1], file=sys.stderr)
    report = {
        "complete": not missing and not errors,
        "selected_episodes": count,
        "channels": sorted({e["channel"] for e in episodes}),
        "large_error_seconds": args.large_error_seconds,
        "unmatched_selectors": unmatched,
        "missing": missing, "errors": errors, "totals": totals, "episodes": episodes,
    }
    if args.json:
        with open(args.json, "w") as f:
            json.dump(report, f, indent=2, allow_nan=False)
            f.write("\n")
    return 1 if errors or (missing and not args.allow_missing) else 0


if __name__ == "__main__":
    sys.exit(main())
