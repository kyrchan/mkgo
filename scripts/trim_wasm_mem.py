#!/usr/bin/env python3
"""Trim a wasm module's declared initial memory pages to a small value.
The Go wasip1 runtime declares huge initial heaps (960MB–2.8GB) for hosted
VMs with virtual memory. The kernel's wasm3 engine has no virtual memory,
so we patch the initial down to `new_pages` (default 256 = 16 MiB) and
let vmod_grow_session resize it at spawn time.

Usage: trim_wasm_mem.py <input.wasm> <output.wasm> [new_pages=256]
"""
import struct, sys

def leb128_decode(data, off):
    result, shift = 0, 0
    while True:
        b = data[off]
        result |= (b & 0x7f) << shift
        off += 1
        if not (b & 0x80):
            break
        shift += 7
    return result, off

def leb128_encode(n):
    out = bytearray()
    while True:
        b = n & 0x7f
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            break
    return bytes(out)

def patch(data, new_pages=256):
    MAGIC = b'\x00asm'
    if data[:4] != MAGIC:
        raise ValueError("not a wasm module")
    ver = struct.unpack('<I', data[4:8])[0]
    
    out = bytearray(data[:8])  # magic + version
    off = 8
    
    while off < len(data):
        sec_id = data[off]; off += 1
        size, off = leb128_decode(data, off)
        sec_start = off
        sec_end = off + size
        
        if sec_id == 5:  # memory section
            # The memory section contains: count (LEB), then repeated:
            #   limits_flags (1 byte), initial (LEB), max (LEB, only if flags & 0x1)
            count = 1  # we assume single memory (standard for Go)
            flags = data[sec_start + 1]  # byte after count
            initial = leb128_decode(data, sec_start + 2)[0]
            new_init_leb = leb128_encode(new_pages)
            
            # Build patched content
            patched = bytearray()
            patched.append(data[sec_start])  # count
            patched.append(flags)
            patched.extend(new_init_leb)
            if flags & 0x1:  # has max
                _, idx = leb128_decode(data, sec_start + 2)
                max_val, _ = leb128_decode(data, idx)
                patched.extend(leb128_encode(max_val))
            # Copy remaining bytes after the max field
            if flags & 0x1:
                _, idx = leb128_decode(data, sec_start + 2)
                _, idx2 = leb128_encode(data, sec_start + 2)  # just to find offset
                _, idx3 = leb128_decode(data, sec_start + 2)
                max_leb_end = idx3
                patched.extend(data[sec_start + 1 + 1 + len(leb128_encode(initial)) + len(leb128_encode(max_val)):])
            else:
                patched.extend(data[sec_start + 1 + 1 + len(leb128_encode(initial)):])
            
            # Recalculate: build from scratch
            patched = bytearray()
            patched.append(data[sec_start])  # count=1
            patched.append(flags)
            patched.extend(new_init_leb)
            # Copy max if present
            pos = sec_start + 1  # skip count
            _ = data[pos]; pos += 1  # flags
            _, pos = leb128_decode(data, pos)  # skip initial
            if flags & 0x1:
                _, pos = leb128_decode(data, pos)  # skip max
                # Copy max value
                max_val, _ = leb128_decode(data, pos - len(leb128_encode(0)) if False else pos)
                # Actually re-copy from original
                pos2 = sec_start + 1
                _ = data[pos2]; pos2 += 1
                _, pos2 = leb128_decode(data, pos2)
                if pos2 < sec_end:
                    patched.extend(data[pos2:sec_end])
            
            out.append(sec_id)
            out.extend(leb128_encode(len(patched)))
            out.extend(patched)
            print(f"Memory section: initial {initial} -> {new_pages} pages ({initial*64}K -> {new_pages*64}K)")
        else:
            out.append(sec_id)
            out.extend(leb128_encode(size))
            out.extend(data[sec_start:sec_end])
        off = sec_end
    
    return bytes(out)

def main():
    if len(sys.argv) < 3:
        print("Usage: trim_wasm_mem.py <input.wasm> <output.wasm> [new_pages=256]")
        sys.exit(1)
    inp, outp = sys.argv[1], sys.argv[2]
    new_pages = int(sys.argv[3]) if len(sys.argv) > 3 else 256
    
    with open(inp, 'rb') as f:
        data = f.read()
    
    patched = patch(data, new_pages)
    
    with open(outp, 'wb') as f:
        f.write(patched)
    
    print(f"Written {len(patched)} bytes to {outp}")

if __name__ == '__main__':
    main()
