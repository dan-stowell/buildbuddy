#!/usr/bin/env python3
"""Compare mounted ext4 trees without following symlinks or reading devices."""
import hashlib
import json
import os
import stat
import sys


def tree(root):
    result = {}
    groups = {}
    for directory, dirs, files in os.walk(root, followlinks=False):
        for name in dirs + files:
            p = os.path.join(directory, name)
            rel = os.path.relpath(p, root)
            s = os.lstat(p)
            item = dict(mode=oct(s.st_mode), uid=s.st_uid, gid=s.st_gid,
                        mtime_ns=s.st_mtime_ns, xattrs={})
            for attr in os.listxattr(p, follow_symlinks=False):
                item['xattrs'][attr] = os.getxattr(p, attr, follow_symlinks=False).hex()
            if stat.S_ISREG(s.st_mode):
                h = hashlib.sha256()
                with open(p, 'rb') as f:
                    for b in iter(lambda: f.read(1024*1024), b''):
                        h.update(b)
                item.update(size=s.st_size, sha256=h.hexdigest())
                groups.setdefault(s.st_ino, []).append(rel)
            elif stat.S_ISLNK(s.st_mode):
                item['target'] = os.readlink(p)
            elif stat.S_ISCHR(s.st_mode) or stat.S_ISBLK(s.st_mode):
                item.update(major=os.major(s.st_rdev), minor=os.minor(s.st_rdev))
            result[rel] = item
    for paths in groups.values():
        for p in paths:
            result[p]['hardlinks'] = sorted(paths)
    # Root and lost+found have conversion-time metadata, independent of OCI.
    # Still compare permissions/ownership/xattrs; report mtime separately.
    s = os.lstat(root)
    result['.'] = dict(mode=oct(s.st_mode), uid=s.st_uid, gid=s.st_gid,
                       mtime_ns=s.st_mtime_ns,
                       xattrs={k: os.getxattr(root, k).hex() for k in os.listxattr(root)})
    return result


if __name__ == '__main__':
    a, b = map(tree, sys.argv[1:3])
    differences = []
    generated = []
    for p in sorted(a.keys() | b.keys()):
        x, y = a.get(p), b.get(p)
        if p in ('.', 'lost+found') and x and y:
            if x.get('mtime_ns') != y.get('mtime_ns'):
                generated.append(dict(path=p, baseline=x['mtime_ns'], candidate=y['mtime_ns']))
            x.pop('mtime_ns', None)
            y.pop('mtime_ns', None)
        if x != y:
            differences.append(dict(path=p, baseline=x, candidate=y))
    print(json.dumps(dict(baseline_entries=len(a), candidate_entries=len(b),
                          generated_mtimes=generated, differences=differences), indent=2))
    sys.exit(bool(differences))
