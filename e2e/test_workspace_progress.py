"""Read target workspace progress through the terminal actions menu."""
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard, run_command, new_session
from test_multi_workspace import grouped
from test_session_restore import request


def test_terminal_reads_workspace_setup_progress(real_terminal):
    t=real_terminal;row=grouped(t)
    assert request(t,'DELETE',f"/sessions/{row['id']}")[0]==200
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('z');d.wait('Grouped review')
        d.send('/Grouped review\r')
        run_command(d,'Workspace setup progress')
        d.wait('Recorded workspace state: ready');d.wait('Second repository: ready')
        d.send('\x1b');d.quit()
    finally:d.close()
