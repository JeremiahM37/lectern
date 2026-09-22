#!/usr/bin/env python3
"""Orchestration benchmark: same task, three arms, objective grading.

  bench.py run <task> <arm> <rep>      one run -> results/<task>-<arm>-<rep>.json
  bench.py summary                     table over results/

Arms: astra   (all-Astra, no skill)
      theirs  (astra-flash-orchestrator verbatim + process transport; Flash worker)
      lectern (Lectern delegated build: lead Astra + Flash worker task)
"""
import glob, json, os, pathlib, shutil, subprocess, sys, time, yaml

B = pathlib.Path("/mnt/bulk/codex-bench")
TASKS = {"receipt-stats": "action-receipt", "lectern-sessions-cli": "lectern", "librarr-source-health": "librarr", "lectern-tasks-cli": "lectern"}
ARMS = {
    "astra":   {"home": B/"home-astra",   "prefix": ""},
    "theirs":  {"home": B/"home-theirs",  "prefix": "$astra-flash-orchestrator "},
    "lectern": {"home": B/"home-lectern", "prefix": "$lectern-delegate "},
    # the upstream package installed by its own installer, native spawn_agent
    # through codex-router (signed in; route unhidden + selected), HOME carries
    # ~/.agents/skills as its installer lays it out
    "native":  {"home": B/"home",         "prefix": "$astra-flash-orchestrator ", "user_home": B/"userhome-native"},
}
ROOT_MODEL = "gpt-6-astra"
PRICE = {  # their estimator, per 1M tokens; Flash at DeepSeek's peak rates
    ROOT_MODEL: {"in": 10.0, "cached": 1.0, "out": 50.0},
    "deepseek-flash": {"in": 0.30, "cached": 0.006, "out": 1.20},
}

def key():
    return yaml.safe_load(open("/home/admin/.ai-secrets/deepseek-api.yaml"))["deepseek"]["api_key"]

def sessions_since(home, t0, cwd, prefix=False):
    """Token usage of every session recorded under `home` for this run's cwd
    (or, with prefix=True, any cwd under it: a worker in a task worktree)."""
    out = []
    for f in glob.glob(str(home/"sessions"/"**"/"*.jsonl"), recursive=True):
        if os.path.getmtime(f) < t0 - 5:
            continue
        meta = None; model = None; last = None; models = set()
        with open(f) as fh:
            for line in fh:
                try: o = json.loads(line)
                except ValueError: continue
                p = o.get("payload", {})
                t = p.get("type") or o.get("type")
                if t == "session_meta": meta = p
                if t == "turn_context" and p.get("model"): model = p["model"]; models.add(p["model"])
                if t == "token_count" and p.get("info"): last = p["info"].get("total_token_usage")
        got = os.path.realpath(meta.get("cwd", "")) if meta else ""
        want = os.path.realpath(cwd)
        if not meta or not (got == want or (prefix and got.startswith(want + "/"))):
            continue
        out.append({"file": f, "id": meta.get("id"), "model": model, "models": models, "usage": last or {}})
    return out

def cost(model, u):
    p = PRICE.get(model)
    if not p or not u: return None
    cached = u.get("cached_input_tokens", 0)
    return round((u.get("input_tokens", 0) - cached) / 1e6 * p["in"] + cached / 1e6 * p["cached"] + u.get("output_tokens", 0) / 1e6 * p["out"], 4)

