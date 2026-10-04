"""Fit edge weights on the Opus labels and test them held out.

Reads data/<edge>-features.jsonl (written by `go run ./cmd/featuredump -edge
<edge>`): one row per candidate of every detected region in the labeled videos,
with the feature answers. No model calls here.

A sentence is labeled 1 when its midpoint falls inside a required Opus segment,
0 when it falls outside every segment, and is left out when it falls inside a
borderline one.

Evaluation approximates the edge rule on curves scored by weights that never
saw the held-out video or channel. It omits production guards, sentence fallback
and outro trimming; see fit/README.md. It reports the same boundary metrics as
score_labels.py for that edge. The start rule is startEdge in
internal/detect/detect.go (the step is fitted over candidates at most
StartFitSpan before the anchor, with the StartFallbackFloor fallback inside
it). The end rule is fitEnd in internal/detect/edge.go on the recorded "has
the program returned" predicate, then moved later past every sentence the
fitted ad score puts at END_EXTEND_FLOOR or above (see Options.EndWeights). The
end weights take the predicate's answer as one more input. The baseline is
scored on the same candidates, so run-to-run noise doesn't enter the
comparison: the current data/weights.json for the start, and the predicate with
no extension for the end.

    python3 fit/fit_weights.py [-edge end]           # cross-validate, print the comparison
    python3 fit/fit_weights.py [-edge end] -write    # also refit on everything and write the weights
    python3 fit/fit_weights.py [-edge end] -write -out work/candidate.json  # keep shipped weights intact
    python3 fit/fit_weights.py [-edge end] -v        # list every held-out edge off by more than 5s
"""
import glob, json, math, os, sys
from collections import defaultdict

sys.path.insert(0, os.path.dirname(__file__))
from score_labels import LABELS, blocks, off, overlap, summary

HERE = os.path.dirname(__file__)
EDGE = sys.argv[sys.argv.index("-edge") + 1] if "-edge" in sys.argv else "start"
FEATURES = os.path.join(HERE, "..", "data", f"{EDGE}-features.jsonl")
WEIGHTS = os.path.join(HERE, "..", "data", "weights.json" if EDGE == "start" else f"{EDGE}-weights.json")

# Mirrors DefaultOptions in internal/detect/detect.go.
START_FIT_SPAN = 75
START_FALLBACK_FLOOR = 0.5
END_FIT_WINDOW = 11
END_MIN_STEP = 0.3
END_NEVER_RETURNS = 0.3
END_EXTEND_FLOOR = 0.6
PREDICATE_FEATURE = "predicate_returned"
L2 = 1.0
# Content sentences count 3x on the start edge; on the end the extension
# rule already only moves an edge later, and 1x did best there.
CONTENT_WEIGHT = 3.0 if EDGE == "start" else 1.0


def load_labels():
    out = {}
    for path in glob.glob(f"{LABELS}/*.json"):
        label = json.load(open(path))
        out[label["video_id"]] = label
    return out


def sentence_label(row, segments):
    mid = (row["start"] + row["end"]) / 2
    for s in segments:
        if s["start"] <= mid < s["end"]:
            return None if s["borderline"] else 1
    return 0


def sigmoid(z):
    return 1 / (1 + math.exp(-max(-30, min(30, z))))


def solve(a, b):
    """Gaussian elimination with partial pivoting; a is square, b a vector."""
    n = len(b)
    m = [row[:] + [b[i]] for i, row in enumerate(a)]
    for c in range(n):
        p = max(range(c, n), key=lambda r: abs(m[r][c]))
        m[c], m[p] = m[p], m[c]
        for r in range(n):
            if r != c and m[r][c]:
                f = m[r][c] / m[c][c]
                for k in range(c, n + 1):
                    m[r][k] -= f * m[c][k]
    return [m[i][n] / m[i][i] for i in range(n)]


