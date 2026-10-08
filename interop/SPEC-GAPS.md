# SPEC gaps found by the stranger

Each item: section, question, choice taken in `peer.py`.

1. **Section 5, check order.** A request can have a bad method and a bad path. Which status comes first? Choice: structure (400), then method (405), then path rules (400).
2. **Section 3, path rules.** What is a "`..` segment" when the path is `/a/..b` or `/a/b..`? Choice: only a segment that equals `..` after splitting on `/`.
3. **Section 3, header block in a REQUEST.** Must the server reject a bad entry in an ignored header? Choice: yes, a truncated entry or a literal name length of 0 gives 400 (section 5 says so).
4. **Section 3, literal names.** Must a receiver reject an uppercase name? Choice: no, accept and keep as is.
5. **Section 4, REQUEST without END.** The spec says the client sends END. It does not say what a server does without it. Choice: ignore the flag, treat the frame as a complete request.
6. **Section 2, RESPONSE frame from a client.** The skip rule names DATA only. Choice: skip every type except REQUEST on the server.
7. **Section 2/4, DATA before RESPONSE, or a second RESPONSE, on the client.** No rule. Choice: the client skips them.
8. **Section 5, 400 for a bad header.** Does the server read the payload of a frame with a bad version? Choice: no. It cannot trust the length, so it sends 400 with `connection: close` and closes. It also does this when length is over the cap.
9. **Section 5, "length over the cap".** The length field has 32 bits, so values above 16,777,215 exist. Choice: same 400 and close as a bad version. The spec does not say if the server reads the oversized frame.
10. **Section 5, directory without trailing `/`.** Spec maps only `/` endings to `index.html`. Choice: `/dir` is not a regular file, so 404.
11. **Section 5, symbolic link out of root.** "MUST NOT serve" does not give the status. Choice: 404, same as a missing file.
12. **Section 5, `/dir/` with no index.html.** Choice: 404.
13. **Section 5, 500 vs 404.** "Exists but cannot read" is tested with `os.access(R_OK)`, then a read error. Choice: 500 for both.
14. **Section 4, response headers.** Which headers must a server send? Choice: `server`, `content-length`, `content-type` (from file extension). Error bodies are `text/plain`.
15. **Section 4, `content-length` on a body-less response.** Choice: send `content-length: 0`.
16. **Section 4, pipelining.** The client MAY send early. Choice: the peer client sends one request, reads the full response, then sends the next. The server handles both because it reads in order.
17. **Client exit codes.** The spec has none (the README does). Choice from the task: 1 error, 4 any 4xx, 5 any 5xx. If both 4xx and 5xx occur, 5 wins.
18. **Client, `connection: close` before the last path.** The spec does not say what the client does. Choice: stop and exit 1.
19. **Section 5, idle timeout.** "MAY close after 30 seconds". Choice: the server does close at 30 seconds.
20. **Section 3, status in a RESPONSE shorter than 2 bytes.** Not defined. Choice: client treats it as a protocol error.
21. **Section 6, example.** The path is 11 bytes and the stated length is `0x0b`, which is correct. No contradiction found. The total of 41 bytes is correct: 1 + 2 + 11 + 17 + 10 = 41.
22. **Section 1, version text.** "A text HTTP client sends `G`." The server sends a 400 RESPONSE to it, which that client cannot read as text. This is fine, but the spec could say so.