def run(task, arm, rep):
    repo = TASKS[task]; a = ARMS[arm]
    rd = B/"runs"/f"{task}-{arm}-{rep}"; ws = rd/"repo"
    if rd.exists(): shutil.rmtree(rd)
    rd.mkdir(parents=True)
    subprocess.run(["git", "clone", "-q", str(B/"base"/repo), str(ws)], check=True)
    if repo == "lectern":  # the Go bundle's frontend deps, so a worker can rebuild if it wants
        subprocess.run(["cp", "-a", "/mnt/bulk/lectern/frontend/node_modules", str(ws/"frontend"/"node_modules")])
    spec = (B/"tasks"/task/"SPEC.md").read_text()
    base = subprocess.run(["git", "-C", str(ws), "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    prompt = a["prefix"] + "Implement the following task in the current repository.\n\n" + spec
    if arm == "lectern":
        import urllib.request
        api = json.loads((B/"home-lectern"/"bench-env.json").read_text())["LECTERN_API"]
        pname = f"{task}-{arm}-{rep}-{int(time.time()) % 100000}"
        branch = subprocess.run(["git", "-C", str(ws), "rev-parse", "--abbrev-ref", "HEAD"], capture_output=True, text=True).stdout.strip()
        req = urllib.request.Request(api + "/api/projects", data=json.dumps({"name": pname, "target_id": 1, "repo_path": str(ws), "default_agent": "codex", "default_base_branch": branch}).encode(),
                                     headers={"Content-Type": "application/json"}, method="POST")
        urllib.request.urlopen(req).read()
        prompt += f"\n\nThis checkout is registered in Lectern as the project named `{pname}`; its path is {ws}.\n"
    env = {k: v for k, v in os.environ.items() if not (k.startswith("LECTERN_") or k.startswith("AGENTDECK_") or k == "GRIMOIRE_SESSION")}
    env.update({"CODEX_HOME": str(a["home"]), "PATH": "/usr/local/go/bin:" + env["PATH"],
                "BENCH_RUN_DIR": str(rd)})
    if a.get("user_home"):
        # HOME moves for the skill layout only; the Go caches stay where they
        # are so a build does not re-download the world and skew wall time
        env["HOME"] = str(a["user_home"])
        env.update({"GOPATH": "/home/admin/go", "GOMODCACHE": "/home/admin/go/pkg/mod", "GOCACHE": "/home/admin/.cache/go-build"})
    if arm == "native":
        import socket
        with socket.socket() as sk:
            if sk.connect_ex(("127.0.0.1", 4202)) != 0:
                raise SystemExit("codex-router is not listening on 4202; start it (tmux codex-router-bench) first")
    if arm == "lectern":
        env.update(json.loads((B/"home-lectern"/"bench-env.json").read_text()))
    t0 = time.time()
    with open(rd/"root.jsonl", "w") as out, open(rd/"root.err", "w") as err:
        p = subprocess.run(["codex", "exec", "--skip-git-repo-check", "-C", str(ws), "--dangerously-bypass-approvals-and-sandbox", "--json", "-"],
                           input=prompt, env=env, stdout=out, stderr=err, text=True, timeout=5400)
    wall = time.time() - t0
    grade = subprocess.run([str(B/"tasks"/task/"grade.sh"), str(ws)], capture_output=True, text=True).stdout
    regraded = False
    if "suite=FAIL" in grade:
        # The repositories' own suites include real-process tests that flake
        # when several runs grade at once; one regrade separates that from a
        # regression, and the record says it happened.
        again = subprocess.run([str(B/"tasks"/task/"grade.sh"), str(ws)], capture_output=True, text=True).stdout
        regraded = True
        if "suite=PASS" in again:
            grade = again
    diff = subprocess.run(["git", "-C", str(ws), "diff", "--shortstat", base], capture_output=True, text=True).stdout.strip()
    untracked = subprocess.run(["git", "-C", str(ws), "ls-files", "--others", "--exclude-standard"], capture_output=True, text=True).stdout.split()
    root = sessions_since(a["home"], t0, ws)
    workers = sessions_since(B/"worker-home", t0, ws) + sessions_since(B/"worker-home-lectern", t0, rd, prefix=True)
    if arm == "native":
        # a native child is a thread in the same home; it is the one whose
        # turns ran on the routed model
        workers = [s for s in root if any("deepseek" in m for m in s["models"])]
        root = [s for s in root if not any("deepseek" in m for m in s["models"])]
    def total(ss, model):
        agg = {}
        for s in ss:
            if model and s["model"] != model: continue
            for k, v in s["usage"].items(): agg[k] = agg.get(k, 0) + v
        return agg
    ru = total(root, ROOT_MODEL); wu = total(workers, None)
    if arm == "native":
        wu = total(workers, None)
    # a root session that itself ran on a non-root model would be a routing failure: record it
    stray = [s["model"] for s in root if s["model"] != ROOT_MODEL]
    res = {"task": task, "arm": arm, "rep": rep, "exit": p.returncode, "wall_s": round(wall, 1),
           "grade": grade.strip().split("\n")[0], "grade_detail": grade.strip(), "suite_regraded": regraded, "diff": diff,
           "untracked": [u for u in untracked if "node_modules" not in u][:20],
           "root_sessions": len(root), "worker_sessions": len(workers), "root_stray_models": stray,
           "root_usage": ru, "worker_usage": wu,
           "root_cost_usd": cost(ROOT_MODEL, ru), "worker_cost_usd": cost("deepseek-flash", wu)}
    (B/"results").mkdir(exist_ok=True)
    (B/"results"/f"{task}-{arm}-{rep}.json").write_text(json.dumps(res, indent=2))
    print(json.dumps({k: res[k] for k in ("task","arm","rep","exit","wall_s","grade","diff","root_usage","worker_usage","root_cost_usd","worker_cost_usd","root_stray_models")}, indent=1))

def summary():
    rows = [json.loads(open(f).read()) for f in sorted(glob.glob(str(B/"results"/"*.json")))]
    print(f"{'task':22} {'arm':8} rep {'accept':7} {'suite':6} {'wall':>6} {'astra_in':>9} {'astra_cached':>12} {'astra_out':>9} {'flash_in':>9} {'flash_out':>9} {'$astra':>7} {'$flash':>7}")
    for r in rows:
        g = r["grade"]; ru = r["root_usage"]; wu = r["worker_usage"]
        print(f"{r['task']:22} {r['arm']:8} {r['rep']:3} {('PASS' if 'accept=PASS' in g else 'FAIL'):7} {('PASS' if 'suite=PASS' in g else 'FAIL'):6} {r['wall_s']:6.0f} {ru.get('input_tokens',0):9} {ru.get('cached_input_tokens',0):12} {ru.get('output_tokens',0):9} {wu.get('input_tokens',0):9} {wu.get('output_tokens',0):9} {r['root_cost_usd'] or 0:7.2f} {r['worker_cost_usd'] or 0:7.2f}")

if __name__ == "__main__":
    if sys.argv[1] == "run": run(sys.argv[2], sys.argv[3], sys.argv[4])
    else: summary()
