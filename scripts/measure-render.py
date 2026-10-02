#!/usr/bin/env python3
"""Sample the production renderer's process tree and working disk on macOS/Linux.

Uses only Python's standard library, ps, ffmpeg and a built renderbench binary.
No network requests or model calls. Results are JSON Lines, including failures.
"""

import argparse
import json
import os
from pathlib import Path
import platform
import signal
import subprocess
import tempfile
import time


def tree_rss(pid):
    rows = subprocess.check_output(
        ["ps", "-axo", "pid=,ppid=,rss="], text=True
    )
    processes = [tuple(map(int, line.split())) for line in rows.splitlines()]
    descendants = {pid}
    while True:
        children = {p for p, parent, _ in processes if parent in descendants}
        expanded = descendants | children
        if expanded == descendants:
            break
        descendants = expanded
    return sum(rss * 1024 for p, _, rss in processes if p in descendants)


def disk_bytes(directory):
    # Logical file bytes, not allocated blocks or filesystem cache.
    total = 0
    for root, _, files in os.walk(directory):
        for name in files:
            try:
                total += (Path(root) / name).stat().st_size
            except FileNotFoundError:
                pass
    return total


def ffmpeg(*args):
    subprocess.run(["ffmpeg", "-nostdin", "-v", "error", *map(str, args)], check=True)


def measure(args, source, work, pattern, bitrate):
    with tempfile.TemporaryDirectory(prefix="case-", dir=work) as case:
        # Source is read-only and can be outside work. Count it exactly once.
        source_bytes = source.stat().st_size
        command = [str(args.binary), "-input", str(source), "-out-dir", case,
                   "-pattern", pattern, "-bitrate", str(bitrate),
                   "-repeat", str(args.repeat)]
        peak_rss, peak_disk, samples = 0, source_bytes, 0
        abort_reason = None
        started = time.monotonic()
        # Files avoid blocked pipes if a child emits a lot of diagnostic output.
        with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
            proc = subprocess.Popen(command, stdout=stdout, stderr=stderr, start_new_session=True)
            try:
                while proc.poll() is None:
                    rss = tree_rss(proc.pid)
                    peak_rss = max(peak_rss, rss)
                    peak_disk = max(peak_disk, source_bytes + disk_bytes(case))
                    samples += 1
                    if rss > args.max_rss_mib * 1024**2:
                        abort_reason = "sampled process-tree RSS exceeded measurement ceiling"
                        break
                    if time.monotonic() - started > args.timeout:
                        abort_reason = "measurement timeout"
                        break
                    time.sleep(args.interval)
            finally:
                # Clean descendants even if the driver exited first.
                try:
                    os.killpg(proc.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                proc.wait()
            wall = time.monotonic() - started
            peak_disk = max(peak_disk, source_bytes + disk_bytes(case))
            stdout.seek(0)
            stderr.seek(0)
            episodes = [json.loads(line) for line in stdout.read().decode().splitlines()]
            error = stderr.read().decode(errors="replace")[-4000:]
        return {
            "type": "measurement", "pattern": pattern, "bitrate_kbps": bitrate,
            "repeat": args.repeat, "source_bytes": source_bytes,
            "wall_seconds": wall, "peak_tree_rss_bytes_sampled": peak_rss,
            "peak_working_bytes_sampled": peak_disk, "samples": samples,
            "exit_code": proc.returncode, "abort_reason": abort_reason,
            "error": error or None, "episodes": episodes,
        }


def main():
    # Let the measurement's finally block stop the process group on SIGTERM too.
    def interrupted(signum, frame):
        raise KeyboardInterrupt

    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--work-dir", type=Path, required=True, help="existing scratch directory")
    parser.add_argument("--report", type=Path, required=True, help="new JSONL file; never overwritten")
    parser.add_argument("--input", type=Path, help="existing local media instead of synthetic fixtures")
    parser.add_argument("--durations", default="1800,5400,10800", help="synthetic source lengths in seconds")
    parser.add_argument("--patterns", default="none,typical,heavy")
    parser.add_argument("--bitrates", default="128")
    parser.add_argument("--repeat", type=int, default=1)
    parser.add_argument("--interval", type=float, default=0.05)
    parser.add_argument("--max-rss-mib", type=int, default=8192, help="abort ceiling, not an OS memory limit")
    parser.add_argument("--timeout", type=float, default=900, help="seconds per measurement")
    args = parser.parse_args()
    args.binary = args.binary.resolve(strict=True)
    if not args.work_dir.is_dir(): parser.error("--work-dir must exist")
    if args.repeat < 1 or args.interval <= 0 or args.timeout <= 0 or args.max_rss_mib <= 0:
        parser.error("repeat, interval, timeout and RSS ceiling must be positive")
    durations = [int(s) for s in args.durations.split(",")]
    bitrates = [int(s) for s in args.bitrates.split(",")]
    patterns = args.patterns.split(",")
    if any(d <= 0 for d in durations): parser.error("durations must be positive")
    if any(b < 32 or b > 320 for b in bitrates): parser.error("bitrates must be 32 through 320")
    if any(p not in ("none", "typical", "heavy") for p in patterns): parser.error("unknown pattern")
    source = args.input.resolve(strict=True) if args.input else None
    failed = False
    with args.report.open("x") as report:
        def emit(row):
            line = json.dumps(row)
            report.write(line + "\n")
            report.flush()
            print(line, flush=True)

        emit({"type": "environment", "platform": platform.platform(),
              "machine": platform.machine(), "logical_cpus": os.cpu_count(),
              "ffmpeg": subprocess.check_output(["ffmpeg", "-version"], text=True).splitlines()[0],
              "sample_interval_seconds": args.interval,
              "fixture": "local input" if source else "60s seeded independent stereo pink noise, 48kHz PCM looped then encoded once to AAC 192k",
              "memory_method": "sum RSS of renderbench and descendants; sampled, shared pages may be counted twice",
              "disk_method": "source plus outputs and renderer scratch, logical bytes; excludes seed and report",
              "max_rss_mib": args.max_rss_mib})
        with tempfile.TemporaryDirectory(prefix="render-measure-", dir=args.work_dir) as work:
            seed = Path(work) / "seed.wav"
            if source is None:
                ffmpeg("-f", "lavfi", "-i",
                       "anoisesrc=color=pink:r=48000:d=60:seed=42[l];anoisesrc=color=pink:r=48000:d=60:seed=43[r];[l][r]amerge=inputs=2",
                       "-c:a", "pcm_s16le", seed)
            for duration in ([None] if source else durations):
                fixture = source or Path(work) / "fixture.m4a"
                if source is None:
                    ffmpeg("-stream_loop", "-1", "-i", seed, "-t", duration,
                           "-c:a", "aac", "-b:a", "192k", fixture)
                for bitrate in bitrates:
                    for pattern in patterns:
                        row = measure(args, fixture, work, pattern, bitrate)
                        row["requested_source_seconds"] = duration
                        emit(row)
                        failed |= row["exit_code"] != 0
                if source is None:
                    fixture.unlink()
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
