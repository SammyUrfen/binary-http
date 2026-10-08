# BH/1: HTTP in binary, version 1

BH/1 carries a file request and a file response over one TCP connection. Every message is a frame. A frame has a fixed 8-byte header and a payload whose length the header gives. A receiver never searches for a delimiter. It reads 8 bytes, then reads exactly `length` bytes.

The words MUST, MUST NOT and MAY have their RFC 2119 meaning. All integers are unsigned and big-endian (network byte order).

## 1. The frame header (8 bytes)

| Offset | Width | Field | Value |
|---|---|---|---|
| 0 | 1 byte | version | `0x01` for BH/1 |
| 1 | 1 byte | type | see section 2 |
| 2 | 1 byte | flags | bit `0x01` is END. Other bits: send 0, ignore on receipt |
| 3 | 1 byte | reserved | send `0x00`, ignore on receipt |
| 4 | 4 bytes | length | payload length in bytes, `0` to `16,777,215` |

Why each width:

- **version, 1 byte, first.** A receiver checks the first byte of each frame before it trusts anything else. A text HTTP client sends `G` (`0x47`) there, so the server rejects it on byte one. A version 2 that needs a new header layout changes this byte.
- **type, 1 byte.** 256 types is more than BH/1 needs (3). The skip rule in section 2 makes room for new types without a version change.
- **flags, 1 byte.** BH/1 uses one bit. Seven bits stay free for a version 2.
- **reserved, 1 byte.** It keeps `length` at offset 4, so a reader gets it with one aligned 32-bit read. A version 2 can give it a meaning, because version 1 receivers ignore it.
- **length, 4 bytes, capped at 2^24 - 1.** The cap is the same as the HTTP/2 maximum. The cap is a rule, not a width, so a version 2 can raise it without a new header layout. The cap stops a 4 GiB length from making a receiver allocate 4 GiB.
- **No stream ID.** BH/1 sends one response at a time, in request order, like HTTP/1.1 keep-alive. HTTP/2 needs a stream ID because it interleaves many responses on one connection. BH/1 does not.

Why HTTP/2 chose 24 / 8 / 8 / 31: 24 bits of length keep frames small (16 KiB default, 16 MiB maximum), so one large body cannot block other streams for long. 8 bits of type and 8 of flags give room to grow. 31 bits of stream ID plus 1 reserved bit fill 32 bits, and the top bit stays clear for languages with only signed integers. The total is 9 bytes. BH/1 has no streams, so it spends those 4 bytes on a version byte, a reserved byte and a wider length.

## 2. Frame types

| Type | Name | Sender | Payload |
|---|---|---|---|
| `0x01` | REQUEST | client | method, path, header block (section 3) |
| `0x02` | RESPONSE | server | status, header block (section 3) |
| `0x03` | DATA | server | raw body bytes |

**The skip rule.** A receiver that reads a frame of a type it does not know MUST read and discard its `length` payload bytes, then continue with the next frame. It MUST NOT reply, fail or close. This holds in both directions and at any point on the connection. A server skips every type except REQUEST. A client skips every type except RESPONSE and DATA.

A client that gets DATA before RESPONSE, or a RESPONSE payload under 2 bytes, has a protocol error. It drops the connection.

## 3. Payloads

**REQUEST payload:** `method` (1 byte) | `path length` (2 bytes) | `path` (bytes) | header block (all remaining bytes).

- Method `0x01` is GET. BH/1 defines no other method.
- The path MUST start with `/`. It MUST NOT contain a `0x00` byte or a `..` segment (a part between two `/` that is exactly `..`). BH/1 does no percent-decoding.
- The server ignores the flags of a REQUEST.

**RESPONSE payload:** `status` (2 bytes, for example `0x00C8` = 200) | header block (all remaining bytes).

**Header block.** A list of entries with no count. The receiver reads entries until the payload ends. An entry has one of two forms:

| First byte | Then |
|---|---|
| `1` to `10`: a name ID from the table below | `value length` (2 bytes), `value` |
| `0`: a literal name | `name length` (1 byte, 1 to 255), `name` (lowercase ASCII), `value length` (2 bytes), `value` |

| ID | Name | ID | Name |
|---|---|---|---|
| 1 | host | 6 | content-length |
| 2 | user-agent | 7 | server |
| 3 | accept | 8 | date |
| 4 | connection | 9 | last-modified |
| 5 | content-type | 10 | allow |

These are the ten names that BH/1 tools send. Any other name goes as a literal. A receiver MUST skip an entry with an ID from 11 to 255, by its value length. A version 2 can add names to the table this way.

## 4. One exchange

1. The client opens one TCP connection and sends one REQUEST frame with END set.
2. The server sends one RESPONSE frame. If the body is empty, RESPONSE has END set and the exchange ends.
3. If not, the server sends DATA frames, each with at most 65,536 payload bytes. The last DATA frame has END set. A receiver MUST accept any DATA length up to the cap.
4. The connection stays open. The client MAY send the next REQUEST at any time, also before the last response ends. The server answers requests in order.

`content-length` on a response gives the body size as decimal ASCII. It is for information only. END decides where the body stops.

## 5. Status codes and errors

| Status | When | Connection |
|---|---|---|
| 200 | The file exists and the server can read it. The response MUST include `content-type` and `content-length` | stays open |
| 400 | The REQUEST payload is malformed (section 3 rules, a truncated entry, a name length of 0) | stays open, because the frame boundary is still known |
| 400 | The header is bad: version is not `0x01`, or length is over the cap | the server does not read the payload. It sends `connection: close`, then closes, because it cannot find the next frame |
| 404 | No regular file at the path: also a folder, a missing `index.html`, or a link out of the root | stays open |
| 405 | The method is not GET. The response includes `allow: GET` | stays open |
| 500 | The file exists but the server cannot read it | stays open |

The server checks in this order: the section 3 rules (400), then the method (405), then the file (404, 500 or 200).

The server maps a path to a file under its root folder. A path that ends in `/` maps to `index.html` in that folder. The server MUST NOT serve a file outside the root, also through a symbolic link. Error responses MAY carry a short `text/plain` body.

End of file between frames is a normal close. End of file inside a frame is an error: the receiver drops the connection and sends nothing. The server MAY close a connection that stays idle between frames for 30 seconds.

## 6. Example: a request, byte by byte

`GET /index.html` with `host: localhost:9000` and `user-agent: bcurl/1`. The payload is 41 bytes (`0x29`).

```
01 01 01 00 00 00 00 29     header: version 1, REQUEST, END, reserved, length 41
01                          method GET
00 0b 2f 69 6e 64 65 78     path length 11, "/index.html"
2e 68 74 6d 6c
01 00 0e 6c 6f 63 61 6c     ID 1 host, value length 14, "localhost:9000"
68 6f 73 74 3a 39 30 30 30
02 00 07 62 63 75 72 6c     ID 2 user-agent, value length 7, "bcurl/1"
2f 31
```

## 7. How a version 2 grows

New frame types and new header IDs need no version change, because BH/1 receivers skip them. The free flag bits and the reserved byte can carry optional features. A new header layout, for example one with a stream ID, changes the version byte. A BH/1 server answers that frame with 400 and closes.
