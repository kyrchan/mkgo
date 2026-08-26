#!/bin/bash
# scripts/run_p9.sh -- Phase 9 network gate.
#
# Boots the kernel with virtio-net attached to QEMU user-net and drives
# services/net end-to-end from a guest driver (guests/p9):
#   - UDP datagram echoed by a host server at 10.0.2.2:5599
#   - HTTP GET fetched from a host http.server at 10.0.2.2:8000
# Markers land on serial; make test-p9 greps them.
set -u
LOG="$1"; shift
QEMU_BIN="$1"; shift
QEMU_ENV_STR="$1"; shift
shift # separator
D=$(mktemp -d)
cleanup() {
    [ -n "${HTTP_PID:-}" ] && kill "$HTTP_PID" 2>/dev/null
    [ -n "${UDP_PID:-}" ] && kill "$UDP_PID" 2>/dev/null
    [ -n "${QPID:-}" ] && kill "$QPID" 2>/dev/null
}
trap cleanup EXIT

printf 'P9-HTTP-BODY welcome\n' > "$D/hello.txt"

python3 - "$D/hello.txt" > /dev/null 2>&1 <<'PYEOF' &
import sys, http.server, functools, socketserver
path = sys.argv[1]
handler = functools.partial(http.server.SimpleHTTPRequestHandler,
                            directory=path.rsplit('/', 1)[0])
socketserver.TCPServer.allow_reuse_address = True
with socketserver.TCPServer(("127.0.0.1", 8000), handler) as httpd:
    httpd.serve_forever()
PYEOF
HTTP_PID=$!

python3 - > /dev/null 2>&1 <<'PYEOF' &
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", 5599))
s.settimeout(1.0)
while True:
    try:
        data, addr = s.recvfrom(2048)
        s.sendto(data, addr)
    except socket.timeout:
        continue
PYEOF
UDP_PID=$!

sleep 1 # helpers listening

env "$QEMU_ENV_STR" "$QEMU_BIN" "$@" \
    -netdev user,id=n1 -device virtio-net-pci,netdev=n1 \
    -serial file:"$LOG" -display none -no-reboot &

# gate window: firmware + bring-up + full exchange (TCG is slow)
sleep 300
exit 0
