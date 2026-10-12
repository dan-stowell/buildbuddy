#!/usr/bin/env python3
"""Cold-cache MakeEmptyImage argument probes (root), three runs per layout."""
import json
import os
from pathlib import Path
import subprocess
import sys
import time

work = Path(sys.argv[1])
work.mkdir(parents=True, exist_ok=True)
def counters():
    values = dict(line.split(':') for line in Path('/proc/self/io').read_text().splitlines())
    return {key: int(values[key]) for key in ('read_bytes', 'write_bytes', 'cancelled_write_bytes')}
for size in (256<<20, 1<<30):
    for config, extra in [('baseline', []), ('lazy-nodiscard', ['-E', 'lazy_itable_init=1,lazy_journal_init=1,nodiscard']),
                          ('prezeroed', ['-E', 'assume_storage_prezeroed=1,nodiscard'])]:
        for iteration in range(1, 4):
            subprocess.run(['sync'], check=True)
            Path('/proc/sys/vm/drop_caches').write_text('3\n')
            image = work / 'empty.ext4'
            before = counters()
            start = time.monotonic()
            subprocess.run(['/sbin/mke2fs', '-q', '-L', '', '-N', '0', '-O', '^64bit',
                            '-m', '5', '-r', '1', '-b', '4096', '-t', 'ext4', *extra,
                            str(image), str(size//1024)+'K'], check=True, stdout=subprocess.DEVNULL)
            subprocess.run(['sync'], check=True)
            elapsed = time.monotonic()-start
            after = counters()
            io = {key: after[key]-before[key] for key in before}
            io['net_write_bytes'] = io['write_bytes']-io['cancelled_write_bytes']
            st = image.stat()
            print(json.dumps(dict(config=config, run=iteration, size=size, allocated=st.st_blocks*512,
                                  seconds=elapsed, io=io)), flush=True)
            subprocess.run(['/sbin/e2fsck', '-fn', str(image)], check=True, stdout=subprocess.DEVNULL,
                           stderr=subprocess.DEVNULL)
            image.unlink()
