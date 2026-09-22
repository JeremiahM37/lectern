#!/usr/bin/env python3
"""Aggregate results/ into the Markdown tables the README carries."""
import glob, json, statistics as st
from collections import defaultdict

rows = [json.loads(open(f).read()) for f in sorted(glob.glob("results/*.json"))]
rows = [r for r in rows if str(r["rep"]) != "0"]  # rep 0 was the pipeline trial
ARMS = ["astra", "theirs", "lectern"]
LABEL = {"astra": "All-Astra", "theirs": "astra-flash-orchestrator", "lectern": "Lectern delegated build"}
by = defaultdict(list)
for r in rows:
    by[(r["task"], r["arm"])].append(r)

def m(xs): return st.mean(xs) if xs else 0
def fmt_k(x): return f"{x/1000:.0f}k"

tasks = sorted({r["task"] for r in rows})
print("| Task | Arm | Runs | Accept | Suite | Wall s | Astra input (cached) | Astra out | Flash in / out | Est. $ |")
print("|---|---|---:|---:|---:|---:|---:|---:|---:|---:|")
tot = defaultdict(lambda: defaultdict(float)); cnt = defaultdict(int)
for t in tasks:
    for a in ARMS:
        rs = by.get((t, a), [])
        if not rs: continue
        acc = sum("accept=PASS" in r["grade"] for r in rs); su = sum("suite=PASS" in r["grade"] for r in rs)
        ain = m([r["root_usage"].get("input_tokens", 0) for r in rs]); ac = m([r["root_usage"].get("cached_input_tokens", 0) for r in rs])
        aout = m([r["root_usage"].get("output_tokens", 0) for r in rs])
        fin = m([r["worker_usage"].get("input_tokens", 0) for r in rs]); fout = m([r["worker_usage"].get("output_tokens", 0) for r in rs])
        usd = m([(r["root_cost_usd"] or 0) + (r["worker_cost_usd"] or 0) for r in rs])
        wall = m([r["wall_s"] for r in rs])
        print(f"| {t} | {LABEL[a]} | {len(rs)} | {acc}/{len(rs)} | {su}/{len(rs)} | {wall:.0f} | {fmt_k(ain)} ({fmt_k(ac)}) | {aout:,.0f} | {fmt_k(fin)} / {fmt_k(fout)} | {usd:.2f} |")
        for k, v in (("ain", ain), ("ac", ac), ("aout", aout), ("fin", fin), ("fout", fout), ("usd", usd), ("wall", wall), ("acc", acc / len(rs)), ("su", su / len(rs))):
            tot[a][k] += v
        cnt[a] += 1
print()
print("| Arm | Tasks | Accept rate | Suite rate | Mean wall s | Mean Astra input (cached) | Mean Astra out | Mean Flash in / out | Mean est. $ |")
print("|---|---:|---:|---:|---:|---:|---:|---:|---:|")
for a in ARMS:
    if not cnt[a]: continue
    n = cnt[a]; T = tot[a]
    print(f"| {LABEL[a]} | {n} | {T['acc']/n:.0%} | {T['su']/n:.0%} | {T['wall']/n:.0f} | {fmt_k(T['ain']/n)} ({fmt_k(T['ac']/n)}) | {T['aout']/n:,.0f} | {fmt_k(T['fin']/n)} / {fmt_k(T['fout']/n)} | {T['usd']/n:.2f} |")
base = tot["astra"]; th = tot["theirs"]; le = tot["lectern"]
if cnt["theirs"] and cnt["lectern"] and cnt["astra"]:
    n = cnt["astra"]
    print()
    print(f"Lectern vs upstream workflow (per-task means over {n} tasks): Astra input {le['ain']/th['ain']-1:+.0%}, Astra output {le['aout']/th['aout']-1:+.0%}, Flash input {le['fin']/th['fin']-1:+.0%}, wall {le['wall']/th['wall']-1:+.0%}, est. $ {le['usd']/th['usd']-1:+.0%}.")
    print(f"Lectern vs all-Astra: Astra input {le['ain']/base['ain']-1:+.0%} (uncached {(le['ain']-le['ac'])/(base['ain']-base['ac'])-1:+.0%}), Astra output {le['aout']/base['aout']-1:+.0%}, wall {le['wall']/base['wall']-1:+.0%}.")
