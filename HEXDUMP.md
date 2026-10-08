# Annotated hexdump: one BH/1 exchange

This file shows one real request and response. The bytes come from a real run on 2026-10-08. `SPEC.md` defines every field.

## The command

The server served `./www` on port 39517. The client ran this command:

```sh
./bin/bcurl -v 127.0.0.1:39517/index.html
```

`www/index.html` has 61 bytes. The client wrote the body to standard output and the frames to standard error.

## Raw `-v` output (standard error)

```
> REQUEST flags=0x01 length=48
> 00000000  01 01 01 00 00 00 00 30  01 00 0b 2f 69 6e 64 65  |.......0.../inde|
> 00000010  78 2e 68 74 6d 6c 01 00  0f 31 32 37 2e 30 2e 30  |x.html...127.0.0|
> 00000020  2e 31 3a 33 39 35 31 37  02 00 07 62 63 75 72 6c  |.1:39517...bcurl|
> 00000030  2f 31 03 00 03 2a 2f 2a                           |/1...*/*|
< RESPONSE flags=0x00 length=109 status=200
< 00000000  01 02 00 00 00 00 00 6d  00 c8 07 00 08 62 73 65  |.......m.....bse|
< 00000010  72 76 65 2f 31 05 00 18  74 65 78 74 2f 68 74 6d  |rve/1...text/htm|
< 00000020  6c 3b 20 63 68 61 72 73  65 74 3d 75 74 66 2d 38  |l; charset=utf-8|
< 00000030  06 00 02 36 31 08 00 1d  54 68 75 2c 20 30 38 20  |...61...Thu, 08 |
< 00000040  4f 63 74 20 32 30 32 36  20 31 33 3a 33 30 3a 32  |Oct 2026 13:30:2|
< 00000050  37 20 47 4d 54 09 00 1d  54 68 75 2c 20 30 38 20  |7 GMT...Thu, 08 |
< 00000060  4f 63 74 20 32 30 32 36  20 31 33 3a 33 30 3a 32  |Oct 2026 13:30:2|
< 00000070  31 20 47 4d 54                                    |1 GMT|
< DATA flags=0x01 length=61
< 00000000  01 03 01 00 00 00 00 3d  3c 21 64 6f 63 74 79 70  |.......=<!doctyp|
< 00000010  65 20 68 74 6d 6c 3e 0a  3c 74 69 74 6c 65 3e 42  |e html>.<title>B|
< 00000020  48 2f 31 3c 2f 74 69 74  6c 65 3e 0a 3c 68 31 3e  |H/1</title>.<h1>|
< 00000030  48 65 6c 6c 6f 20 6f 76  65 72 20 42 48 2f 31 3c  |Hello over BH/1<|
< 00000040  2f 68 31 3e 0a                                    |/h1>.|
```

## Standard output (the body)

```
<!doctype html>
<title>BH/1</title>
<h1>Hello over BH/1</h1>
```

## 1. REQUEST frame (client to server): 56 bytes

The frame has an 8-byte header and a 48-byte payload.

```
01 01 01 00 00 00 00 30     header
01                          method
00 0b                       path length
2f 69 6e 64 65 78 2e 68 74 6d 6c
                            path
01 00 0f                    header entry 1
31 32 37 2e 30 2e 30 2e 31 3a 33 39 35 31 37
                            value
02 00 07                    header entry 2
62 63 75 72 6c 2f 31        value
03 00 03                    header entry 3
2a 2f 2a                    value
```

| Bytes | Field | Meaning |
|---|---|---|
| `01` | version | BH/1 |
| `01` | type | REQUEST |
| `01` | flags | END is set |
| `00` | reserved | zero |
| `00 00 00 30` | length | 48 payload bytes follow |
| `01` | method | GET |
| `00 0b` | path length | 11 bytes follow |
| `2f 69 6e 64 65 78 2e 68 74 6d 6c` | path | `/index.html` |
| `01` | name ID | 1 = host |
| `00 0f` | value length | 15 |
| `31 32 37 2e 30 2e 30 2e 31 3a 33 39 35 31 37` | value | `127.0.0.1:39517` |
| `02` | name ID | 2 = user-agent |
| `00 07` | value length | 7 |
| `62 63 75 72 6c 2f 31` | value | `bcurl/1` |
| `03` | name ID | 3 = accept |
| `00 03` | value length | 3 |
| `2a 2f 2a` | value | `*/*` |

