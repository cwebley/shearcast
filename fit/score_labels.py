"""Score render records against the Opus ground-truth labels in data/opus-labels.

No model calls: reads each labeled video's render.m4a.json and compares its
cut regions with the labels. Usage:

    python3 fit/score_labels.py [RENDERS_DIR] [-v] [-labels DIR]

RENDERS_DIR defaults to the render cache and must hold <channel>/<id>/render.m4a.json.
-labels scores against another label directory, such as data/test-labels.
-v lists every edge off by more than 5s, every block with more than 5s of ad
left between its cuts, and every missed or extra region.

Adjacent labels (gap of 3s or less) merge into one block, because a cut that
spans two back-to-back ads is right, not 60s early. Labels marked borderline
are optional: a block made only of them is not required, and an edge that lands
anywhere inside a borderline stretch at a block's end counts as correct.
Labeling instructions are in fit/label_prompt.md.
"""
import glob, json, os, sys

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


def main():
    argv = sys.argv[1:]
    labels_dir = LABELS
    if "-labels" in argv:
        i = argv.index("-labels")
        labels_dir = argv[i + 1]
        del argv[i:i + 2]
    args = [a for a in argv if a != "-v"]
    verbose = "-v" in argv
    renders = args[0] if args else RENDERS

    starts, ends, missed, extra, notes, required, videos = [], [], [], [], [], 0, 0
    interior = []
    for path in sorted(glob.glob(f"{labels_dir}/*.json")):
        label = json.load(open(path))
        rec = f"{renders}/{label['channel']}/{label['video_id']}/render.m4a.json"
        if not os.path.exists(rec):
            continue
        videos += 1
        regions = json.load(open(rec))["detection"]["Regions"] or []
        name = f"{label['channel']} {label['video_id']}"
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
                extra.append(f"{name} {g['Rule']} {g['Start']:.0f}-{g['End']:.0f}")

    print(f"{videos} labeled videos with render records in {renders}")
    print(f"{required} required blocks: {len(starts)} detected, {len(missed)} missed, {len(extra)} extra regions")
    # A start that is early (negative) cuts content; an end that is late (positive) does.
    print(summary("start", starts, -1))
    print(summary("end", ends, 1))
    print(f"inner ad left >5s between cuts: {len(interior)} ({sum(interior):.0f}s)")
    if verbose:
        for title, rows in (("edges off by more than 5s", notes), ("missed", missed), ("extra", extra)):
            print(f"\n{title}:")
            for r in rows:
                print(f"  {r}")


if __name__ == "__main__":
    main()
