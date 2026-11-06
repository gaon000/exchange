"""Build only from versioned sources; never execute binaries retained in imported bundles."""
import os
import platform
import shutil
import subprocess
from pathlib import Path
from common import REPO, digest, read, require, save

ASSETS = Path(__file__).resolve().parent / 'assets'


def go(*args, **kwargs):
    return subprocess.run([str(REPO/'scripts/go'), *map(str, args)], cwd=REPO,
                          check=True, **kwargs)


def prepare_overlay(root, mode):
    require(mode in ('trace', 'io'), 'unknown overlay mode')
    sources = read(ASSETS/'sources.json')
    (root/'overlay').mkdir()
    replacements = {}
    for name, entry in sources.items():
        if name == 'durable.go' and mode != 'io':
            continue
        source = REPO/entry['source']
        require(digest(source) == entry['sha256'],
                f'overlay source changed: {entry["source"]}; review templates and sources.json first')
        content = (ASSETS/(name+'.tmpl')).read_text()
        if name == 'exchange-main.go' and mode == 'trace':
            content = content.replace('\n\t"github.com/gaon000/exchange/internal/durable"', '')
            content = content.replace('\n\t\tioErr = durable.WriteIORecords(path + ".sync.jsonl")', '')
        target = root/'overlay'/name
        target.write_text(content)
        replacements[str(source)] = str(target)
    save(root/'overlay.json', {'Replace': replacements})
    return root/'overlay.json'


def build(root, suite):
    (root/'bin').mkdir()
    if suite == 'timer':
        go('build', '-o', root/'bin/timerprobe', './tools/timerprobe')
    else:
        require(platform.system() == 'Darwin' and platform.machine() == 'arm64',
                'trade diagnostics require the supported macOS arm64/APFS/cgo environment')
        args = ['build']
        if suite in ('trace', 'io'):
            args += ['-overlay', prepare_overlay(root, suite)]
        args += ['-o', root/'bin', './cmd/exchange', './cmd/loadgen']
        go(*args)
        go('build', '-o', root/'bin/experimentcheck', './tools/experimentcheck')
        if suite == 'io':
            subprocess.run(['/usr/bin/clang', '-Wall', '-Wextra', '-Werror', '-O2',
                            str(ASSETS/'procio.c'), '-o', str(root/'bin/procio')], check=True)
    require(os.access(root/'bin'/('timerprobe' if suite == 'timer' else 'exchange'), os.X_OK),
            'missing built binary')


def build_checker(output):
    go('build', '-o', output, './tools/experimentcheck')
