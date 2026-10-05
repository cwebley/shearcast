"""Collect closing-edge curves with a labeled true end into data/end-curves.jsonl.

Sources: the jev-test 12-video feature set (SponsorBlock truth), the doorbell
baseline archive, and Space Time render records (truth from SponsorBlock,
cross-checked by a blind Opus read on 2026-09-24).
"""
import glob, json, os, sys

OUT = os.path.join(os.path.dirname(__file__), "..", "data", "end-curves.jsonl")
JEV = os.path.expanduser("~/src/jev-test/data/features.jsonl")
DOORBELL = sys.argv[1] if len(sys.argv) > 1 else None  # extracted render.m4a.json
RENDERS = os.path.expanduser("~/Library/Caches/shearcast/renders/spacetime")

# (video, rule) -> true end in seconds, where program content resumes.
TRUTH = {
    ("qgLaCZyKv_8", "sponsor"): 1037.5,     # R016, matches the Outro chapter at 1038
    ("I07RBedXRYA", "sponsor"): 1084.3,     # ad runs to the last caption
    ("I07RBedXRYA", "selfpromo"): 116.0,
    ("eYViePMalJE", "selfpromo"): 119.0,    # inside the 1:54 caption; next caption 2:03
    ("Yid9cO7peXg", "selfpromo"): 79.4,
    ("YSwRqNCeP9k", "selfpromo"): 7682.0,   # SponsorBlock is 25s off here; Opus read used
}

out = []
by = {}
for l in open(JEV):
    r = json.loads(l)
    if r["edge"] == "end" and r["truth"] >= 0:
        by.setdefault((r["video"], r["rule"]), []).append(r)
for (v, rule), rs in by.items():
    rs.sort(key=lambda r: r["start"])
    out.append({"video": v, "rule": rule, "source": "jev-test", "truth": rs[0]["truth"],
                "points": [{"id": r["id"], "start": r["start"], "end": r["end"], "p": r["predicate"]} for r in rs]})

records = glob.glob(f"{RENDERS}/*/render.m4a.json") + ([DOORBELL] if DOORBELL else [])
for path in records:
    rec = json.load(open(path))
    for g in rec["detection"]["Regions"] or []:
        key = (rec["video_id"], g["Rule"])
        if key not in TRUTH or not g["EndCurve"]:
            continue
        out.append({"video": key[0], "rule": key[1], "source": os.path.basename(os.path.dirname(os.path.dirname(path))),
                    "truth": TRUTH[key], "anchor_end": g["AnchorEnd"],
                    "points": [{"id": p["ID"], "start": p["Start"], "end": p["End"], "p": p["P"]} for p in g["EndCurve"]]})

with open(OUT, "w") as f:
    for c in out:
        f.write(json.dumps(c) + "\n")
print(f"{len(out)} curves -> {os.path.normpath(OUT)}")
