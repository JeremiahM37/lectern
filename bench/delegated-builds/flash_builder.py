#!/usr/bin/env python3
"""Delegation transport for the benchmark: runs the installed Flash worker as a
separate Codex process and prints its completion report.

Native spawn_agent cannot host a non-OpenAI child on a ChatGPT login (see
ROUTER-FINDING.md), so this is the one substitution in an otherwise verbatim
copy of astra-flash-orchestrator. The worker model, its developer instructions
(WORKER-INSTRUCTIONS.md) and its effort are fixed by the worker's CODEX_HOME.

  flash_builder.py --check
  flash_builder.py --workdir DIR --brief FILE            # new worker, prints report + thread id
  flash_builder.py --workdir DIR --brief FILE --resume ID # one consolidated correction cycle
"""
import argparse, json, os, subprocess, sys, pathlib

WORKER_HOME = os.environ.get("FLASH_WORKER_HOME", "/mnt/bulk/codex-bench/worker-home")

def key():
    import yaml
    return yaml.safe_load(open("/home/admin/.ai-secrets/deepseek-api.yaml"))["deepseek"]["api_key"]

def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true")
    ap.add_argument("--workdir")
    ap.add_argument("--brief")
    ap.add_argument("--resume")
    a = ap.parse_args()
    env = {**os.environ, "CODEX_HOME": WORKER_HOME, "DEEPSEEK_API_KEY": key()}
    for v in list(env):
        if v.startswith("LECTERN_") or v.startswith("AGENTDECK_") or v == "GRIMOIRE_SESSION":
            env.pop(v)
    if a.check:
        r = subprocess.run(["codex", "exec", "--skip-git-repo-check", "-C", "/tmp", "Reply with exactly: WORKER-OK"],
                           env=env, stdin=subprocess.DEVNULL, capture_output=True, text=True, timeout=300)
        ok = "WORKER-OK" in r.stdout
        print(json.dumps({"worker_ok": ok, "model": "deepseek-flash", "provider": "deepseek"}))
        sys.exit(0 if ok else 1)
    if not (a.workdir and a.brief):
        ap.error("--workdir and --brief are required")
    brief = pathlib.Path(a.brief).read_text()
    cmd = ["codex", "exec", "--skip-git-repo-check", "-C", a.workdir,
           "--dangerously-bypass-approvals-and-sandbox", "--json"]
    if a.resume:
        cmd = ["codex", "-C", a.workdir, "exec", "resume", a.resume, "--skip-git-repo-check",
               "--dangerously-bypass-approvals-and-sandbox", "--json", "-"]
    else:
        cmd.append("-")
    r = subprocess.run(cmd, input=brief, env=env, capture_output=True, text=True, timeout=3600)
    thread = None; last = None
    for line in r.stdout.splitlines():
        try:
            o = json.loads(line)
        except ValueError:
            continue
        if o.get("type") == "thread.started":
            thread = o.get("thread_id")
        item = o.get("item") or {}
        if o.get("type") == "item.completed" and item.get("type") == "agent_message":
            last = item.get("text")
    print(f"WORKER THREAD: {thread or a.resume}")
    print("WORKER REPORT:")
    print(last or "(the worker produced no final message)")
    if r.returncode != 0:
        print(f"WORKER EXIT: {r.returncode}\n{r.stderr[-2000:]}")
    sys.exit(0)

if __name__ == "__main__":
    main()
