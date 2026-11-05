import json,glob,sys,statistics as st
S=["stdhttp","fasthttp","fib","nbio","hertz","hand-a","hand-b","fib-async"]
def load(exp,s):
    out=[]
    for f in sorted(glob.glob(f"raw/{exp}-{s}-[0-9].json")):
        try: out.append(json.load(open(f)))
        except Exception: pass
    return out
def rng(v,fmt="{:.0f}"):
    if not v: return "n/a"
    return f"{fmt.format(st.mean(v))} [{fmt.format(min(v))}-{fmt.format(max(v))}]"
print("## A idle (bytes per connection; mean [min-max] over runs)")
print("| server | runs | RSS base | RSS/conn @10k | RSS marginal 10k->20k | heap+stack/conn @10k | heap+stack marginal | goroutines @20k |")
for s in S:
    R=load("idle",s)
    if not R: continue
    def hs(m,t): return m[t]["heap_inuse"]+m[t]["stack_inuse"]
    base=[r["rss0"]/1e6 for r in R]
    a=[(r["rss_n1"]-r["rss0"])/r["conns_n1"] for r in R]
    m=[(r["rss_n2"]-r["rss_n1"])/(r["conns_n2"]-r["conns_n1"]) for r in R]
    ha=[(hs(r,"stats_n1")-hs(r,"stats0"))/r["conns_n1"] for r in R]
    hm=[(hs(r,"stats_n2")-hs(r,"stats_n1"))/(r["conns_n2"]-r["conns_n1"]) for r in R]
    g=[r["stats_n2"]["goroutines"] for r in R]
    print(f"| {s} | {len(R)} | {rng(base,'{:.1f}')} MB | {rng(a)} | {rng(m)} | {rng(ha)} | {rng(hm)} | {rng(g)} |")
for exp,title in (("plain","B plain GET / 256 conns"),("block","C GET /block 1000 conns")):
    print(f"\n## {title}")
    print("| server | runs | req/s | p50 us | p99 us | errors | peak RSS MB (VmHWM) | peak goroutines | gomaxprocs |")
    for s in S:
        R=load(exp,s)
        if not R: continue
        print(f"| {s} | {len(R)} | {rng([r['rps'] for r in R])} | {rng([r['p50_us'] for r in R])} | {rng([r['p99_us'] for r in R])} | {sum(r['errors'] for r in R)} | {rng([r['vmhwm']/1e6 for r in R],'{:.1f}')} | {rng([r['stats']['max_goroutines'] for r in R])} | {R[0]['stats']['gomaxprocs']} |")
