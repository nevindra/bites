#!/usr/bin/env python3 -I
"""Attribute the CPU samples of each profile to buckets and print ns per request per bucket.

Reads $CACHE/prof/<route>-<server>-<round>.pb.gz through `go tool pprof -traces`, and
out/run-<route>-<server>-<round>.txt for the request rate and the /proc counters of the window.
A sample goes to one bucket: first the syscall it is in (read, write, epoll, futex), then the GC,
the deadline code, the Date line, then the leaf-most frame that a rule names. Usage:
  python3 -I analyze.py [--unmatched]
"""
import glob, json, os, re, statistics as st, subprocess, sys

CACHE = os.path.expanduser(os.environ.get("CACHE", "~/.cache/nilo-go-profile"))
HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.environ.get("OUT", os.path.join(HERE, "out"))  # as run.sh: another directory keeps an earlier run's

def rx(*p): return re.compile("|".join(p))

SYSCALL = [  # searched over the whole stack, in this order
    ("sys write", rx(r"^syscall\.write$", r"fib/internal/sys\.Write$")),
    ("sys read", rx(r"^syscall\.read$", r"fib/internal/sys\.Read$")),
    ("sys epoll", rx(r"^runtime\.epollwait", r"EpollWait", r"epollWaiter\)\.wait", r"fib\.pollEvents", r"EpollCtl", r"runtime\.netpollopen", r"runtime\.netpollclose", r"runtime\.netpollBreak")),
    ("futex (thread sleep/wake)", rx(r"^runtime\.futex")),
]
WHOLE = [  # searched over the whole stack, after the syscalls
    ("GC", rx(r"^runtime\.(gcBgMarkWorker|gcDrain|bgsweep|bgscavenge|gcAssist|markroot|sweepone|gcStart|gcMarkDone|gcMarkTermination|gcControllerCommit)", r"^runtime\.\(\*mheap\)\.")),
    ("deadlines (SetReadDeadline, timers)", rx(r"\.SetReadDeadline", r"\.SetDeadline", r"setDeadlineImpl", r"runtime_pollSetDeadline", r"Connection\)\.(setDeadline|armDeadlineLocked)")),
    ("engine clock: Server.now() x4 and dateLine", rx(r"Server\)\.(now|dateLine)")),
]
LEAF = [  # first frame, leaf to root, that matches
    ("clock (time.Now, nanotime)", rx(r"^time\.(Now|runtimeNow|runtimeNano|Since|Until)$", r"^runtime\.nanotime", r"^runtime\.walltime")),
    ("allocation (mallocgc)", rx(r"^runtime\.(mallocgc|newobject|makeslice|growslice|convT|rawstring|concatstrings|slicebytetostring|memclrNoHeapPointers)")),
    ("scheduler and netpoll", rx(r"^runtime\.(schedule|findRunnable|park_m|gopark|goready|ready|netpoll|netpollblock|netpollready|wakep|startm|stopm|mcall|resetspinning|checkTimers|runqget|runqput|injectglist|notesleep|notewakeup|handoffp|exitsyscall|entersyscall|reentersyscall|lock2|unlock2|lockWithRank|unlockWithRank|sysmon|goexit0|gfput|newproc|systemstack|usleep|osyield|procyield|notetsleep|mPark|semacquire|semrelease|selectgo|chanrecv|chansend|park|goparkunlock|casgstatus|execute|gogo|mstart|mput|mget|pidleget|pidleput|stealWork|globrunqget|nanotime1|preemptone|retake|timeSleep|futexsleep)", r"^runtime\.\(\*timers?\)", r"^internal/poll\.runtime_pollWait", r"^internal/poll\.\(\*pollDesc\)")),
    ("router", rx(r"internal/router\.")),
    ("h1 parse", rx(r"internal/h1\.", r"lesismal/fib/http\.\(\*Parser\)", r"fib/http\.(parseRequestHead|parseSimpleRequestHead|simpleField|headerHasToken|headString|parseHeaderLine)")),
    ("decode + JSON codec", rx(r"internal/jsoncodec", r"internal/wire\.", r"encoding/json", r"jsontext", r"jsonwire", r"nilo-go\.\(\*inputPlan\)", r"nilo-go\.decode", r"nilo-go\.\(\*textRun\)", r"nilo-go\.\(\*Opt")),
    ("response write (head, outPlan)", rx(r"nilo-go\.\(\*outPlan\)", r"engine\.\(\*Response\)", r"engine\.appendClean", r"fib/http\.\(\*Context\)\.(Respond|sendResponse|writeHTTP1|ended|drop)", r"fib/http\.appendResponseHead", r"fib\.\(\*Connection\)\.(SendParts|uncork|flushLocked|queue|push|grow|consume)")),
    ("handler (user code / hand-written route)", rx(r"^main\.")),
    ("engine loop (internal/engine)", rx(r"internal/engine\.")),
    ("request ctx + typed-route glue (reqctx, root)", rx(r"internal/reqctx\.", r"nilo-go\.")),
    ("fib glue (Connection, Engine, taskpool)", rx(r"lesismal/fib")),
    ("net + poll wrappers (non-syscall part)", rx(r"^net\.", r"^internal/poll\.")),
]
SYSLEAF = re.compile(r"^(internal/runtime/syscall/linux\.Syscall6|syscall\.(Raw)?Syscall6?)$")

