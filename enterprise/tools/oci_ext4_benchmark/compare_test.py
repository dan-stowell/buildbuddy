"""Hardlink comparison must include symlinks and unreadable special files."""
import os
import tempfile
import unittest

from compare import tree


class CompareTest(unittest.TestCase):
    def test_non_regular_hardlinks(self):
        with tempfile.TemporaryDirectory() as root:
            os.symlink('missing', os.path.join(root, 'sym'))
            os.mkfifo(os.path.join(root, 'fifo'))
            for name in ('sym', 'fifo'):
                os.link(os.path.join(root, name), os.path.join(root, name + '-link'),
                        follow_symlinks=False)
            linked = tree(root)  # Would hang if the comparator tried reading the FIFO.
            for name in ('sym', 'fifo'):
                self.assertEqual([name, name + '-link'], linked[name]['hardlinks'])
                self.assertEqual(linked[name]['hardlinks'], linked[name + '-link']['hardlinks'])
                os.unlink(os.path.join(root, name + '-link'))
                if name == 'sym':
                    os.symlink('missing', os.path.join(root, name + '-link'))
                else:
                    os.mkfifo(os.path.join(root, name + '-link'))
                # Equal metadata and targets cannot hide broken inode relationships.
                os.utime(os.path.join(root, name + '-link'),
                         ns=(0, os.lstat(os.path.join(root, name)).st_mtime_ns),
                         follow_symlinks=False)
            independent = tree(root)
            for name in ('sym', 'fifo'):
                self.assertEqual([name], independent[name]['hardlinks'])
                self.assertNotEqual(linked[name], independent[name])
                a, b = dict(linked[name]), dict(independent[name])
                a.pop('hardlinks')
                b.pop('hardlinks')
                self.assertEqual(a, b)


if __name__ == '__main__':
    unittest.main()
