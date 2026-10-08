#!/usr/bin/env python3
"""Independent BH/1 peer, written from SPEC.md only. Standard library only."""
import mimetypes
import os
import socket
import struct
import sys
import threading

VERSION = 1
T_REQUEST, T_RESPONSE, T_DATA = 1, 2, 3
F_END = 1
MAX_LEN = (1 << 24) - 1
CHUNK = 65536  # spec section 4: at most 65,536 bytes per DATA frame
IDLE_SECONDS = 30  # spec section 5: server MAY close an idle connection
CLIENT_TIMEOUT = 15  # a deadline on every client read
NAMES = ["", "host", "user-agent", "accept", "connection", "content-type",
         "content-length", "server", "date", "last-modified", "allow"]
IDS = {n: i for i, n in enumerate(NAMES) if n}


class Malformed(Exception):
    pass


class Dropped(Exception):
    """EOF inside a frame: drop the connection, send nothing."""


def read_exact(sock, n, allow_eof=False):
    buf = b""
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            if allow_eof and not buf:
                return None
            raise Dropped()
        buf += chunk
    return buf


def read_frame(sock):
    """Return (version, type, flags, payload) or None on clean EOF."""
    h = read_exact(sock, 8, allow_eof=True)
    if h is None:
        return None
    ver, typ, flags, _res, length = struct.unpack(">BBBBI", h)
    if ver != VERSION or length > MAX_LEN:
        return ver, typ, flags, length  # caller sees bad header; payload not read
    return ver, typ, flags, read_exact(sock, length) if length else b""


def frame(typ, flags, payload):
    return struct.pack(">BBBBI", VERSION, typ, flags, 0, len(payload)) + payload


def encode_headers(pairs):
    out = b""
    for name, value in pairs:
        v = value.encode() if isinstance(value, str) else value
        if name in IDS:
            out += struct.pack(">BH", IDS[name], len(v)) + v
        else:
            n = name.encode()
            out += struct.pack(">BB", 0, len(n)) + n + struct.pack(">H", len(v)) + v
    return out


def decode_headers(b):
    """Return dict of known/literal names. Raise Malformed on a bad block."""
    out, i = {}, 0
    while i < len(b):
        first = b[i]
        i += 1
        if first == 0:
            if i >= len(b):
                raise Malformed()
            nl = b[i]
            i += 1
            if nl == 0 or i + nl > len(b):
                raise Malformed()
            name = b[i:i + nl].decode("latin-1")
            i += nl
        else:
            name = NAMES[first] if first <= 10 else None
        if i + 2 > len(b):
            raise Malformed()
        (vl,) = struct.unpack(">H", b[i:i + 2])
        i += 2
        if i + vl > len(b):
            raise Malformed()
        if name is not None:
            out[name] = b[i:i + vl].decode("latin-1")
        i += vl
    return out


# ---------------------------------------------------------------- server

def resolve(root, path):
    """Return (status, filepath). Realpath check blocks symlink escapes."""
    rel = path[1:] + ("index.html" if path.endswith("/") else "")
    full = os.path.realpath(os.path.join(root, rel))
    if full != root and not full.startswith(root + os.sep):
        return 404, None
    if not os.path.isfile(full):
        return 404, None
    if not os.access(full, os.R_OK):
        return 500, None
    return 200, full


def respond(sock, status, extra, body):
    hdr = [("server", "peer.py/1"), ("content-length", str(len(body)))] + extra
    sock.sendall(frame(T_RESPONSE, F_END if not body else 0,
                       struct.pack(">H", status) + encode_headers(hdr)))
    for i in range(0, len(body), CHUNK):
        part = body[i:i + CHUNK]
        last = i + CHUNK >= len(body)
        sock.sendall(frame(T_DATA, F_END if last else 0, part))


def error(sock, status, extra=(), text=b""):
    hdr = list(extra)
    if text:
        hdr.append(("content-type", "text/plain"))
    respond(sock, status, hdr, text)


