#!/usr/bin/env bash
# Runs peer.py server and client against each other. Prints PASS or FAIL.
set -u
DIR="$(cd "$(dirname "$0")" && pwd)"
PEER="$DIR/peer.py"
TMP="$(mktemp -d)"
SRV=""
cleanup() { [ -n "$SRV" ] && kill "$SRV" 2>/dev/null; rm -rf "$TMP"; }
trap cleanup EXIT
fail() { echo "FAIL: $1"; exit 1; }

mkdir "$TMP/root"
printf 'hello\n' > "$TMP/root/a.txt"
printf 'second\n' > "$TMP/root/b.txt"
head -c 200000 /dev/urandom > "$TMP/root/big.bin"   # 4 DATA frames

PORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')
python3 "$PEER" serve "$TMP/root" "$PORT" & SRV=$!

# poll the port with a deadline, no fixed sleep
for _ in $(seq 100); do
  (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null && break
  sleep 0.05
done

A="127.0.0.1:$PORT"
timeout 20 python3 "$PEER" get "$A" /a.txt > "$TMP/o1"; rc=$?
[ $rc -eq 0 ] && cmp -s "$TMP/o1" "$TMP/root/a.txt" || fail "200 (rc=$rc)"

timeout 20 python3 "$PEER" get "$A" /nope > /dev/null 2>&1; rc=$?
[ $rc -eq 4 ] || fail "404 exit code $rc"

timeout 20 python3 "$PEER" get "$A" /big.bin > "$TMP/o3"; rc=$?
[ $rc -eq 0 ] && cmp -s "$TMP/o3" "$TMP/root/big.bin" || fail "multi-frame (rc=$rc)"

timeout 20 python3 "$PEER" get "$A" /a.txt /b.txt > "$TMP/o4"; rc=$?
[ $rc -eq 0 ] && [ "$(cat "$TMP/o4")" = "$(printf 'hello\nsecond')" ] || fail "two paths (rc=$rc)"

echo PASS
