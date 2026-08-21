#!/usr/bin/env python3
"""Build the final PE32+ EFI application.

Two-source merge:
  * shim ELF  (C firmware handshake, linked at IMAGE_BASE)
  * Go kernel raw binary (objcopy -O binary of kernel.elf) placed at GO_BASE

The combined image is position-fixed (RELOCS_STRIPPED): OVMF must load it at
the preferred ImageBase, which is free at boot. The shim's placeholder magic
constants are patched with the Go image's real entry/end addresses.
"""
import os
import struct
import sys

IMAGE_BASE = 0x100000
SECT_ALIGN = 4096
FILE_ALIGN = 512
SUBSYSTEM = 10

GO_ENTRY_MAGIC = (0xB10B1A7C0FFEE001).to_bytes(8, "little")
GO_END_MAGIC = (0xB10B1A7C0FFEE002).to_bytes(8, "little")

R_ABS64_TYPES = {1, 6, 7}
R_X86_64_RELATIVE = 8


def align(x, a):
    return (x + a - 1) & ~(a - 1)


def elf_parts(path):
    d = open(path, "rb").read()
    (_t, _m, _v, e_entry, _p, e_shoff, _f, _h, _pe, _pn,
     e_shentsize, e_shnum, e_shstrndx) = struct.unpack_from("<HHIQQQIHHHHHH", d, 16)

    def shdr(i):
        o = e_shoff + i * e_shentsize
        f = struct.unpack_from("<IIQQQQIIQQ", d, o)
        keys = ("name", "type", "flags", "addr", "off", "size",
                "link", "info", "align", "entsize")
        return dict(zip(keys, f))

    stro = shdr(e_shstrndx)["off"]

    def sname(n):
        e = d.index(b"\0", stro + n)
        return d[stro + n:e].decode()

    skip = (".dynamic", ".rela", ".rel", ".dynsym", ".dynstr", ".hash",
            ".gnu.hash", ".comment", ".note", ".reloc", ".tbss", ".tdata")
    groups = {"text": [], "rodata": [], "data": []}
    relas, dynsym = [], None

    for i in range(1, e_shnum):
        s = shdr(i)
        nm = sname(s["name"])
        if nm == ".dynsym":
            dynsym = d[s["off"]:s["off"] + s["size"]]
        elif s["type"] == 4:  # SHT_RELA
            for j in range(s["size"] // 24):
                relas.append(struct.unpack_from("<QQq", d, s["off"] + j * 24))
        elif not (s["flags"] & 0x2) or s["addr"] == 0:
            continue
        elif nm.startswith(skip):
            continue
        elif nm.startswith(".text"):
            groups["text"].append((nm, s))
        elif nm.startswith(".rodata"):
            groups["rodata"].append((nm, s))
        else:
            groups["data"].append((nm, s))

    def sym_value(idx):
        if not dynsym:
            return None
        _n, _i, _o, shndx, value, _sz = struct.unpack_from("<IBBHQQ", dynsym, idx * 24)
        return None if shndx == 0 else value

    relocs = []
    for off, info, add in relas:
        t = info & 0xFFFFFFFF
        if t == R_X86_64_RELATIVE:
            relocs.append((off, add))
        elif t in R_ABS64_TYPES:
            sv = sym_value(info >> 32)
            if sv is None:
                sys.exit(f"undefined symbol reloc at {off:#x}")
            relocs.append((off, sv + add))
        else:
            sys.exit(f"unsupported reloc type {t}")

    def blob(items):
        items.sort(key=lambda kv: kv[1]["addr"])
        base = items[0][1]["addr"]
        out = bytearray()
        pos = base
        for nm, s in items:
            out += b"\0" * (s["addr"] - pos)
            if s["type"] == 8:  # NOBITS
                out += b"\0" * s["size"]
            else:
                out += d[s["off"]:s["off"] + s["size"]]
            pos = s["addr"] + s["size"]
        return base, bytes(out)

    secs = []
    for cls, pname, pchar in (("text", ".text", 0x60000020),
                              ("rodata", ".rodata", 0x40000040),
                              ("data", ".data", 0xC0000040)):
        if groups[cls]:
            va, data = blob(groups[cls])
            secs.append({"name": pname, "va": va, "data": data, "char": pchar})
    return secs, e_entry, relocs


def load_elf_image(path):
    """Return (base, image) covering all PT_LOAD segments (BSS zeroed)."""
    d = open(path, "rb").read()
    (_t, _m, _v, _e, e_phoff, _s, _f, _h, e_phentsize, e_phnum,
     _ss, _sn, _sx) = struct.unpack_from("<HHIQQQIHHHHHH", d, 16)
    lo, hi = None, 0
    segs = []
    for i in range(e_phnum):
        o = e_phoff + i * e_phentsize
        ptype, _flags, poff, pvaddr, _pa, pfilesz, pmemsz, _al = \
            struct.unpack_from("<IIQQQQQQ", d, o)
        if ptype != 1:  # PT_LOAD
            continue
        segs.append((poff, pvaddr, pfilesz, pmemsz))
        if lo is None or pvaddr < lo:
            lo = pvaddr
        end = pvaddr + pmemsz
        if end > hi:
            hi = end
    img = bytearray(hi - lo)
    for poff, pvaddr, pfilesz, pmemsz in segs:
        img[pvaddr - lo:pvaddr - lo + pfilesz] = d[poff:poff + pfilesz]
    return lo, bytes(img)


def main(shim_elf, go_bin, go_base, go_entry, go_end, dst):
    go_base = int(str(go_base), 0)
    go_entry = int(str(go_entry), 0)
    go_end = int(str(go_end), 0)
    go_lo, go_image = load_elf_image(go_bin)
    if go_lo != go_base:
        print(f"mkpefi: note: go image base is {go_lo:#x} (arg said {go_base})")

    secs, e_entry, relocs = elf_parts(shim_elf)

    # apply shim relocations as fixed absolute values (image is position-fixed)
    for off, add in relocs:
        done = False
        for s in secs:
            lo, hi = s["va"], s["va"] + len(s["data"])
            if lo <= off < hi:
                buf = bytearray(s["data"])
                struct.pack_into("<Q", buf, off - lo, add & (2**64 - 1))
                s["data"] = bytes(buf)
                done = True
                break
        if not done:
            sys.exit(f"reloc target {off:#x} outside shim image")

    # append the Go kernel as one big section
    go_rva = go_base - IMAGE_BASE
    if go_rva % SECT_ALIGN:
        sys.exit("go base not section aligned relative to image base")
    secs.append({"name": ".gokern", "va": go_base, "rva": go_rva,
                 "data": go_image, "char": 0x60000020})

    hdr_size = align(0x80 + 4 + 20 + 240 + 40 * len(secs), FILE_ALIGN)
    file_off = hdr_size
    for s in secs:
        s.setdefault("rva", s["va"] - IMAGE_BASE)
        s["vsize"] = len(s["data"])
        s["rawsz"] = len(s["data"])
        s["foff"] = file_off
        if s["rva"] < hdr_size and s["name"] != ".gokern":
            sys.exit(f"section {s['name']} rva {s['rva']:#x} under headers")
        file_off = align(file_off + s["rawsz"], FILE_ALIGN)
    size_of_image = align(max(s["rva"] + max(s["vsize"], 1) for s in secs), SECT_ALIGN)

    img = bytearray()
    dos = bytearray(b"\0" * 0x80)
    dos[0:2] = b"MZ"
    struct.pack_into("<I", dos, 0x3C, 0x80)
    stub = b"This program cannot be run in DOS mode.\r\r\n$"
    hdr = bytearray(dos + stub)[:0x80]
    hdr += b"\0" * (0x80 - len(hdr))
    hdr += b"PE\0\0"
    init = sum(align(s["rawsz"], FILE_ALIGN) for s in secs if s["char"] & 0x40)
    code = sum(s["vsize"] for s in secs if s["name"] in (".text", ".gokern"))
    hdr += struct.pack("<HHIIIHH", 0x8664, len(secs), 0, 0, 0, 240,
                       0x2022 | 1)  # RELOCS_STRIPPED: position-fixed image
    opt = bytearray(240)
    struct.pack_into("<H", opt, 0, 0x20B)
    struct.pack_into("<III", opt, 4, code, init, 0)
    struct.pack_into("<II", opt, 16, e_entry - IMAGE_BASE, secs[0]["rva"])
    struct.pack_into("<Q", opt, 24, IMAGE_BASE)
    struct.pack_into("<II", opt, 32, SECT_ALIGN, FILE_ALIGN)
    struct.pack_into("<HHHH", opt, 40, 6, 0, 0, 0)
    struct.pack_into("<HH", opt, 48, 6, 0)
    struct.pack_into("<II", opt, 56, size_of_image, hdr_size)
    struct.pack_into("<HH", opt, 68, SUBSYSTEM, 0)
    struct.pack_into("<QQQQ", opt, 72, 0x100000, 0x1000, 0x100000, 0x1000)
    struct.pack_into("<II", opt, 104, 0, 16)
    hdr += opt
    for s in secs:
        nm = s["name"].encode() + b"\0" * (8 - len(s["name"]))
        hdr += nm + struct.pack("<IIIIII", s["vsize"], s["rva"], s["rawsz"],
                                s["foff"], 0, 0) + struct.pack("<HHI", 0, 0, s["char"])
    hdr += b"\0" * (hdr_size - len(hdr))

    img = bytearray(hdr)
    for s in secs:
        img += b"\0" * (s["foff"] - len(img))
        img += s["data"]

    # patch Go entry/end markers with real addresses
    n1 = img.count(GO_ENTRY_MAGIC)
    n2 = img.count(GO_END_MAGIC)
    if n1 != 1 or n2 != 1:
        sys.exit(f"marker count wrong: entry={n1} end={n2}")
    img = img.replace(GO_ENTRY_MAGIC, struct.pack("<Q", go_entry))
    img = img.replace(GO_END_MAGIC, struct.pack("<Q", go_end))

    open(dst, "wb").write(img)
    print(f"mkpefi: {dst} size={len(img)} entry={e_entry - IMAGE_BASE:#x} "
          f"go=[{go_base:#x},{go_end:#x}) sections={[s['name'] for s in secs]}")


if __name__ == "__main__":
    if len(sys.argv) != 7:
        sys.exit("usage: mkpefi.py shim.so go.bin go_base go_entry go_end out.efi")
    main(*sys.argv[1:])
