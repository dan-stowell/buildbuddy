#!/usr/bin/env python3
"""Extra metadata, writable mount, and guest-style online resize checks (root)."""
import fcntl
import os
from pathlib import Path
import struct
import subprocess
import sys

work = Path(sys.argv[1])
bench = sys.argv[2]
work.mkdir(parents=True, exist_ok=True)
source = work / 'metadata'
source.mkdir(exist_ok=True)
(source / 'file').write_bytes(b'metadata fixture\n')
os.chown(source / 'file', 1001, 1002)
os.chmod(source / 'file', 0o4751)
os.setxattr(source / 'file', 'user.test', b'user attribute')
os.setxattr(source / 'file', 'security.capability', struct.pack('<5I', 0x02000001, 1<<13, 0, 0, 0))
os.link(source / 'file', source / 'hardlink')
os.symlink('file', source / 'symlink')
os.symlink('absent', source / 'dangling')
os.lchown(source / 'symlink', 1003, 1004)
os.mkdir(source / 'dir', 0o1777)
os.setxattr(source / 'dir', 'user.directory', b'directory attribute')
os.mknod(source / 'char', 0o020640, os.makedev(1, 3))
os.mknod(source / 'block', 0o060640, os.makedev(7, 0))
os.mkfifo(source / 'fifo', 0o640)
for p in [source, *source.iterdir()]:
    os.utime(p, ns=(1600000000123456789, 1600000000987654321), follow_symlinks=False)

tmpfs = work / 'tmpfs-metadata'
tmpfs.mkdir()
subprocess.run(['mount', '-t', 'tmpfs', '-o', 'size=16m,nodev,nosuid,mode=0700', 'tmpfs', str(tmpfs)], check=True)
images = []
try:
    subprocess.run(['cp', '-a', str(source) + '/.', str(tmpfs)], check=True)
    for name, extra in [('baseline', []), ('prezeroed', ['-E', 'assume_storage_prezeroed=1,nodiscard']),
                        ('no-journal', ['-E', 'lazy_itable_init=1,nodiscard', '-O', '^64bit,^has_journal']),
                        ('tmpfs-prezeroed', ['-E', 'assume_storage_prezeroed=1,nodiscard'])]:
        image = work / (name + '.ext4')
        subprocess.run(['/sbin/mke2fs', '-q', '-N', '0', '-O', '^64bit', '-d', str(tmpfs if name == 'tmpfs-prezeroed' else source),
                        '-m', '5', '-r', '1', '-b', '4096', '-t', 'ext4', *extra,
                        str(image), '262144K'], check=True,
                       env=dict(os.environ, E2FSPROGS_FAKE_TIME='1700000000'))
        images.append(str(image))
finally:
    subprocess.run(['umount', str(tmpfs)], check=True)
subprocess.run([bench, '-compare', ','.join(images), '-work', str(work)], check=True)

mount = work / 'rw-mount'
mount.mkdir()
for image in images:
    loop = subprocess.check_output(['losetup', '--find', '--show', image], text=True).strip()
    try:
        subprocess.run(['mount', loop, str(mount)], check=True)
        try:
            (mount / 'written').write_bytes(b'writable guest root\n')
            with open(image, 'r+b') as f:
                f.truncate(512<<20)
            subprocess.run(['losetup', '-c', loop], check=True)
            fd = os.open(mount, os.O_RDONLY)
            try:
                try:
                    fcntl.ioctl(fd, 0x40086610, struct.pack('Q', (512<<20)//4096))
                    print(Path(image).name + ': writable mount and EXT4_IOC_RESIZE_FS succeeded', flush=True)
                except OSError as e:
                    print(Path(image).name + ': writable mount succeeded; EXT4_IOC_RESIZE_FS failed: ' + str(e), flush=True)
                    if Path(image).stem != 'no-journal':
                        raise
            finally:
                os.close(fd)
        finally:
            subprocess.run(['umount', str(mount)], check=True)
    finally:
        subprocess.run(['losetup', '-d', loop], check=True)
    subprocess.run(['/sbin/e2fsck', '-fn', image], check=True)
