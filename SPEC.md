# BH/1: HTTP in binary, version 1

BH/1 carries file requests and responses over one TCP connection. Every message is a frame: a fixed 8-byte header, then `length` payload bytes. A receiver never searches for a delimiter. MUST, MUST NOT and MAY follow RFC 2119. All integers are unsigned and big-endian.

## 1. The frame header (8 bytes)

| Offset | Width | Field | Value |
|---|---|---|---|
| 0 | 1 byte | version | `0x01` |
| 1 | 1 byte | type | section 2 |
| 2 | 1 byte | flags | `0x01` is END. Send other bits as 0, ignore them on receipt |
| 3 | 1 byte | reserved | send `0x00`, ignore on receipt |
| 4 | 4 bytes | length | payload bytes, `0` to `16,777,215` |

- **version, 1 byte, first.** The receiver checks it before it trusts anything else. A text HTTP client sends `G` (`0x47`) there, so the server rejects it on byte one. A new header layout changes this byte.
- **type and flags, 1 byte each.** BH/1 uses 3 types and 1 flag. The rest stay free, and the skip rule (section 2) lets new types in without a version change.
- **reserved, 1 byte.** It puts `length` at offset 4, for one aligned 32-bit read. Version 1 ignores it, so a version 2 can use it.
- **length, 4 bytes, capped at 2^24 - 1.** The cap equals the HTTP/2 maximum, and it stops a bogus 4 GiB length from a 4 GiB allocation. The cap is a rule, not a width, so a version 2 can raise it in the same layout.
- **No stream ID.** BH/1 answers one request at a time, in order, like HTTP/1.1 keep-alive. Nothing interleaves, so nothing needs an ID.

HTTP/2 chose 24 / 8 / 8 / 31 (9 bytes) because it interleaves streams. Small frames stop one body from blocking other streams, and a 31-bit ID plus 1 reserved bit fills 32 bits with the sign bit clear. BH/1 has no streams, so it spends those 4 bytes on a version byte, a reserved byte and a wider length.

## 2. Frame types and the skip rule

| Type | Name | Sender | Payload |
|---|---|---|---|
| `0x01` | REQUEST | client | method, path, header block |
| `0x02` | RESPONSE | server | status, header block |
| `0x03` | DATA | server | body bytes |

**A receiver that gets a frame type it does not know MUST read and discard its `length` bytes, and continue. It MUST NOT reply, fail or close.** This holds in both directions, at any point. A server skips every type except REQUEST. A client skips every type except RESPONSE and DATA. A client that gets DATA before RESPONSE, or a RESPONSE payload under 2 bytes, drops the connection.

**How a version 2 grows:** new types and header IDs need no new version, because version 1 skips them. Free flag bits and the reserved byte carry optional features. A new layout changes the version byte, and a BH/1 server answers it with 400 and closes.

## 3. Payloads

**REQUEST:** `method` (1 byte) | `path length` (2 bytes) | `path` | header block (the rest). The server ignores REQUEST flags.

- Method `0x01` is GET, the only method in BH/1.
- The path MUST start with `/`. It MUST NOT contain `0x00` or a `..` segment (a part between two `/` that is exactly `..`). No percent-decoding.

**RESPONSE:** `status` (2 bytes, `0x00C8` = 200) | header block (the rest).

**Header block:** entries until the payload ends, with no count. Each entry is one of:

| First byte | Then |
|---|---|
| `1` to `10`: a name ID | `value length` (2 bytes), `value` |
| `0`: a literal name | `name length` (1 byte, 1 to 255), lowercase ASCII `name`, `value length` (2 bytes), `value` |

| ID | Name | ID | Name |
|---|---|---|---|
| 1 | host | 6 | content-length |
| 2 | user-agent | 7 | server |
| 3 | accept | 8 | date |
| 4 | connection | 9 | last-modified |
| 5 | content-type | 10 | allow |

These are the ten names that BH/1 tools send. Any other name is a literal. A receiver MUST skip an entry with ID 11 to 255 by its value length, so a version 2 can grow the table.

## 4. One exchange

1. The client opens one TCP connection and sends a REQUEST with END set.
2. The server sends one RESPONSE. For an empty body, RESPONSE has END set and the exchange ends.
3. Otherwise DATA frames follow, each with at most 65,536 bytes. The last one has END set. A receiver MUST accept any DATA length up to the cap. `content-length` (decimal ASCII) is for information only: END decides where the body stops.
4. The connection stays open. The client MAY send the next REQUEST at any time, also before a response ends. The server answers in order.

## 5. Status codes and errors

| Status | When | Connection |
|---|---|---|
| 200 | The file exists and is readable. MUST include `content-type` and `content-length` | open |
| 400 | The REQUEST payload breaks a section 3 rule, or an entry is truncated | open: the boundary is known |
| 400 | Bad header: version is not `0x01`, or length is over the cap. The server does not read the payload and sends `connection: close` | closed: the next frame cannot be found |
| 404 | No regular file: also a folder, a missing `index.html`, or a link out of the root | open |
| 405 | The method is not GET. Includes `allow: GET` | open |
| 500 | The file exists but cannot be read | open |

The server checks in this order: section 3 rules (400), the method (405), the file. A path that ends in `/` maps to `index.html` in that folder. The server MUST NOT serve a file outside its root, also through a symbolic link. Error responses MAY carry a short `text/plain` body.

End of file between frames is a normal close. End of file inside a frame is an error: the receiver drops the connection and sends nothing. The server MAY close a connection idle between frames for 30 seconds.

## 6. Example: a request, byte by byte

`GET /index.html`, `host: localhost:9000`, `user-agent: bcurl/1`. Payload 41 = 1 + 2 + 11 + 17 + 10.

```
01 01 01 00 00 00 00 29                               version 1, REQUEST, END, reserved, length 41
01                                                    method GET
00 0b 2f 69 6e 64 65 78 2e 68 74 6d 6c                path length 11, "/index.html"
01 00 0e 6c 6f 63 61 6c 68 6f 73 74 3a 39 30 30 30    ID 1 host, length 14, "localhost:9000"
02 00 07 62 63 75 72 6c 2f 31                         ID 2 user-agent, length 7, "bcurl/1"
```
