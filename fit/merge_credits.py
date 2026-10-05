"""Merge the blind Opus credits labels into data/opus-labels.

Reads data/opus-credits/<id>.json (written by agents following
fit/credits_prompt.md). Replaces any credits segments already in the matching
data/opus-labels/<id>.json, tagging the new ones "labeler": "opus-credits".
Before replacing, prints how the old credits segments compare with the new
ones, so hand labels can be checked against the blind ones.

    python3 fit/merge_credits.py          # compare only
    python3 fit/merge_credits.py -write   # compare, then merge
"""
import glob, json, os, sys

HERE = os.path.dirname(__file__)
LABELS = os.path.join(HERE, "..", "data", "opus-labels")
CREDITS = os.path.join(HERE, "..", "data", "opus-credits")
LABELER = "opus-credits"


def overlap(a, b):
    return min(a["end"], b["end"]) - max(a["start"], b["start"])


def main():
    write = "-write" in sys.argv[1:]
    labels = {os.path.basename(p)[:-5]: p for p in glob.glob(f"{LABELS}/*.json")}
    found = sorted(glob.glob(f"{CREDITS}/*.json"))
    missing = sorted(set(labels) - {os.path.basename(p)[:-5] for p in found})
    if missing:
        print(f"no credits file yet for {len(missing)} video(s): {' '.join(missing)}")

    total = 0
    for path in found:
        vid = os.path.basename(path)[:-5]
        # Segments marked "excluded" fall outside the credits definition as it
        # was later narrowed; they stay in the raw file but aren't merged.
        new = [s for s in json.load(open(path))["segments"] if not s.get("excluded")]
        label = json.load(open(labels[vid]))
        old = [s for s in label["segments"] if s["category"] == "credits"]
        total += len(new)
        for s in new:
            match = [o for o in old if overlap(o, s) > 0]
            note = ("matches hand label " + ", ".join(f"{o['start']:.0f}-{o['end']:.0f}" for o in match)
                    if match else "new")
            flag = " borderline" if s.get("borderline") else ""
            print(f"{label['channel']:10s} {vid} {s['start']:7.1f}-{s['end']:7.1f} {s.get('confidence', '')}{flag}  {note}")
        for o in old:
            if not any(overlap(o, s) > 0 for s in new):
                print(f"{label['channel']:10s} {vid} {o['start']:7.1f}-{o['end']:7.1f} hand label only, Opus found nothing")
        if write:
            kept = [s for s in label["segments"] if s["category"] != "credits"]
            for s in new:
                s = dict(s, category="credits", labeler=LABELER)
                s.setdefault("advertiser", None)
                kept.append(s)
            label["segments"] = sorted(kept, key=lambda s: s["start"])
            with open(labels[vid], "w") as f:
                json.dump(label, f, indent=2, ensure_ascii=False)
                f.write("\n")
    print(f"{total} credits segment(s) across {len(found)} video(s)" + (", merged" if write else ""))


if __name__ == "__main__":
    main()
