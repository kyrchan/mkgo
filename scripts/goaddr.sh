#!/bin/bash
# Emit GO_ENTRY / GO_END assignments from the Go kernel ELF.
set -e
ELF="$1"
OUT="$2"
ENTRY=$(nm "$ELF" | awk '$3=="_rt0_amd64_baremetal" {print "0x"$1; exit}')
END=$(nm -n "$ELF" | awk 'NF>=3 {a=$1} END{print "0x"a}')
[ -n "$ENTRY" ] || { echo "entry symbol not found" >&2; exit 1; }
cat > "$OUT" <<EOF
GO_ENTRY=$ENTRY
GO_END=$END
EOF
echo "goaddr: entry=$ENTRY end=$END"
