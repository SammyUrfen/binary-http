# binary-http: rules for every session

Bibek's graded course project (Network Architecture, CN at Scaler). Due 2026-10-08, end of day. Language: Go 1.26.

Read `SPEC.md` (the protocol), then `README.md` (the two programs and the exit codes). They are the contract.

## Hard rules

1. Standard library only. No third-party module. No `net/http`.
2. `SPEC.md` decides. Do not change the wire format. If the spec is unclear or wrong, write it in your report. Do not guess silently.
3. The API in `internal/frame/frame.go` is frozen. Do not rename, remove or change a signature. You can add unexported helpers.
4. Tests first. A test that pins a behavior lands before the code for that behavior.
5. Black-box tests of `bserve` and `bcurl` build the real binary and use hand-written bytes, not `internal/frame`. A bug in `frame` must not hide itself.
6. Every test is deterministic: random free ports (`127.0.0.1:0`), temp folders, deadlines on every read. No `time.Sleep` to wait for a server. Poll the port with a deadline.
7. Comments explain why, not what. Named constants, no magic numbers.
8. Commit messages: one terse imperative sentence, no body, no trailer.

## The gate

```sh
gofmt -l .            # prints nothing
go vet ./...
go test -race -count=1 ./...
```

## Layout

| Path | What |
|---|---|
| `internal/frame/` | frame header, payload encode and decode |
| `cmd/bserve/` | the server |
| `cmd/bcurl/` | the client |
| `interop/` | an independent Python peer, written from `SPEC.md` only |