def fit(rows, feats, l2=L2, iters=25, content_weight=None):
    """L2 logistic regression by Newton's method. The bias is not penalized.

    content_weight scales the loss on content sentences. Cutting content is the
    worse error, so above 1 it trades ad left in for content kept."""
    if content_weight is None:
        content_weight = CONTENT_WEIGHT
    d = len(feats) + 1  # last column is the bias
    X = [[r["values"][f] for f in feats] + [1.0] for r in rows]
    y = [r["label"] for r in rows]
    w = [0.0] * d
    for _ in range(iters):
        g = [l2 * w[j] if j < d - 1 else 0.0 for j in range(d)]
        H = [[(l2 if i == j and i < d - 1 else 0.0) for j in range(d)] for i in range(d)]
        for xi, yi in zip(X, y):
            p = sigmoid(sum(a * b for a, b in zip(w, xi)))
            cw = 1.0 if yi else content_weight
            e, s = cw * (p - yi), cw * p * (1 - p)
            for i in range(d):
                g[i] += e * xi[i]
                si = s * xi[i]
                if si:
                    Hi = H[i]
                    for j in range(d):
                        Hi[j] += si * xi[j]
        step = solve(H, g)
        w = [a - b for a, b in zip(w, step)]
        if max(abs(v) for v in step) < 1e-6:
            break
    return {"bias": w[-1], "weights": dict(zip(feats, w[:-1]))}


def score(model, values):
    return sigmoid(model["bias"] + sum(c * values.get(f, 0.0) for f, c in model["weights"].items()))


def changepoint(ys):
    best, best_sse = 1, None
    for i in range(1, len(ys)):
        a, b = ys[:i], ys[i:]
        ma, mb = sum(a) / len(a), sum(b) / len(b)
        sse = sum((v - ma) ** 2 for v in a) + sum((v - mb) ** 2 for v in b)
        if best_sse is None or sse < best_sse:
            best, best_sse = i, sse
    return best


def fit_start(ys):
    if len(ys) < 2:
        return 0
    i = changepoint(ys)
    lo, hi = sum(ys[:i]) / i, sum(ys[i:]) / (len(ys) - i)
    if hi <= lo or lo >= 0.5:
        return 0
    return i


def start_edge(curve, cands, anchor):
    """Mirrors Detector.startEdge. Returns the chosen start time."""
    lo = 0
    while lo < len(cands) and cands[lo]["start"] < anchor - START_FIT_SPAN:
        lo += 1
    idx = fit_start(curve[lo:])
    if idx > 0:
        return cands[lo + idx]["start"]
    for y, c in zip(curve, cands):
        if c["start"] >= anchor and y >= START_FALLBACK_FLOOR:
            return c["start"]
    return anchor


def fit_end(curve, window=END_FIT_WINDOW, min_step=END_MIN_STEP, never=END_NEVER_RETURNS):
    """Mirrors fitEnd. curve is "has the program returned"; returns an index,
    len(curve) meaning it never does, or None when the curve is ambiguous."""
    if len(curve) < 2:
        return 0
    first = min(window, len(curve)) if window > 1 else len(curve)
    for w in range(first, len(curve) + 1):
        ys = curve[:w]
        i = changepoint(ys)
        if sum(ys[i:]) / (w - i) - sum(ys[:i]) / i >= min_step:
            return i
    return len(curve) if max(curve) < never else None


def end_edge(cands, ad=None):
    """The predicate's end, extended past sentences scored as ad when ad is given."""
    idx = fit_end([c["predicate"] for c in cands])
    if idx is None:
        # Ambiguous: walk from the first ad-scored sentence after the region's start, or keep the
        # anchor's end without one (see bound). The rows don't record the
        # region's start, so the anchor's start stands in for it.
        first = next((i for i, c in enumerate(cands) if ad is not None
                      and cands[0]["anchor_start"] <= c["start"] < cands[0]["anchor_end"]
                      and ad[i] >= END_EXTEND_FLOOR), None)
        if first is None:
            first = 0
            while first < len(cands) and cands[first]["start"] < cands[0]["anchor_end"]:
                first += 1
        idx = first
    if ad is not None:
        while idx < len(cands) and ad[idx] >= END_EXTEND_FLOOR:
            idx += 1
    return max(cands[-1]["end"], cands[0]["anchor_end"]) if idx >= len(cands) else cands[idx]["start"]


