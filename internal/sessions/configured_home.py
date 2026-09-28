"""Discover the configuration directory from the agent process below the exact
tmux pane. This deliberately does not use the exporting user's HOME."""
import os, subprocess, sys
agent, name = sys.argv[1:]
try:
    root = int(subprocess.check_output(['tmux','display-message','-p','-t','='+name+':','#{pane_pid}'], stderr=subprocess.DEVNULL, text=True).strip())
    seen = set(); queue = [root]; children = {}
    for ent in os.listdir('/proc'):
        if not ent.isdigit(): continue
        try:
            p = int(ent); fields = open('/proc/'+ent+'/stat').read().rsplit(')',1)[1].split(); children.setdefault(int(fields[1]), []).append(p)
        except (OSError, ValueError, IndexError): pass
    while queue:
        p = queue.pop()
        if p in seen: continue
        seen.add(p); queue.extend(children.get(p, []))
    for p in sorted(seen):
        try:
            executable = os.path.basename(os.readlink('/proc/'+str(p)+'/exe'))
            # Claude Code installs each release under a versioned executable
            # path (for example .../versions/2.1.268), while its process comm
            # remains the name claude. This probe only locates the configured home;
            # CaptureNativeID separately validates PID/starttime, transcript,
            # workspace, and tmux tracking identity before accepting a CID.
            comm = open('/proc/'+str(p)+'/comm').read().strip()
            if executable != agent and not (agent == 'claude' and comm == 'claude'): continue
            env = {}
            for line in open('/proc/'+str(p)+'/environ','rb').read().split(b'\0'):
                if b'=' in line:
                    k,v=line.split(b'=',1); env[k.decode(errors='ignore')]=v.decode(errors='ignore')
            key = 'CODEX_HOME' if agent == 'codex' else 'CLAUDE_CONFIG_DIR'
            value = env.get(key, '')
            if not value: value = os.path.join(env.get('HOME',''), '.codex' if agent == 'codex' else '.claude')
            if value: print(os.path.realpath(os.path.expanduser(value))); raise SystemExit(0)
        except (OSError, ValueError): pass
except (OSError, ValueError, subprocess.SubprocessError): pass
raise SystemExit(1)
