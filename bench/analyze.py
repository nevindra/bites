#!/usr/bin/env python3
"""Print the tables of a bench run: mean [min-max] over the rounds of every metric.

usage: analyze.py [outdir]   (default: out/ beside this script)

A margin between two servers that lies inside the spread of the run is "unchanged", not a win.
Idle memory is the marginal cost per connection from n1 to n2 connections (RSS, and heap plus
stack from the server's runtime/metrics after two collections), not the average over the first n1.
"""
import glob, json, os, re, statistics as st, sys

OUT = sys.argv[1] if len(sys.argv) > 1 else os.path.join(os.path.dirname(os.path.abspath(__file__)), "out")
ORDER = ["bites", "nethttp", "chi", "gin", "echo", "fiber", "hertz", "huma", "nilo", "fib", "gws", "gorilla", "coder"]


def load(exp, s):
    runs = []
    for f in sorted(glob.glob(f"{OUT}/{exp}-{s}-[0-9]*.json")):
        try:
            d = json.load(open(f))
        except Exception:
            continue
        if "error" not in d:
            runs.append(d)
    return runs


def rng(v, fmt="{:.0f}"):
    v = [x for x in v if x is not None]
    if not v:
        return "n/a"
    return f"{fmt.format(st.mean(v))} [{fmt.format(min(v))}-{fmt.format(max(v))}]"


def hs(m):  # heap objects plus goroutine stacks, from runtime/metrics
    return m["heap_objects"] + m["stacks"]


def servers(exp):
    found = {re.match(rf"{exp}-(.+)-\d+\.json", os.path.basename(f)).group(1) for f in glob.glob(f"{OUT}/{exp}-*.json")}
    return [s for s in ORDER if s in found] + sorted(found - set(ORDER))


def table(title, head, rows):
    if not rows:
        return
    print(f"\n## {title}")
    print("| " + " | ".join(head) + " |")
    print("|" + "---|" * len(head))
    for r in rows:
        print("| " + " | ".join(str(x) for x in r) + " |")


try:
    v = json.load(open(f"{OUT}/versions.json"))
    print("run:", {k: v[k] for k in ("date", "nilo_go_commit", "nilo_commit", "go", "huma", "fib", "rounds", "conns", "dur") if k in v})
except Exception:
    pass

rows = []
for s in servers("allocs"):
    R = load("allocs", s)
    rows.append([s, len(R), rng([r["allocs_per_req"] for r in R], "{:.2f}"), rng([r["bytes_per_req"] for r in R])])
table("allocations per request (POST /users/{id}, one connection, server mallocs delta)", ["server", "runs", "allocs/req", "bytes/req"], rows)

for exp, title in (("get", "GET /users/{id}"), ("post", "POST /users/{id}?page&expand with JSON body")):
    rows = []
    for s in servers(exp):
        R = load(exp, s)
        rows.append([s, len(R), rng([r["rps"] for r in R]), rng([r["cpu_ns_per_req"] for r in R]),
                     rng([r["p50_us"] for r in R]), rng([r["p99_us"] for r in R]), rng([r["p999_us"] for r in R]),
                     sum(r["errors"] for r in R), rng([r["vmhwm"] / 1e6 for r in R], "{:.1f}")])
    table(f"load: {title}", ["server", "runs", "req/s", "server CPU ns/req", "p50 us", "p99 us", "p999 us", "errors", "peak RSS MB"], rows)

# block: 1,000 connections on GET /block, a handler that sleeps 10 ms (the stand-in for a database
# call). The ideal is 1000 / 0.010 = 100,000 req/s and a p50 just over 10 ms; peak RSS shows what
# a server pays for 1,000 blocked handlers. Skipped when the run had no block files.
rows = []
for s in servers("block"):
    R = load("block", s)
    if not R:
        continue
    rows.append([s, len(R), rng([r["rps"] for r in R]), rng([r["p50_us"] for r in R]), rng([r["p99_us"] for r in R]),
                 rng([r["p999_us"] for r in R]), sum(r["errors"] for r in R),
                 rng([r["peak_rss_sampled"] / 1e6 for r in R], "{:.1f}"), rng([r["vmhwm"] / 1e6 for r in R], "{:.1f}")])
table("block: GET /block (10 ms sleep), 1,000 connections",
      ["server", "runs", "req/s", "p50 us", "p99 us", "p999 us", "errors", "peak RSS MB (sampled)", "peak RSS MB (VmHWM)"], rows)

# WebSocket (ws-run.sh): the echo of one message a round trip, raw bytes and typed JSON.
rows = []
for s in servers("wsallocs"):
    R = load("wsallocs", s)
    rows.append([s, len(R), rng([r["allocs_per_req"] for r in R], "{:.2f}"), rng([r["bytes_per_req"] for r in R])])
table("WebSocket allocations per message (raw echo of 32 B, one socket, server mallocs delta)", ["server", "runs", "allocs/msg", "bytes/msg"], rows)

for exp, title in (("wsraw32", "raw echo, 32 B"), ("wsraw4k", "raw echo, 4 KiB"), ("wsjson32", "JSON echo, 32 B"), ("wsjson4k", "JSON echo, 4 KiB")):
    rows = []
    for s in servers(exp):
        R = load(exp, s)
        rows.append([s, len(R), rng([r["rps"] for r in R]), rng([r["cpu_ns_per_req"] for r in R]),
                     rng([r["p50_us"] for r in R]), rng([r["p99_us"] for r in R]), rng([r["p999_us"] for r in R]),
                     sum(r["errors"] for r in R), rng([r["vmhwm"] / 1e6 for r in R], "{:.1f}")])
    table(f"WebSocket load: {title}", ["server", "runs", "msg/s", "server CPU ns/msg", "p50 us", "p99 us", "p999 us", "errors", "peak RSS MB"], rows)

for exp, label in (("idle", "connection, each served one request"), ("wsidle", "WebSocket, each echoed one message")):
    rows = []
    for s in servers(exp):
        R = load(exp, s)
        if not R:
            continue
        rss = [(r["rss_n2"] - r["rss_n1"]) / (r["conns_n2"] - r["conns_n1"]) for r in R]
        rss1 = [(r["rss_n1"] - r["rss0"]) / r["conns_n1"] for r in R]
        if all(r.get("stats_n1") and r.get("stats_n2") for r in R):
            hsm = [(hs(r["stats_n2"]) - hs(r["stats_n1"])) / (r["conns_n2"] - r["conns_n1"]) for r in R]
            gor = [r["stats_n2"]["goroutines"] for r in R]
        else:
            hsm, gor = [], []
        rows.append([s, len(R), rng(rss), rng(hsm), rng(rss1), rng(gor), sum(r.get("fails", 0) for r in R)])
    table(f"idle memory per {label}, bytes (marginal n1 to n2)",
          ["server", "runs", "RSS marginal", "heap+stack marginal", "RSS avg 0 to n1", "goroutines @n2", "failed conns"], rows)

try:
    sz = json.load(open(f"{OUT}/sizes.json"))
    table("binary size (stripped; Go: -trimpath -ldflags '-s -w', Zig: ReleaseFast strip)", ["server", "bytes", "MB"],
          [[s, sz[s], f"{sz[s] / 1e6:.2f}"] for s in ORDER if s in sz])
except Exception:
    pass
