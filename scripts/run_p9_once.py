#!/usr/bin/env python3
"""Deterministic p9 gate runner: in-process UDP echo + HTTP server,
QEMU subprocess, fixed window, prints [p9] lines from serial."""
import socket, subprocess, time, os, sys, threading

env = dict(os.environ, LD_LIBRARY_PATH="/home/cyr/.local/osdev-root/usr/lib/x86_64-linux-gnu")
QEMU = "/home/cyr/.local/osdev-root/usr/bin/qemu-system-x86_64"
D = "/tmp/opencode/p9srv"
os.makedirs(D, exist_ok=True)
open(D + "/hello.txt", "w").write("P9-HTTP-BODY welcome\n")

stop = threading.Event()

def udp_echo():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    s.bind(("127.0.0.1", 5599)); s.settimeout(0.5)
    while not stop.is_set():
        try:
            data, addr = s.recvfrom(2048)
            s.sendto(data, addr)
        except socket.timeout:
            continue
    s.close()

def http_srv():
    import http.server, functools, socketserver
    handler = functools.partial(http.server.SimpleHTTPRequestHandler,
                                directory=D)
    socketserver.TCPServer.allow_reuse_address = True
    try:
        with socketserver.TCPServer(("127.0.0.1", 8000), handler) as h:
            h.timeout = 1
            while not stop.is_set():
                h.handle_request()
    except Exception:
        pass

threading.Thread(target=udp_echo, daemon=True).start()
threading.Thread(target=http_srv, daemon=True).start()
time.sleep(0.5)

sockf = "/tmp/opencode/p9mon.sock"
if os.path.exists(sockf): os.unlink(sockf)
logf = "/tmp/opencode/p9serial.log"
if os.path.exists(logf): os.unlink(logf)

args = [QEMU,
 "-drive", "format=raw,file=build/disk-p9.img",
 "-L", "/home/cyr/.local/osdev-root/usr/share/qemu",
 "-L", "/home/cyr/.local/osdev-root/usr/share/seabios",
 "-machine", "q35", "-cpu", "max", "-m", "512", "-accel", "tcg",
 "-drive", "if=pflash,format=raw,readonly=on,file=/home/cyr/.local/osdev-root/usr/share/OVMF/OVMF_CODE_4M.fd",
 "-drive", "if=pflash,format=raw,file=build/VARS.fd",
 "-display", "none", "-no-reboot",
 "-netdev", "user,id=n1", "-device", "virtio-net-pci,netdev=n1",
            "-object", "filter-dump,id=f1,netdev=n1,file=/tmp/opencode/p93.pcap",
 "-serial", f"file:{logf}",
 "-monitor", f"unix:{sockf},server,nowait"]
p = subprocess.Popen(args, env=env, stdout=subprocess.DEVNULL,
                     stderr=subprocess.DEVNULL)
window = float(sys.argv[1]) if len(sys.argv) > 1 else 75.0
end = time.time() + window
ok = False
txt = ""
while time.time() < end:
    if os.path.exists(logf):
        txt = open(logf, errors="replace").read()
        if "[p9] udp ok" in txt and "[p9] http ok" in txt:
            ok = True
            break
    time.sleep(1)
stop.set(); time.sleep(0.4)
p.terminate(); time.sleep(1)
try: p.kill()
except Exception: pass
for line in txt.splitlines():
    if "[p9]" in line or "netwin" in line or "modern ready" in line:
        print(line)
print("GATE:", "PASS" if ("[p9] udp ok" in txt and "[p9] http ok" in txt) else "FAIL")
