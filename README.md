# binary-http

Two programs that speak BH/1, a binary version of HTTP. `SPEC.md` defines the protocol.

## Build

```sh
go build -o bin/ ./cmd/...
```

## bserve, the server

```sh
./bin/bserve ./www 9000
```

- Serves files under the root folder (`./www`) on TCP port 9000, on all interfaces.
- Handles each connection in its own goroutine. Keeps a connection open as `SPEC.md` section 5 says.
- Logs one line for each request to standard error: the remote address, the method, the path and the status.
- Sends `server: bserve/1`, `content-type`, `content-length`, `date` and `last-modified` on a 200.

## bcurl, the client

```sh
./bin/bcurl [-v] HOST:PORT/PATH [PATH ...]
```

- Opens one TCP connection to `HOST:PORT`. Sends one GET for each path, in order, on that connection. Never opens a second connection.
- Sends `host: HOST:PORT`, `user-agent: bcurl/1` and `accept: */*`.
- Writes each response body to standard output, with nothing between bodies.
- `-v` writes every frame, sent and received, to standard error: one summary line, then a hexdump.

```
> REQUEST flags=0x01 length=41
> 00000000  01 01 01 00 00 00 00 29  01 00 0b 2f 69 6e 64 65  |.......)..../inde|
< RESPONSE flags=0x00 length=96 status=200
< 00000000  ...
```

The hexdump lines use the format of Go `encoding/hex.Dumper`, with `> ` before a sent frame and `< ` before a received frame.

| Exit code | When |
|---|---|
| 0 | Every response is 1xx, 2xx or 3xx |
| 1 | Connection error, or a protocol error (bad frame, DATA before RESPONSE, end of file inside a response) |
| 2 | Usage error |
| 4 | At least one response is 4xx, and none is 5xx |
| 5 | At least one response is 5xx |

## Tests

```sh
gofmt -l . && go vet ./... && go test -race ./...
```
