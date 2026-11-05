# depth.py <frames> <pprof -traces output> (probe.sh depth): the three deepest sampled call chains of
# a request, each frame with its locals from the compiler (-S) and 16 B of return address and
# frame pointer added; -1 is a frame not in the list (a generic shape), an inlined frame counts 0,
# and chains already on the system stack are skipped.
import sys,re
fr={}
for l in open(sys.argv[1]):
    a,b=l.split(None,1); fr[b.strip()]=int(a)
best=[]
for blk in open(sys.argv[2]).read().split('-----------+-------------------------------------------------------')[1:]:
    lines=[l for l in blk.strip().split('\n') if l.strip()]
    funcs=[]
    for i,l in enumerate(lines):
        l=l.strip()
        if i==0:
            parts=l.split(None,1)
            if len(parts)<2: continue
            l=parts[1]
        inl='(inline)' in l
        name=l.replace('(inline)','').strip()
        funcs.append((name,inl))
    # from leaf to root
    tot=0; chain=[]
    for name,inl in funcs:
        if inl: chain.append((name,0)); continue
        f=fr.get(name)
        if f is None: f=-1
        tot+=max(f,0)+16
        chain.append((name,f))
    if any('runtime.mcall' in n or 'runtime.morestack' in n or 'systemstack' in n for n,_ in chain): continue
    best.append((tot,chain))
best.sort(key=lambda x:-x[0])
seen=set()
for tot,chain in best[:3]:
    print('TOTAL',tot)
    for n,f in reversed(chain): print('  %5d %s'%(f,n))
