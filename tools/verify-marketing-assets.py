"""Check the refreshed README's local links and recorded media integrity."""
import json
from pathlib import Path
import re
import struct
import subprocess

root = Path(__file__).resolve().parents[1]
assets = root / 'docs/media/control-plane'
docs = [root / 'README.md', root / 'docs/media/README.md', assets / 'README.md', root / '.agents/distribution.md', root / 'docs/internal/launch/control-plane-refresh.md']
for doc in docs:
    text = doc.read_text()
    links = re.findall(r'\]\(([^)]+)\)', text) + re.findall(r'src="([^"]+)"', text)
    for link in links:
        if '://' in link or link.startswith('#'):
            continue
        dest = link.split('#', 1)[0]
        assert (doc.parent / dest).exists(), f'{doc.relative_to(root)}: missing {dest}'
    assert not re.search(r'\.ts\.net|\.internal|/home/admin|100\.\d+\.\d+\.\d+', text), doc
for name in ['machines', 'tasks', 'dispatch', 'review', 'sessions', 'phone-approval', 'phone-sessions', 'pdf-preview', 'file-upload', 'native-cli', 'social-preview']:
    blob = (assets / (name + '.png')).read_bytes()
    assert blob[:8] == b'\x89PNG\r\n\x1a\n', name
    width, height = struct.unpack('>II', blob[16:24])
    assert width >= 390 and height >= 600, (name, width, height)
    if name == 'social-preview':
        assert (width, height) == (1200, 630)
for name in ['control-plane.mp4', 'phone-approval.mp4', 'files.mp4', 'dispatch-review.gif']:
    media = assets / name
    info = json.loads(subprocess.check_output(['ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json', str(media)]))
    assert any(s['codec_type'] == 'video' for s in info['streams']), name
    assert 3 < float(info['format']['duration']) < 60, name
    subprocess.run(['ffmpeg', '-v', 'error', '-i', str(media), '-f', 'null', '-'], check=True)
    print(f'{name}: {float(info["format"]["duration"]):.1f}s; decodes successfully')
for name, mock in [('capture-report.json', True), ('file-report.json', False)]:
    report = json.loads((assets / name).read_text())
    assert report['build']['ok'] and report['build']['mock'] == mock, name
    assert not report['errors'] and len(report['checks']) >= 4, name
print('PASS: documentation links, capture reports, 11 screenshots and 4 playable media files')
