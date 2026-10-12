#!/usr/bin/env python3
"""Render the committed JSONL measurements as Markdown tables."""
import json
from pathlib import Path
import statistics

root = Path(__file__).parent / 'measurements'
configs = ['baseline', 'lazy', 'nodiscard', 'lazy-nodiscard', 'small-journal', 'no-journal',
           'compact', 'inodes', 'tmpfs', 'prezeroed', 'tmpfs-prezeroed', 'no-fsync', 'tar-no-same']
rows = []
for image in ['alpine', 'ubuntu', 'rbe']:
    data = [json.loads(l) for p in [root/(image+'.jsonl'), root/(image+'-tar-no-same.jsonl')]
            for l in p.read_text().splitlines()]
    rows.extend(data)
    print('\n### ' + image + '\n')
    print('| Layout | Written MiB | Cancelled MiB | Net written MiB | Read MiB | Seconds | Apparent MiB | Allocated MiB |')
    print('| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |')
    for config in configs:
        r = [x for x in data if x['Config'] == config]
        assert len(r) == 3
        io = lambda key: statistics.mean(x['Conversion']['io'][key]/(1<<20) for x in r)
        values = [io(k) for k in ['write_bytes', 'cancelled_write_bytes', 'net_write_bytes', 'read_bytes']]
        values += [statistics.mean(x['Conversion']['seconds'] for x in r),
                   statistics.mean(x['Apparent']/(1<<20) for x in r), statistics.mean(x['Allocated']/(1<<20) for x in r)]
        print('| ' + config + ' | ' + ' | '.join(f'{v:.3f}' for v in values) + ' |')
print('\n### Phase IO and wall time\n')
print('Each cell is **net written MiB / read MiB / seconds**, averaged over three runs.\n')
print('| Image / layout | Unpack | Estimate | Mkfs | Link | Cleanup | Conversion sync |')
print('| --- | ---: | ---: | ---: | ---: | ---: | ---: |')
for image in ['alpine', 'ubuntu', 'rbe']:
    for config in ['baseline', 'prezeroed', 'tmpfs-prezeroed']:
        r = [x for x in rows if x['Image'] == image and x['Config'] == config]
        values = []
        for phase in ['unpack', 'estimate', 'mke2fs', 'filecache-hardlink', 'cleanup', 'conversion-sync']:
            p = [next(p for p in x['Phases'] if p['name'] == phase) for x in r]
            values.append(' / '.join(f'{v:.3f}' for v in [statistics.mean(x['io']['net_write_bytes']/(1<<20) for x in p),
                                                        statistics.mean(x['io']['read_bytes']/(1<<20) for x in p),
                                                        statistics.mean(x['seconds'] for x in p)]))
        print('| ' + image + ' / ' + config + ' | ' + ' | '.join(values) + ' |')
print('\n### Conversion plus sparse chunk creation\n')
print('| Image / layout | Total written MiB | Total cancelled MiB | Total net MiB | Total read MiB | Seconds | Chunk allocated MiB |')
print('| --- | ---: | ---: | ---: | ---: | ---: | ---: |')
for image in ['alpine', 'ubuntu', 'rbe']:
    for config in configs[:-1]:
        r = [x for x in rows if x['Image'] == image and x['Config'] == config]
        values = [statistics.mean(x['Total']['io'][k]/(1<<20) for x in r) for k in ['write_bytes', 'cancelled_write_bytes', 'net_write_bytes', 'read_bytes']]
        values += [statistics.mean(x['Total']['seconds'] for x in r), statistics.mean(x['ChunkAllocated']/(1<<20) for x in r)]
        print('| ' + image + ' / ' + config + ' | ' + ' | '.join(f'{v:.3f}' for v in values) + ' |')
