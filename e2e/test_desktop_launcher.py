"""Default-terminal selection and literal SSH arguments; no GUI apps required."""
import json
import os
from pathlib import Path
import subprocess
import pytest
from conftest import ROOT

SCRIPT=ROOT/'web/static/desktop/setup-lectern-terminal.sh'

@pytest.mark.parametrize('installed,expected,prefix', [
    (['xdg-terminal-exec','kitty'],'xdg-terminal-exec',['--']),
    (['x-terminal-emulator','kitty'],'x-terminal-emulator',['-e']),
    (['alacritty'],'alacritty',['-e']),
    (['gnome-terminal'],'gnome-terminal',['--']),
    (['konsole'],'konsole',['-e']),
    (['kitty'],'kitty',['--']),
    (['wezterm'],'wezterm',['start','--']),
])
def test_uses_desktop_default_or_installed_terminal(tmp_path,installed,expected,prefix):
    for name in installed:
        file=tmp_path/name
        file.write_text('#!/usr/bin/python3\nimport json,os,sys\nopen(os.environ["RECEIPT"],"w").write(json.dumps({"terminal":os.path.basename(sys.argv[0]),"args":sys.argv[1:]}))\n')
        file.chmod(0o755)
    receipt=tmp_path/'receipt.json'
    env={**os.environ,'PATH':str(tmp_path),'TERMINAL':'','RECEIPT':str(receipt)}
    subprocess.run(['/bin/bash',str(SCRIPT),'lectern://attach/session/42'],env=env,check=True)
    data=json.loads(receipt.read_text())
    assert data['terminal']==expected
    assert data['args']==prefix+['env','TERM=xterm-256color','ssh','-t','-o','StrictHostKeyChecking=yes','lectern','/usr/local/bin/lectern','--hosted-attach','attach','session','42']

@pytest.mark.parametrize('uri',['lectern://attach/session/42?cmd=evil','lectern://attach/session/1;echo bad','lectern://attach/session/0','lectern://attach/unknown/12'])
def test_rejects_invalid_links_before_launch(tmp_path,uri):
    p=subprocess.run(['/bin/bash',str(SCRIPT),uri],env={**os.environ,'PATH':str(tmp_path)},capture_output=True,text=True)
    assert p.returncode==2 and 'Invalid Lectern' in p.stderr

def test_legacy_download_also_uses_default_terminal():
    assert SCRIPT.read_bytes()==(SCRIPT.parent/'setup-lectern-kitty.sh').read_bytes()
