#!/usr/bin/env python3
"""Compare mounted ext4 guest trees including metadata and hardlink topology.

Usage: sudo python3 check_trees.py BASE.ext4 VARIANT.ext4 [...]
The empty mke2fs-created lost+found directory has a conversion-time mtime;
report it separately, rather than confusing it with OCI rootfs differences.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile


def tree(root, allow_generated_times, generated_paths):
    records, links = {}, {}
    paths = [root]
    for directory, dirs, files in os.walk(root, followlinks=False):
        paths.extend(Path(directory) / n for n in dirs + files)
    for p in sorted(paths):
        name = str(p.relative_to(root))
        s = p.lstat()
        xattrs = {n: os.getxattr(p, n, follow_symlinks=False).hex()
                  for n in os.listxattr(p, follow_symlinks=False)}
        r = dict(mode=s.st_mode, uid=s.st_uid, gid=s.st_gid,
                 mtime_ns=s.st_mtime_ns, xattrs=xattrs)
        if stat.S_ISREG(s.st_mode):
            h = hashlib.sha256()
            with p.open('rb') as f:
                for b in iter(lambda: f.read(1024 * 1024), b''):
                    h.update(b)
            r.update(size=s.st_size, sha256=h.hexdigest())
            links.setdefault((s.st_dev, s.st_ino), []).append(name)
        elif stat.S_ISLNK(s.st_mode):
            r['target'] = os.readlink(p)
        elif stat.S_ISCHR(s.st_mode) or stat.S_ISBLK(s.st_mode):
            r['device'] = [os.major(s.st_rdev), os.minor(s.st_rdev)]
        records[name] = r
    for group in links.values():
        if len(group) > 1:
            for name in group:
                records[name]['hardlinks'] = sorted(group)
    if allow_generated_times:
        print('generated root mtime:', records['.']['mtime_ns'])
        records['.']['mtime_ns'] = 'extraction-generated'
    if allow_generated_times and 'lost+found' in records:
        # Only ignore its mtime if this generated directory is empty.
        if not list((root / 'lost+found').iterdir()):
            records['lost+found']['mtime_ns'] = 'mke2fs-generated'
    for name in generated_paths:
        if name in records:
            if not stat.S_ISDIR(records[name]['mode']):
                raise ValueError(f'generated timestamp exception is not a directory: {name}')
            print('generated directory mtime:', name, records[name]['mtime_ns'])
            records[name]['mtime_ns'] = 'extraction-generated'
    print('TREE_INVENTORY', json.dumps(dict(
        entries=len(records),
        regular=sum(stat.S_ISREG(r['mode']) for r in records.values()),
        directories=sum(stat.S_ISDIR(r['mode']) for r in records.values()),
        symlinks=sum(stat.S_ISLNK(r['mode']) for r in records.values()),
        devices=sum(stat.S_ISCHR(r['mode']) or stat.S_ISBLK(r['mode']) for r in records.values()),
        with_xattrs=sum(bool(r['xattrs']) for r in records.values()),
        hardlink_groups=len({tuple(r['hardlinks']) for r in records.values() if 'hardlinks' in r}),
    ), sort_keys=True))
    return records


def inspect(image, allow_generated_times, generated_paths):
    subprocess.run(['e2fsck', '-fn', image], check=True)
    print('du:', subprocess.check_output(['du', '-B1', image], text=True).strip())
    print('du apparent:', subprocess.check_output(['du', '-B1', '--apparent-size', image], text=True).strip())
    with tempfile.TemporaryDirectory(prefix='ociconv-mount-') as d:
        subprocess.run(['mount', '-o', 'loop,ro', image, d], check=True)
        try:
            return tree(Path(d), allow_generated_times, generated_paths)
        finally:
            subprocess.run(['umount', d], check=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-generated-times', action='store_true',
                        help='report but exclude generated root and empty lost+found mtimes')
    parser.add_argument('--generated-mtime-paths', type=Path,
                        help='explicit directory paths whose mtime variation was confirmed by baseline-vs-baseline comparison')
    parser.add_argument('images', nargs='+')
    args = parser.parse_args()
    generated_paths = args.generated_mtime_paths.read_text().splitlines() if args.generated_mtime_paths else []
    baseline = inspect(args.images[0], args.allow_generated_times, generated_paths)
    failed = False
    for image in args.images[1:]:
        variant = inspect(image, args.allow_generated_times, generated_paths)
        differences = {k: [baseline.get(k), variant.get(k)]
                       for k in baseline.keys() | variant.keys()
                       if baseline.get(k) != variant.get(k)}
        print(image, 'entries:', len(variant), 'differences:', len(differences))
        if differences:
            print(json.dumps(differences, indent=2, sort_keys=True))
            failed = True
    sys.exit(1 if failed else 0)
