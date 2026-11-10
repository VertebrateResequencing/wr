#!/usr/bin/env python3
"""walbacklog.py <storedir>: the log store's unfolded wal backlog, read without
any lock. Reads history.bolt's newest valid meta page, its root bucket leaf and
the inline "meta" bucket's "folded" key (wal file u32 BE, offset u64 BE), then
sums the wal bytes after that position. Read-only; tolerant of a concurrent
writer (retries a few times on an inconsistent read). 1g.3 evidence tooling.

Prints: folded=<file>:<off> end=<file>:<size> backlogBytes=<n> walFiles=<k>
"""
import os, re, struct, sys, time

def leaf_get(buf, base, key):
    flags, count = struct.unpack_from('<HH', buf, base + 8)
    if not flags & 0x02:
        raise ValueError('not a leaf page (flags %#x)' % flags)
    for i in range(count):
        e = base + 16 + i * 16
        fl, pos, ks, vs = struct.unpack_from('<IIII', buf, e)
        k = buf[e + pos:e + pos + ks]
        if k == key:
            return fl, buf[e + pos + ks:e + pos + ks + vs]
    return None, None

def read_folded(path):
    with open(path, 'rb') as f:
        m0 = f.read(4096)
        ps = struct.unpack_from('<I', m0, 24)[0]
        f.seek(ps); m1 = f.read(ps)
        metas = [m for m in (m0, m1) if struct.unpack_from('<I', m, 16)[0] == 0xED0CDAED]
        best = max(metas, key=lambda m: struct.unpack_from('<Q', m, 64)[0])
        root = struct.unpack_from('<Q', best, 32)[0]
        f.seek(root * ps); page = f.read(ps)
        overflow = struct.unpack_from('<I', page, 12)[0]
        if overflow:
            f.seek(root * ps); page = f.read(ps * (overflow + 1))
    fl, v = leaf_get(page, 0, b'meta')
    if v is None or not fl & 0x01:
        raise ValueError('no meta bucket in root leaf')
    broot = struct.unpack_from('<Q', v, 0)[0]
    if broot != 0:
        with open(path, 'rb') as f:
            f.seek(broot * ps); bp = f.read(ps)
        _, fv = leaf_get(bp, 0, b'folded')
    else:
        _, fv = leaf_get(v, 16, b'folded')
    if fv is None or len(fv) != 12:
        raise ValueError('no folded key')
    return struct.unpack('>IQ', fv)

def main():
    d = sys.argv[1]
    err = None
    for _ in range(5):
        try:
            ffile, foff = read_folded(os.path.join(d, 'history.bolt'))
            break
        except Exception as e:  # concurrent writer: try again
            err = e; time.sleep(0.2)
    else:
        print('error=%s' % str(err).replace(' ', '_')); return 1
    wals = sorted((int(n[:8], 16), os.path.getsize(os.path.join(d, n)))
                  for n in os.listdir(d) if re.fullmatch(r'[0-9a-f]{8}\.wal', n))
    backlog = 0
    for n, sz in wals:
        if n > ffile:
            backlog += sz
        elif n == ffile:
            backlog += max(0, sz - foff)
    end = '%d:%d' % wals[-1] if wals else '-'
    print('folded=%d:%d end=%s backlogBytes=%d walFiles=%d' % (ffile, foff, end, backlog, len(wals)))
    return 0

sys.exit(main())
