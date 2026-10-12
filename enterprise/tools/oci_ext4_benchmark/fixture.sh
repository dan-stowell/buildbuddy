#!/usr/bin/env bash
# Run as root. Creates an archive with metadata absent from most base images.
set -euo pipefail
fixture_root="${1:?usage: fixture.sh DIRECTORY TAR_PATH}"
fixture_tar="${2:?}"
mkdir -p "$fixture_root/dir" "$fixture_root/dev"
printf 'fixture contents\n' > "$fixture_root/dir/file"
chown 123:456 "$fixture_root/dir/file"
chmod 6750 "$fixture_root/dir/file"
ln "$fixture_root/dir/file" "$fixture_root/dir/hard"
ln -s ../dir/file "$fixture_root/symlink"
mknod "$fixture_root/dev/null" c 1 3
mknod "$fixture_root/dev/block" b 8 1
mkfifo "$fixture_root/fifo"
setfattr -n user.example -v fixture "$fixture_root/dir/file"
# A real Linux capability xattr, encoded by setcap rather than arbitrary bytes.
setcap cap_net_bind_service=ep "$fixture_root/dir/file"
python3 - "$fixture_root" <<'PY'
import os, sys
r = sys.argv[1]
with open(r + '/sparse', 'wb') as f:
    f.write(b'begin')
    f.seek(32*1024*1024-3)
    f.write(b'end')
long = 'a'*200
os.mkdir(r + '/' + long)
with open(r + '/' + long + '/' + 'b'*200, 'wb') as f:
    f.write(b'long name')
for directory, dirs, files in os.walk(r):
    for name in dirs + files:
        os.utime(os.path.join(directory, name), ns=(1600000000123456789,)*2, follow_symlinks=False)
os.utime(r, ns=(1600000000123456789,)*2)
PY
tar --format=pax --sparse --xattrs --xattrs-include='*' -cf "$fixture_tar" -C "$fixture_root" .