def parse_val(s):
    m = re.match(r"([\d.]+)(ns|us|µs|ms|s)$", s)
    return float(m.group(1)) * {"ns": 1e-6, "us": 1e-3, "µs": 1e-3, "ms": 1, "s": 1000}[m.group(2)]  # ms

def traces(binary, prof):
    out = subprocess.run(["go", "tool", "pprof", "-traces", binary, prof], capture_output=True, text=True).stdout
    blocks = out.split("-----------+-------------------------------------------------------")[1:]
    for b in blocks:
        lines = [l for l in b.strip("\n").split("\n") if l.strip()]
        if not lines: continue
        first = lines[0].split(None, 1)
        v, frames = parse_val(first[0]), [re.sub(r" \(inline\)$", "", first[1].strip())]
        frames += [re.sub(r" \(inline\)$", "", l.strip()) for l in lines[1:]]
        yield v, frames

def classify(frames):
    for name, r in SYSCALL:
        if any(r.search(f) for f in frames): return name, None
    for name, r in WHOLE:
        if any(r.search(f) for f in frames): return name, None
    for f in frames:
        for name, r in LEAF:
            if r.search(f): return name, None
    if SYSLEAF.match(frames[0]): return "sys other", frames
    return "unmatched", frames

def main():
    show_un = "--unmatched" in sys.argv
    rows = {}
    for rf in sorted(glob.glob(f"{OUT}/run-*.txt")):
        route, srv, rnd = re.match(r".*/run-(\w+)-(\w+)-(\d+)\.txt", rf).groups()
        txt = open(rf).read().split("\n")
        w = dict(zip(txt[0].split()[0::2], map(int, txt[0].split()[1::2])))
        w.update(zip(txt[1].split()[0::2], map(int, txt[1].split()[1::2])))
        w.update(zip(txt[2].split()[0::2], map(int, txt[2].split()[1::2])))
        cl = json.loads(txt[3].split(" ", 1)[1])
        reqs = cl["rps"] * 10
        prof = f"{CACHE}/prof/{route}-{srv}-{rnd}.pb.gz"
        b, un, tot = {}, {}, 0
        for v, fr in traces(f"{CACHE}/bin/{srv}", prof):
            name, extra = classify(fr)
            b[name] = b.get(name, 0) + v; tot += v
            if extra and name in ("unmatched", "sys other"):
                k = (name, " < ".join(x.split("/")[-1] for x in extra[:5])); un[k] = un.get(k, 0) + v
        r = {"total": tot * 1e6 / reqs}  # samples are ms: ms * 1e6 / requests = ns per request
        for k, v in b.items(): r[k] = v * 1e6 / reqs
        r["rps"] = cl["rps"]
        r["proc cpu ns/req (user+sys ticks)"] = (w["window_user_ticks"] + w["window_sys_ticks"]) * 1e7 / reqs
        r["user ns/req (ticks)"] = w["window_user_ticks"] * 1e7 / reqs
        r["sys ns/req (ticks)"] = w["window_sys_ticks"] * 1e7 / reqs
        r["client cpu ns/req"] = cl["cpu_ns_per_req"]
        r["reads/req (syscr)"] = w["window_syscr"] / reqs
        r["writes/req (syscw)"] = w["window_syscw"] / reqs
        r["voluntary ctx/req"] = w["window_vol_ctx"] / reqs
        r["nonvoluntary ctx/req"] = w["window_nonvol_ctx"] / reqs
        r["threads"] = w["threads"]
        rows.setdefault((route, srv), []).append(r)
        if show_un:
            print(f"-- {route} {srv} {rnd}: unmatched / other syscalls")
            for (n, k), v in sorted(un.items(), key=lambda x: -x[1])[:8]: print(f"   {v/1000:6.2f}s {n}: {k}")
    keys = sorted({k for rs in rows.values() for r in rs for k in r})
    order = [n for n, _ in SYSCALL] + [n for n, _ in WHOLE] + [n for n, _ in LEAF] + ["sys other", "unmatched"]
    metrics = ["total"] + [k for k in order if k in keys] + [k for k in keys if k not in order and k != "total"]
    for route in ("get", "post"):
        print(f"\n== {route.upper()}: mean [min-max] over {len(rows.get((route,'nilogo'),[]))} rounds, ns per request unless noted")
        print(f"{'metric':44s} {'nilo-go':>26s} {'fib':>26s} {'nilo-go minus fib':>12s}")
        for m in metrics:
            cells, means = [], []
            for srv in ("nilogo", "fib"):
                xs = [r.get(m, 0) for r in rows.get((route, srv), [])]
                if not xs: cells.append("-"); means.append(0); continue
                mu = st.mean(xs); means.append(mu)
                cells.append(f"{mu:9.1f} [{min(xs):.1f}-{max(xs):.1f}]" if mu >= 10 or m == "threads" else f"{mu:8.3f} [{min(xs):.3f}-{max(xs):.3f}]")
            d = means[0] - means[1]
            print(f"{m:44s} {cells[0]:>26s} {cells[1]:>26s} {d:12.1f}")

    print("\n== syscalls per request, by number, from the ptrace counter (syscalls.sh); only those over 0.02")
    for f in sorted(glob.glob(f"{OUT}/syscalls-*.json")):
        d = json.load(open(f)); n = d["reqs"]
        big = {k: round(v / n, 2) for k, v in sorted(d["counts"].items(), key=lambda x: -x[1]) if v / n >= 0.02}
        print(f"{os.path.basename(f)[9:-5]:18s} reqs {n:7d}  {big}")

main()
