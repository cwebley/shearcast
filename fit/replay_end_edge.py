"""Replay closing-edge rules over data/end-curves.jsonl. No model calls."""
import json, os

CURVES = os.path.join(os.path.dirname(__file__), "..", "data", "end-curves.jsonl")

def changepoint(ys):
    best = None
    for i in range(1, len(ys)):
        a, b = ys[:i], ys[i:]
        ma, mb = sum(a) / len(a), sum(b) / len(b)
        sse = sum((y - ma) ** 2 for y in a) + sum((y - mb) ** 2 for y in b)
        if best is None or sse < best[0]:
            best = (sse, i, mb - ma)
    return best[1], best[2]

def current(pts, window=11):
    ys = [p["p"] for p in pts][:window]
    i, h = changepoint(ys)
    return (pts[i]["start"], f"R{i+1:02d} step {h:.2f}") if h > 0 else (None, "never rises -> anchor end")

def proposed(pts, window=11, min_step=0.3, low=0.3):
    ys = [p["p"] for p in pts]
    for w in range(min(window, len(ys)), len(ys) + 1):
        i, h = changepoint(ys[:w])
        if h >= min_step:
            return pts[i]["start"], f"R{i+1:02d} step {h:.2f} (window {w})"
    if max(ys) < low:
        return pts[-1]["end"], "never returns -> end of last candidate"
    return pts[0]["start"], "ambiguous -> earliest candidate (keep content)"

def err(edge, truth, c):
    if edge is None:
        edge = c.get("anchor_end")
    return None if edge is None else edge - truth

curves = [json.loads(l) for l in open(CURVES)]
print(f"{'video':12} {'rule':11} {'current':>8} {'proposed':>9}  proposed rule")
for c in curves:
    pts, t = c["points"], c["truth"]
    e0, why0 = current(pts)
    e1, why1 = proposed(pts)
    a, b = err(e0, t, c), err(e1, t, c)
    fmt = lambda x: "  anchor?" if x is None else f"{x:+8.1f}"
    flag = "" if a is None or b is None or abs(a - b) < 0.05 else "  <- changed"
    print(f"{c['video']:12} {c['rule']:11} {fmt(a)} {fmt(b)}  {why1}{flag}")
