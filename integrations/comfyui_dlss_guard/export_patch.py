"""Export only the reviewed lifecycle changes against the pinned upstream source."""
import difflib
import hashlib
import json
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
UPSTREAM = REPO / 'artifacts/dlss5-community-20260921/node'
INSTALLED = Path('C:/Work/ComfyUI/custom_nodes/ComfyUI-DLSS5-Enhancer')
COMMIT = '796ed5927a202ba50b5c929cd08e16b365041162'
FILES = ['dlss5/session.py', 'nodes/enhance_images.py', 'nodes/enhance_video.py']

if __name__ == '__main__':
    diff = []
    manifest = {'upstream_commit': COMMIT, 'files': []}
    for name in FILES + ['dlss5/lifecycle.py']:
        before = '' if name.endswith('/lifecycle.py') else subprocess.check_output(
            ['git', '-C', str(UPSTREAM), 'show', COMMIT + ':' + name]).decode('utf-8')
        after = (INSTALLED / name).read_text(encoding='utf-8')
        if name.endswith('/lifecycle.py') and after != (HERE / 'lifecycle.py').read_text(encoding='utf-8'):
            raise RuntimeError('Installed lifecycle differs from reviewed source')
        diff.extend(difflib.unified_diff(before.splitlines(True), after.splitlines(True),
                    fromfile='a/' + name if before else '/dev/null', tofile='b/' + name))
        manifest['files'].append({'path': name, 'before_sha256': hashlib.sha256(before.encode()).hexdigest() if before else None,
                                 'after_sha256': hashlib.sha256(after.encode()).hexdigest()})
    (HERE / 'guard.patch').write_text(''.join(diff), encoding='utf-8', newline='\n')
    (HERE / 'guard-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n', encoding='utf-8')
