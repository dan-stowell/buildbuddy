#!/usr/bin/env python3
"""Print median totals and per-phase results from benchmark JSONL files."""
import collections
import json
import statistics
import sys

rows = collections.defaultdict(list)
for filename in sys.argv[1:]:
    with open(filename) as f:
        for line in f:
            r = json.loads(line)
            if 'Variant' in r:
                rows[(r['Image'], r['Variant'])].append(r)

def median(rs, fn):
    return statistics.median(fn(r) for r in rs)

print('| Image | Variant | Runs | Written MiB | Read MiB | Cancelled MiB | Seconds | Apparent MiB | Allocated MiB | Write syscalls | Result |')
print('|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---|')
for (image, variant), rs in rows.items():
    m = lambda fn: median(rs, fn)
    print(f'| {image} | {variant} | {len(rs)} | '
          f'{m(lambda r: r["Total"]["io"]["write_bytes"])/2**20:.2f} | '
          f'{m(lambda r: r["Total"]["io"]["read_bytes"])/2**20:.2f} | '
          f'{m(lambda r: r["Total"]["io"]["cancelled_write_bytes"])/2**20:.2f} | '
          f'{m(lambda r: r["Total"]["seconds"]):.3f} | '
          f'{m(lambda r: r["Apparent"])/2**20:.2f} | '
          f'{m(lambda r: r["Allocated"])/2**20:.2f} | '
          f'{m(lambda r: r["Total"]["io"]["syscw"]):.0f} | '
          f'{"ERROR" if any(r.get("error") for r in rs) else "converted"} |')

print('\n| Image | Variant | Phase | Written MiB | Read MiB | Cancelled MiB | Seconds |')
print('|---|---|---|---:|---:|---:|---:|')
for (image, variant), rs in rows.items():
    phases = collections.defaultdict(list)
    for r in rs:
        for p in r['Phases']:
            phases[p['phase']].append(p)
    for name, ps in phases.items():
        m = lambda fn: median(ps, fn)
        print(f'| {image} | {variant} | {name} | '
              f'{m(lambda p: p["io"]["write_bytes"])/2**20:.2f} | '
              f'{m(lambda p: p["io"]["read_bytes"])/2**20:.2f} | '
              f'{m(lambda p: p["io"]["cancelled_write_bytes"])/2**20:.2f} | '
              f'{m(lambda p: p["seconds"]):.3f} |')
