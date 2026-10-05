"""Replay opening-edge rules over the start curves in cached render records.

No model calls: every region's StartCurve is already stored in its
render.m4a.json. The current rule mirrors fitStart in internal/detect/edge.go. Exits non-zero when a labeled case picks the wrong edge.
"""
import glob, json, os, sys

RENDERS = os.path.expanduser("~/Library/Caches/shearcast/renders")

# (video, anchor start rounded) -> where the advertisement really begins, in
# seconds. None means "at or before the first candidate": keep the anchor edge.
TRUTH = {
    ("2SyX3sudRrY", 1): None,  # Vergecast opens on a Sonos read at 0:00
}

def changepoint(ys):
    best = None
    for i in range(1, len(ys)):
        a, b = ys[:i], ys[i:]
        ma, mb = sum(a) / len(a), sum(b) / len(b)
        sse = sum((y - ma) ** 2 for y in a) + sum((y - mb) ** 2 for y in b)
        if best is None or sse < best[0]:
            best = (sse, i, ma, mb)
    return best[1], best[2], best[3]

def any_rise(ys):
    """The rule before 2026-09-27: accept any rise. Kept for comparison."""
    if len(ys) < 2:
        return 0
    i, lo, hi = changepoint(ys)
    return i if hi > lo else 0

def current(ys):
    """fitStart: a rise counts only if the before side reads as not begun."""
    i = any_rise(ys)
    return i if i and sum(ys[:i]) / i < 0.5 else 0

RULES = {"current": current, "any_rise": any_rise}

def main():
    failures = 0
    rows = 0
    for path in sorted(glob.glob(f"{RENDERS}/*/*/render.m4a.json")):
        rec = json.load(open(path))
        for g in rec["detection"]["Regions"] or []:
            curve = g.get("StartCurve") or []
            if len(curve) < 2:
                continue
            rows += 1
            ys = [c["P"] for c in curve]
            recorded = next((i for i, c in enumerate(curve) if c["Edge"]), 0)
            picks = {name: rule(ys) for name, rule in RULES.items()}
            # Records made before 2026-09-27 were picked by any_rise.
            if recorded not in (picks["current"], picks["any_rise"]):
                print(f"replay mismatch: {rec['video_id']} anchor {g['AnchorStart']:.0f}: "
                      f"recorded {recorded}, replayed {picks['current']}")
                failures += 1
            key = (rec["video_id"], round(g["AnchorStart"]))
            flag = ""
            if key in TRUTH:
                want = TRUTH[key]
                for name, idx in picks.items():
                    got = None if idx == 0 else curve[idx]["Start"]
                    ok = got == want if want is None else got is not None and abs(got - want) < 2
                    flag += f" {name}:{'ok' if ok else 'WRONG'}"
                    failures += name == "current" and not ok
            cells = " ".join(f"{n}={'anchor' if i == 0 else curve[i]['ID']}" for n, i in picks.items())
            print(f"{rec['channel']:10} {rec['video_id']:12} {g['Rule']:9} anchor {g['AnchorStart']:7.1f} "
                  f"pre-mean {sum(ys[:max(picks['current'],1)])/max(picks['current'],1):.2f} {cells}{flag}")
    print(f"{rows} start curves, {failures} failure(s)")
    sys.exit(1 if failures else 0)

main()
