"""Check agent labels' timestamps against the text they quote.

Agents convert caption timestamps by hand and sometimes slip by whole minutes
(two of 34 credits segments on 2026-09-28). For every segment with a
first_line, this finds that text in the transcript and prints how far the
labeled start is from it. Needs the videos' captions in the cache.

    python3 fit/check_label_times.py data/test-labels
"""
import glob, json, re, subprocess, sys


def secs(t):
    s = 0
    for x in t.split(":"):
        s = s * 60 + int(x)
    return s


def words(s):
    return re.sub(r"[^a-z0-9 ]", "", (s or "").lower()).split()


def main():
    bad = 0
    for path in sorted(glob.glob(f"{sys.argv[1]}/*.json")):
        d = json.load(open(path))
        segs = [s for s in d["segments"] if s.get("first_line")]
        if not segs:
            continue
        out = subprocess.run(["./shearcast", "transcript", "https://www.youtube.com/watch?v=" + d["video_id"]],
                             capture_output=True, text=True).stdout
        lines = [(secs(m.group(1)), " ".join(words(m.group(2))))
                 for m in re.finditer(r"^\s*(\d+:\d\d(?::\d\d)?)\s+(.*)$", out, re.M)]
        for s in segs:
            key = " ".join(words(s["first_line"])[:6])
            hits = [t for t, txt in lines if key and key in txt]
            if not hits:
                print(f"{d['video_id']} {s['start']:7.1f} no match for {s['first_line'][:60]!r}")
                bad += 1
                continue
            at = min(hits, key=lambda t: abs(t - s["start"]))
            if abs(at - s["start"]) > 15:
                print(f"{d['video_id']} {s['category']} {s['start']:7.1f}-{s['end']:7.1f}: text is at {at}, off by {at - s['start']:+.0f}s")
                bad += 1
    print(f"{bad} segment(s) to check")


if __name__ == "__main__":
    main()
