#!/usr/bin/env bash
# Interop in both directions: bcurl against peer.py serve, and peer.py get against bserve.
# Prints PASS or FAIL for each case. Exits 1 if any case fails.
set -u
DIR="$(cd "$(dirname "$0")" && pwd)"
PEER="$DIR/peer.py"
TMP="$(mktemp -d)"
PIDS=()
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done
  rm -rf "$TMP"
}
trap cleanup EXIT

# A client run that takes longer than this is a hang, not a slow answer.
RUN_LIMIT=30
# Port polls: 200 tries 0.05 s apart give a server 10 s to listen.
POLL_TRIES=200
POLL_GAP=0.05

(cd "$DIR/.." && go build -o "$TMP/bin/" ./cmd/...) || { echo "FAIL build"; exit 1; }

mkdir "$TMP/root"
printf 'hello\n' > "$TMP/root/a.txt"
printf 'second\n' > "$TMP/root/b.txt"
head -c 200000 /dev/urandom > "$TMP/root/big.bin" # 4 DATA frames
cat "$TMP/root/a.txt" "$TMP/root/b.txt" > "$TMP/ab.want"

free_port() {
  python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'
}

wait_port() {
  for _ in $(seq "$POLL_TRIES"); do
    (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null && return 0
    sleep "$POLL_GAP"
  done
  return 1
}

FAILED=0
# check NAME WANT_RC WANT_FILE CMD...: runs CMD, compares its exit code and its stdout.
check() {
  local name=$1 want_rc=$2 want_file=$3
  shift 3
  timeout "$RUN_LIMIT" "$@" > "$TMP/out" 2> "$TMP/err"
  local rc=$?
  if [ "$rc" -eq "$want_rc" ] && { [ -z "$want_file" ] || cmp -s "$TMP/out" "$want_file"; }; then
    echo "PASS $name"
  else
    echo "FAIL $name (exit $rc, want $want_rc): $(head -c 200 "$TMP/err")"
    FAILED=1
  fi
}

PEER_PORT=$(free_port)
python3 "$PEER" serve "$TMP/root" "$PEER_PORT" 2>/dev/null & PIDS+=($!)
BSERVE_PORT=$(free_port)
"$TMP/bin/bserve" "$TMP/root" "$BSERVE_PORT" 2>/dev/null & PIDS+=($!)
wait_port "$PEER_PORT" || { echo "FAIL peer.py serve did not listen"; exit 1; }
wait_port "$BSERVE_PORT" || { echo "FAIL bserve did not listen"; exit 1; }

P="127.0.0.1:$PEER_PORT"
check "bcurl -> peer.py: small file" 0 "$TMP/root/a.txt" "$TMP/bin/bcurl" "$P/a.txt"
check "bcurl -> peer.py: 200 KB file" 0 "$TMP/root/big.bin" "$TMP/bin/bcurl" "$P/big.bin"
check "bcurl -> peer.py: 404" 4 "" "$TMP/bin/bcurl" "$P/nope"
check "bcurl -> peer.py: two paths, one connection" 0 "$TMP/ab.want" "$TMP/bin/bcurl" "$P/a.txt" /b.txt

B="127.0.0.1:$BSERVE_PORT"
check "peer.py -> bserve: small file" 0 "$TMP/root/a.txt" python3 "$PEER" get "$B" /a.txt
check "peer.py -> bserve: 200 KB file" 0 "$TMP/root/big.bin" python3 "$PEER" get "$B" /big.bin
check "peer.py -> bserve: 404" 4 "" python3 "$PEER" get "$B" /nope
check "peer.py -> bserve: two paths, one connection" 0 "$TMP/ab.want" python3 "$PEER" get "$B" /a.txt /b.txt

exit "$FAILED"
