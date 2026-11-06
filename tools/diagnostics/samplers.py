"""Bounded, owned read-only samplers. Unavailable data never becomes zero."""
import json
import subprocess
import threading
import time
from common import lines, require, save
from recovery import now, start


class Samplers:
    def __init__(self, root, case, io=False):
        self.root, self.case, self.io = root, case, io
        self.host = None
        self.thread = None
        self.targets = []
        self.errors = []
        self.handles = []

    def __enter__(self):
        output = (self.root/(self.case+'-host.jsonl')).open('x')
        error = (self.root/(self.case+'-host.stderr')).open('x')
        self.handles += [output, error]
        try:
            self.host = subprocess.Popen(['/usr/sbin/iostat', '-d', '-C', '-w', '1'],
                                         stdout=subprocess.PIPE, stderr=error, text=True)
        except BaseException:
            output.close()
            error.close()
            raise

        def collect():
            try:
                for index, line in enumerate(self.host.stdout):
                    require(index < 12000 and len(line) < 65536, 'host sampler output limit')
                    output.write(json.dumps({'utc': now(), 'monotonicNanos': time.monotonic_ns(),
                                             'line': line.rstrip()})+'\n')
                    output.flush()
            except BaseException as error:
                self.errors.append(repr(error))
                self.host.terminate()
        self.thread = threading.Thread(target=collect)
        self.thread.start()
        return self

    def start(self, argv, case, name):
        target = start(argv, case, name)
        if self.io and name in ('server-ready', 'generator'):
            try:
                output = (case/(name+'-procio.jsonl')).open('xb')
                error = (case/(name+'-procio.stderr')).open('xb')
                self.handles += [output, error]
                sampler = subprocess.Popen([str(self.root/'bin/procio'), str(target.pid)],
                                            stdout=output, stderr=error)
                self.targets.append((sampler, case/(name+'-procio.jsonl')))
            except BaseException:
                target.terminate()
                target.wait(timeout=30)
                raise
        return target

    def __exit__(self, exc_type, exc, tb):
        try:
            require(self.host.poll() is None, "host sampler exited early")
            self.host.terminate()
            self.host.wait(timeout=10)
            self.thread.join(timeout=10)
            require(not self.thread.is_alive(), 'host collector did not join')
            for sampler, path in self.targets:
                try:
                    sampler.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    sampler.terminate()
                    sampler.wait(timeout=5)
                    self.errors.append(f'process sampler did not exit with its target: {path.name}')
                require(sampler.returncode == 0, f'process sampler exited {sampler.returncode}')
                records = lines(path)
                valid = [r for r in records if r['available']]
                require(2 <= len(valid) < 4000, 'process sampler incomplete or limit reached')
                require(all(r.get('errno') == 3 for r in records if not r['available']),
                        'OS process counters unavailable')
                require(not path.with_suffix('.stderr').stat().st_size, 'process sampler stderr')
            require(not self.errors, '; '.join(self.errors))
            require((self.root/(self.case+'-host.jsonl')).stat().st_size > 0, 'empty host sampler')
            require((self.root/(self.case+'-host.stderr')).stat().st_size == 0, 'host sampler stderr')
        except BaseException as error:
            self.errors.append(repr(error))
            if exc is None:
                raise
            save(self.root/(self.case+'-sampler-failed.json'), {'errors': self.errors})
        finally:
            if self.host.poll() is None:
                self.host.terminate()
                self.host.wait(timeout=10)
            if self.thread.is_alive():
                self.thread.join(timeout=10)
            for sampler, _ in self.targets:
                if sampler.poll() is None:
                    sampler.terminate()
                    sampler.wait(timeout=10)
            for handle in self.handles:
                handle.close()
            if self.host.stdout:
                self.host.stdout.close()
        return False
