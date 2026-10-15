#!/usr/bin/env python3
"""Append an `abi_ver` custom section (payload = version byte) to a wasm
module. Custom section body = name_len_LEB + name + payload; section size
covers all three."""
import sys


def leb(n):
    out = bytearray()
    while True:
        b7 = n & 0x7F
        n >>= 7
        out.append(b7 | (0x80 if n else 0))
        if not n:
            return bytes(out)


def main(src, dst, ver=1):
    d = bytearray(open(src, "rb").read())
    if d[:4] != b"\0asm":
        sys.exit("not a wasm module")
    name = b"abi_ver"
    body = leb(len(name)) + name + bytes([ver])
    open(dst, "wb").write(bytes(d) + bytes([0]) + leb(len(body)) + body)
    print(f"abiver: {dst} ver={ver}")


if __name__ == "__main__":
    if len(sys.argv) not in (3, 4):
        sys.exit("usage: add_abiver.py in.wasm out.wasm [ver]")
    main(sys.argv[1], sys.argv[2], int(sys.argv[3]) if len(sys.argv) == 4 else 1)
