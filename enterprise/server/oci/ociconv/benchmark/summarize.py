#!/usr/bin/env python3
"""Summarize OCICONV_IO lines from the opt-in test (means of >=3 runs)."""
import collections
import json
import statistics
import sys

rows = collections.defaultdict(list)
for path in sys.argv[1:]:
    for line in open(path):
        if line.startswith('OCICONV_IO ') or line.startswith('{'):
            r = json.loads(line.removeprefix('OCICONV_IO '))
            rows[r['image'], r.get('fetch_mode', 'direct') + ('-warm' if r.get('warm_layers') else ''), r['config']].append(r)
print('| Image | Fetch | Config | n | Written bytes | Read bytes | Cancelled bytes | Seconds | Apparent bytes | Allocated bytes |')
print('|---|---|---|---:|---:|---:|---:|---:|---:|---:|')
for (image, fetch_mode, config), rs in rows.items():
    mean = lambda f: statistics.mean(f(r) for r in rs)
    print(f"| {image} | {fetch_mode} | {config} | {len(rs)} | {mean(lambda r: r['total']['write_bytes']):.0f} | {mean(lambda r: r['total']['read_bytes']):.0f} | {mean(lambda r: r['total']['cancelled_write_bytes']):.0f} | {mean(lambda r: r['seconds']):.3f} | {mean(lambda r: r['apparent_bytes']):.0f} | {mean(lambda r: r['allocated_bytes']):.0f} |")
print('\n| Image | Fetch | Config | Phase | Written bytes | Read bytes | Cancelled bytes | Seconds | Read syscalls | Write syscalls |')
print('|---|---|---|---|---:|---:|---:|---:|---:|---:|')
for (image, fetch_mode, config), rs in rows.items():
    for i, phase in enumerate(rs[0]['phases']):
        ps = [r['phases'][i] for r in rs]
        mean = lambda f: statistics.mean(f(p) for p in ps)
        print(f"| {image} | {fetch_mode} | {config} | {phase['phase']} | {mean(lambda p: p['io']['write_bytes']):.0f} | {mean(lambda p: p['io']['read_bytes']):.0f} | {mean(lambda p: p['io']['cancelled_write_bytes']):.0f} | {mean(lambda p: p['seconds']):.3f} | {mean(lambda p: p['io']['read_syscalls']):.0f} | {mean(lambda p: p['io']['write_syscalls']):.0f} |")
