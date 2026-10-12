#!/usr/bin/env python3
"""Untimed layout probes against already verified baseline trees (run as root)."""
import json
import os
from pathlib import Path
import subprocess
import sys

base = Path(sys.argv[1])
measurements = Path(sys.argv[2])
for name in ('alpine', 'ubuntu', 'rbe'):
    row = json.loads((measurements / (name + '.jsonl')).read_text().splitlines()[0])
    mount = base / name / 'sizing-mount'
    mount.mkdir(exist_ok=True)
    subprocess.run(['mount', '-o', 'loop,ro', str(base / name / 'baseline-1.ext4'), str(mount)], check=True)
    try:
        variants = [('default', ['-N', '0']),
                    ('count', ['-N', str(row['Entries'] + max(row['Entries']//10, 128))]),
                    ('i65536', ['-i', '65536']), ('i131072', ['-i', '131072'])]
        for config, inode_args in variants:
            # Minimum at 1 MiB resolution, with lazy initialization and journal
            # retained. Reserved blocks do not restrict mke2fs running as root.
            low, high = 4, max((row['Apparent'] + (1<<20)-1)//(1<<20), (row['Entries']*131072 + (1<<20)-1)//(1<<20) + 64)
            while low <= high:
                mib = (low + high)//2
                image = base / name / 'sizing.ext4'
                image.unlink(missing_ok=True)
                args = ['/sbin/mke2fs', '-q', '-L', "''", *inode_args,
                        '-O', '^64bit', '-d', str(mount), '-m', '5', '-r', '1',
                        '-b', '4096', '-t', 'ext4', '-E',
                        'lazy_itable_init=1,lazy_journal_init=1,nodiscard',
                        str(image), str(mib*1024)+'K']
                p = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                                   env=dict(os.environ, E2FSPROGS_FAKE_TIME='1700000000'))
                print(json.dumps(dict(image=name, config=config, size_mib=mib,
                                      success=p.returncode == 0)), flush=True)
                image.unlink()
                if p.returncode == 0:
                    high = mib - 1
                else:
                    low = mib + 1
            print(json.dumps(dict(image=name, config=config, minimum_mib=low,
                                  estimate=row['Estimate'], entries=row['Entries'])), flush=True)
    finally:
        subprocess.run(['umount', str(mount)], check=True)
