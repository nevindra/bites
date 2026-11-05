#!/usr/bin/env python3
# Marginal bytes per idle connection, n1 to n2: RSS, and heap_alloc plus stacks (runtime/metrics
# as the server's SIGUSR1 stats report them). Prints one line per run and the mean and range.
import json, glob, statistics as st, sys
d = sys.argv[1] if len(sys.argv) > 1 else "out"
for mode in ("base", "plain", "tls"):
    rss, hs = [], []
    for f in sorted(glob.glob(f"{d}/idle-{mode}-*.json")):
        r = json.load(open(f))
        n = r["conns_n2"] - r["conns_n1"]
        a, b = r["stats_n1"], r["stats_n2"]
        rss.append((r["rss_n2"] - r["rss_n1"]) / n)
        hs.append(((b["heap_alloc"] + b["stacks"]) - (a["heap_alloc"] + a["stacks"])) / n)
        print(f"{mode} {f}: conns {r['conns_n1']}->{r['conns_n2']} fails {r.get('fails', 0)} rss {rss[-1]:.0f} B  heap+stack {hs[-1]:.0f} B")
    for name, v in (("rss", rss), ("heap+stack", hs)):
        print(f"{mode} {name}: mean {st.mean(v):.0f} B  min {min(v):.0f}  max {max(v):.0f}")
