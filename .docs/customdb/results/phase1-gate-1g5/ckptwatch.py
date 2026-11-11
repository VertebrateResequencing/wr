#!/usr/bin/env python3
"""Phase 1 gate item 1g.5: record when the log store writes each checkpoint.

usage: ckptwatch.py <storedir> <out.tsv> <stopfile>

Watches <storedir> with inotify (the manager runs on this host, so its own
creates, closes and renames on NFS raise local events) until <stopfile>
exists. A checkpoint rotates the wal (a new NNNNNNNN.wal is created), then
creates NNNNNNNN.ckpt.tmp, writes it through one buffered writer, fsyncs and
closes it, and renames it to NNNNNNNN.ckpt (spec "Checkpoints"), so the .tmp's
create-to-rename window brackets the unpaced write and its fsync. Writes one
line per event: epoch seconds (ms), event (create, close_write, moved_to,
delete), name, and the file's size in bytes for close_write and moved_to.
"""
import ctypes
import os
import select
import struct
import sys
import time

IN_CLOSE_WRITE, IN_MOVED_TO, IN_CREATE, IN_DELETE = 0x8, 0x80, 0x100, 0x200
NAMES = {IN_CLOSE_WRITE: "close_write", IN_MOVED_TO: "moved_to", IN_CREATE: "create", IN_DELETE: "delete"}

storedir, outpath, stopfile = sys.argv[1:4]
libc = ctypes.CDLL("libc.so.6", use_errno=True)
while not os.path.isdir(storedir):
    if os.path.exists(stopfile):
        sys.exit(0)
    time.sleep(1)
fd = libc.inotify_init1(os.O_NONBLOCK)
if fd < 0 or libc.inotify_add_watch(fd, storedir.encode(), IN_CLOSE_WRITE | IN_MOVED_TO | IN_CREATE | IN_DELETE) < 0:
    sys.exit(f"inotify failed: errno {ctypes.get_errno()}")
with open(outpath, "a", buffering=1) as out:
    out.write("epoch\tevent\tname\tbytes\n")
    for n in sorted(os.listdir(storedir)):
        if ".ckpt" in n:
            out.write(f"{time.time():.3f}\texisting\t{n}\t{os.stat(os.path.join(storedir, n)).st_size}\n")
    while not os.path.exists(stopfile):
        r, _, _ = select.select([fd], [], [], 1.0)
        if not r:
            continue
        now = time.time()
        try:
            buf = os.read(fd, 65536)
        except BlockingIOError:
            continue
        i = 0
        while i < len(buf):
            _, mask, _, ln = struct.unpack_from("iIII", buf, i)
            name = buf[i + 16 : i + 16 + ln].rstrip(b"\0").decode()
            i += 16 + ln
            ev = next((v for k, v in NAMES.items() if mask & k), None)
            if ev is None or not (".ckpt" in name or name.endswith(".wal")):
                continue
            size = "-"
            if ev in ("close_write", "moved_to"):
                try:
                    size = os.stat(os.path.join(storedir, name)).st_size
                except OSError:
                    size = -1
            out.write(f"{now:.3f}\t{ev}\t{name}\t{size}\n")