def region_block(region, truth):
    """The required block this region's anchor overlaps most, if any."""
    a = (region[0]["anchor_start"], region[0]["anchor_end"])
    best = max(truth, key=lambda b: overlap(a, (b["start"], b["end"])), default=None)
    if best and best["required"] and overlap(a, (best["start"], best["end"])) > 0:
        return best
    return None


def evaluate(regions, labels, scorer):
    errs, notes = [], []
    for key, cands in regions.items():
        video = key[1]
        b = region_block(cands, blocks(labels[video]["segments"]))
        if not b:
            continue
        curve = [scorer(key, c) for c in cands] if scorer else None
        if EDGE == "start":
            e = off(start_edge(curve, cands, cands[0]["anchor_start"]), b["start_ok"])
        else:
            e = off(end_edge(cands, curve), b["end_ok"])
        errs.append(e)
        if abs(e) > 5:
            truth = b["start"] if EDGE == "start" else b["end"]
            notes.append(f"{key[0]} {video} truth {truth:.0f}: {EDGE} {e:+.1f}s")
    return errs, notes


def main():
    args = sys.argv[1:]
    output = WEIGHTS
    if "-out" in args:
        i = args.index("-out")
        if "-write" not in args or i + 1 >= len(args) or args[i + 1].startswith("-"):
            raise SystemExit("-out requires -write and an output path")
        output = args[i + 1]
    labels = load_labels()
    rows = [json.loads(l) for l in open(FEATURES)]

    if EDGE == "end":
        for r in rows:
            r["values"][PREDICATE_FEATURE] = r["predicate"]
    feats = sorted(rows[0]["values"])

    regions = defaultdict(list)
    for r in rows:
        regions[(r["channel"], r["video"], r["anchor_start"])].append(r)
        r["label"] = sentence_label(r, labels[r["video"]]["segments"])
    train = [r for r in rows if r["label"] is not None]
    pos = sum(r["label"] for r in train)
    print(f"{len(rows)} candidate rows, {len(train)} labeled ({pos} ad, {len(train) - pos} content), "
          f"{len(regions)} regions, {len({r['video'] for r in rows})} videos, "
          f"{len({r['channel'] for r in rows})} channels")

    current = json.load(open(WEIGHTS)) if os.path.exists(WEIGHTS) else {"bias": 0.0, "weights": {}}
    def held_out(group):
        models = {}
        for g in sorted({group(r) for r in rows}):
            models[g] = fit([r for r in train if group(r) != g], feats)
        return lambda key, c: score(models[group(c)], c["values"])

    scorers = {}
    if EDGE == "end":
        scorers["predicate alone"] = None
    if current["weights"]:
        scorers[f"current {os.path.basename(WEIGHTS)}"] = lambda key, c: score(current, c["values"])
    scorers["refit, video held out"] = held_out(lambda r: r["video"])
    scorers["refit, channel held out"] = held_out(lambda r: r["channel"])
    for name, scorer in scorers.items():
        errs, notes = evaluate(regions, labels, scorer)
        print(f"\n{name}")
        print("  " + summary(EDGE, errs, -1 if EDGE == "start" else 1))
        if "-v" in args:
            for n in notes:
                print(f"    {n}")

    model = fit(train, feats)
    print("\nrefit on everything:")
    for f, c in sorted(model["weights"].items(), key=lambda kv: -abs(kv[1])):
        print(f"  {f:20s} {c:+.3f}   (was {current['weights'].get(f, 0):+.3f})")
    print(f"  {'bias':20s} {model['bias']:+.3f}   (was {current['bias']:+.3f})")

    if "-write" in args:
        out = {"edge": EDGE, "bias": round(model["bias"], 4),
               "trained_on": sorted({r["channel"] for r in train}),
               "videos": len({r["video"] for r in train}),
               "rows": len(train),
               "weights": {f: round(c, 4) for f, c in sorted(model["weights"].items())}}
        with open(output, "w") as f:
            f.write(json.dumps(out, indent=2) + "\n")
        print(f"\nwrote {output}")


if __name__ == "__main__":
    main()