Check: payload 48 = 1 + 2 + 11 + (1 + 2 + 15) + (1 + 2 + 7) + (1 + 2 + 3). Frame 56 = 8 + 48.

## 2. RESPONSE frame (server to client): 117 bytes

The frame has an 8-byte header and a 109-byte payload. END is not set, because DATA frames follow.

```
01 02 00 00 00 00 00 6d     header
00 c8                       status
07 00 08                    header entry 1
62 73 65 72 76 65 2f 31     value
05 00 18                    header entry 2
74 65 78 74 2f 68 74 6d 6c 3b 20 63 68 61 72 73 65 74 3d 75 74 66 2d 38
                            value
06 00 02                    header entry 3
36 31                       value
08 00 1d                    header entry 4
54 68 75 2c 20 30 38 20 4f 63 74 20 32 30 32 36 20 31 33 3a 33 30 3a 32 37 20 47 4d 54
                            value
09 00 1d                    header entry 5
54 68 75 2c 20 30 38 20 4f 63 74 20 32 30 32 36 20 31 33 3a 33 30 3a 32 31 20 47 4d 54
                            value
```

| Bytes | Field | Meaning |
|---|---|---|
| `01` | version | BH/1 |
| `02` | type | RESPONSE |
| `00` | flags | END is not set |
| `00` | reserved | zero |
| `00 00 00 6d` | length | 109 payload bytes follow |
| `00 c8` | status | 200 |
| `07` | name ID | 7 = server |
| `00 08` | value length | 8 |
| `62 73 65 72 76 65 2f 31` | value | `bserve/1` |
| `05` | name ID | 5 = content-type |
| `00 18` | value length | 24 |
| `74 65 78 74 2f 68 74 6d 6c 3b 20 63 68 61 72 73 65 74 3d 75 74 66 2d 38` | value | `text/html; charset=utf-8` |
| `06` | name ID | 6 = content-length |
| `00 02` | value length | 2 |
| `36 31` | value | `61` (decimal ASCII) |
| `08` | name ID | 8 = date |
| `00 1d` | value length | 29 |
| `54 68 75 2c ... 32 37 20 47 4d 54` | value | `Thu, 08 Oct 2026 13:30:27 GMT` |
| `09` | name ID | 9 = last-modified |
| `00 1d` | value length | 29 |
| `54 68 75 2c ... 32 31 20 47 4d 54` | value | `Thu, 08 Oct 2026 13:30:21 GMT` |

Check: payload 109 = 2 + (1 + 2 + 8) + (1 + 2 + 24) + (1 + 2 + 2) + (1 + 2 + 29) + (1 + 2 + 29). Frame 117 = 8 + 109.

## 3. DATA frame (server to client): 69 bytes

The frame has an 8-byte header and a 61-byte payload. The payload is the file body, with no other field.

```
01 03 01 00 00 00 00 3d     header
3c 21 64 6f 63 74 79 70 65 20 68 74 6d 6c 3e 0a
                            body line 1: "<!doctype html>" and a newline (16 bytes)
3c 74 69 74 6c 65 3e 42 48 2f 31 3c 2f 74 69 74 6c 65 3e 0a
                            body line 2: "<title>BH/1</title>" and a newline (20 bytes)
3c 68 31 3e 48 65 6c 6c 6f 20 6f 76 65 72 20 42 48 2f 31 3c 2f 68 31 3e 0a
                            body line 3: "<h1>Hello over BH/1</h1>" and a newline (25 bytes)
```

| Bytes | Field | Meaning |
|---|---|---|
| `01` | version | BH/1 |
| `03` | type | DATA |
| `01` | flags | END is set: this is the last frame |
| `00` | reserved | zero |
| `00 00 00 3d` | length | 61 payload bytes follow |
| 61 bytes | body | the content of `www/index.html` |

Check: payload 61 = 16 + 20 + 25. The value of `content-length` in the RESPONSE is also 61. Frame 69 = 8 + 61.

## Totals

The client sent 56 bytes and got 117 + 69 = 186 bytes. Every length field equals the count of bytes after it.