def handle_request(sock, root, p):
    # Structure first (400, stays open), then method (405), then path rules.
    if len(p) < 3:
        return error(sock, 400, text=b"bad request\n")
    method, plen = p[0], struct.unpack(">H", p[1:3])[0]
    if 3 + plen > len(p):
        return error(sock, 400, text=b"bad request\n")
    path = p[3:3 + plen]
    try:
        decode_headers(p[3 + plen:])
    except Malformed:
        return error(sock, 400, text=b"bad request\n")
    if method != 1:
        return error(sock, 405, [("allow", "GET")], b"method not allowed\n")
    if (not path.startswith(b"/") or b"\0" in path
            or b".." in path.split(b"/")):
        return error(sock, 400, text=b"bad path\n")
    status, full = resolve(root, path.decode("latin-1"))
    if status == 200:
        try:
            with open(full, "rb") as f:
                body = f.read()
        except OSError:
            status = 500
    if status != 200:
        return error(sock, status, text=b"error\n")
    ctype = mimetypes.guess_type(full)[0] or "application/octet-stream"
    respond(sock, 200, [("content-type", ctype)], body)


def serve_conn(sock, root):
    sock.settimeout(IDLE_SECONDS)
    try:
        while True:
            fr = read_frame(sock)
            if fr is None:
                return
            ver, typ, flags, payload = fr
            if ver != VERSION or isinstance(payload, int):
                error(sock, 400, [("connection", "close")], b"bad header\n")
                return
            if typ == T_REQUEST:
                handle_request(sock, root, payload)
            # any other type, known or not, is read and discarded (skip rule)
    except (Dropped, OSError):
        pass
    finally:
        sock.close()


def serve(root, port):
    root = os.path.realpath(root)
    srv = socket.socket()
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(64)
    while True:
        c, _ = srv.accept()
        threading.Thread(target=serve_conn, args=(c, root), daemon=True).start()


# ---------------------------------------------------------------- client

def read_response(sock):
    """Return (status, body, closes). Skips unknown frame types."""
    status, body, have_resp, closes = None, b"", False, False
    while True:
        fr = read_frame(sock)
        if fr is None:
            raise Dropped()
        ver, typ, flags, payload = fr
        if ver != VERSION or isinstance(payload, int):
            raise Malformed()
        if typ == T_RESPONSE and not have_resp:
            if len(payload) < 2:
                raise Malformed()
            status = struct.unpack(">H", payload[:2])[0]
            hdrs = decode_headers(payload[2:])
            closes = hdrs.get("connection", "").lower() == "close"
            have_resp = True
        elif typ == T_DATA and have_resp:
            body += payload
        else:
            continue  # unknown type: skip
        if flags & F_END:
            return status, body, closes


def get(addr, paths):
    host, _, port = addr.rpartition(":")
    code = 0
    try:
        sock = socket.create_connection((host, int(port)), timeout=CLIENT_TIMEOUT)
        for n, path in enumerate(paths):
            p = path.encode()
            payload = (b"\x01" + struct.pack(">H", len(p)) + p
                       + encode_headers([("host", addr), ("user-agent", "peer.py/1")]))
            sock.sendall(frame(T_REQUEST, F_END, payload))
            status, body, closes = read_response(sock)
            sys.stdout.buffer.write(body)
            sys.stdout.buffer.flush()
            if status >= 500:
                code = 5 if code != 1 else 1
            elif status >= 400 and code == 0:
                code = 4
            if closes and n < len(paths) - 1:
                return 1
        return code
    except (Dropped, Malformed, OSError, ValueError) as e:
        print("error: %r" % (e,), file=sys.stderr)
        return 1


if __name__ == "__main__":
    a = sys.argv[1:]
    if len(a) == 3 and a[0] == "serve":
        serve(a[1], int(a[2]))
    elif len(a) >= 3 and a[0] == "get":
        sys.exit(get(a[1], a[2:]))
    else:
        print("usage: peer.py serve ROOT PORT | get HOST:PORT PATH...", file=sys.stderr)
        sys.exit(2)
