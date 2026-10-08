// Black-box tests of bserve. They build the real binary, start it on a free port,
// and speak BH/1 with hand-written bytes. They do not import internal/frame, so a
// bug in frame cannot hide itself (CLAUDE.md rule 5).
package main_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Wire constants, copied from SPEC.md on purpose and not imported.
const (
	version1     = 0x01
	typeRequest  = 0x01
	typeResponse = 0x02
	typeData     = 0x03
	typeUnknown  = 0x7f
	flagEnd      = 0x01
	methodGet    = 0x01
	methodOther  = 0x02
	headerLen    = 8
	maxLength    = 1<<24 - 1
	maxDataChunk = 65536

	idHost       = 1
	idConnection = 4
	idUnknown    = 200
)

// headerNames is the name table of SPEC.md section 3. IDs outside it are skipped.
var headerNames = map[byte]string{
	1: "host", 2: "user-agent", 3: "accept", 4: "connection", 5: "content-type",
	6: "content-length", 7: "server", 8: "date", 9: "last-modified", 10: "allow",
}

const (
	readTimeout  = 2 * time.Second
	startTimeout = 5 * time.Second
	// pollInterval is the gap between two connect tries while bserve starts.
	pollInterval = 10 * time.Millisecond
	dialTimeout  = 100 * time.Millisecond
)

// The files in every test root.
const (
	helloBody    = "hello, BH/1\n"
	indexBody    = "<h1>root</h1>\n"
	subIndexBody = "<h1>sub</h1>\n"
	// bigSize needs more than three DATA frames of the largest size.
	bigSize = 200 * 1024
	secret  = "TOP-SECRET-OUTSIDE-THE-ROOT"
)

var bigBody = func() []byte {
	b := make([]byte, bigSize)
	for i := range b {
		// A prime modulus, so a frame that is out of place changes the bytes.
		b[i] = byte(i % 251)
	}
	return b
}()

var (
	bservePath string
	buildFlags []string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bserve-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bservePath = filepath.Join(dir, "bserve")
	args := append([]string{"build"}, buildFlags...)
	args = append(args, "-o", bservePath, ".")
	if out, err := exec.Command("go", args...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build bserve: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// ---- server and connection helpers ----

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// startServer runs bserve on root and returns its address once the port accepts.
func startServer(t *testing.T, root string) string {
	t.Helper()
	port := freePort(t)
	var stderr bytes.Buffer
	cmd := exec.Command(bservePath, root, strconv.Itoa(port))
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bserve: %v", err)
	}
	var waitErr error
	done := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
		if strings.Contains(stderr.String(), "DATA RACE") {
			t.Errorf("bserve has a data race:\n%s", stderr.String())
		} else if t.Failed() {
			t.Logf("bserve stderr:\n%s", stderr.String())
		}
	})

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(startTimeout)
	for {
		c, err := net.DialTimeout("tcp", addr, dialTimeout)
		if err == nil {
			c.Close()
			return addr
		}
		if time.Now().After(deadline) {
			t.Fatalf("bserve did not listen on %s in %v: %v", addr, startTimeout, err)
		}
		select {
		case <-done:
			t.Fatalf("bserve exited before it listened: %v", waitErr)
		case <-time.After(pollInterval):
		}
	}
}

func makeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string][]byte{
		"index.html":     []byte(indexBody),
		"hello.txt":      []byte(helloBody),
		"empty.txt":      nil,
		"sub/index.html": []byte(subIndexBody),
		"big.bin":        bigBody,
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// setup gives a running server on a fresh root.
func setup(t *testing.T) (addr, root string) {
	t.Helper()
	root = makeRoot(t)
	return startServer(t, root), root
}

func dial(t *testing.T, addr string) *net.TCPConn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, readTimeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*net.TCPConn)
}

func send(t *testing.T, c net.Conn, b []byte) {
	t.Helper()
	c.SetWriteDeadline(time.Now().Add(readTimeout))
	if _, err := c.Write(b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// ---- frame builders ----

func rawFrame(version, typ, flags, reserved byte, payload []byte) []byte {
	b := []byte{version, typ, flags, reserved}
	b = binary.BigEndian.AppendUint32(b, uint32(len(payload)))
	return append(b, payload...)
}

func frame(typ, flags byte, payload []byte) []byte {
	return rawFrame(version1, typ, flags, 0, payload)
}

// header gives an 8-byte frame header whose length field is free to lie.
func header(version, typ byte, length uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte{version, typ, flagEnd, 0}, length)
}

func requestPayload(method byte, path string, entries ...[]byte) []byte {
	b := []byte{method}
	b = binary.BigEndian.AppendUint16(b, uint16(len(path)))
	b = append(b, path...)
	for _, e := range entries {
		b = append(b, e...)
	}
	return b
}

func entry(id byte, value string) []byte {
	b := binary.BigEndian.AppendUint16([]byte{id}, uint16(len(value)))
	return append(b, value...)
}

func literal(name, value string) []byte {
	b := append([]byte{0, byte(len(name))}, name...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(value)))
	return append(b, value...)
}

func get(path string) []byte {
	return frame(typeRequest, flagEnd, requestPayload(methodGet, path, entry(idHost, "localhost")))
}

// ---- frame readers ----

func readFrame(c net.Conn) (hdr [headerLen]byte, payload []byte, err error) {
	c.SetReadDeadline(time.Now().Add(readTimeout))
	if _, err = io.ReadFull(c, hdr[:]); err != nil {
		return hdr, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[4:])
	if n > maxLength {
		return hdr, nil, fmt.Errorf("length %d is over the cap", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(c, payload)
	return hdr, payload, err
}

type response struct {
	status   int
	headers  map[string]string
	body     []byte
	dataLens []int // the payload length of each DATA frame, in order
}

// readResponse reads one RESPONSE frame and its DATA frames up to END.
func readResponse(t *testing.T, c net.Conn) response {
	t.Helper()
	hdr, p, err := readFrame(c)
	if err != nil {
		t.Fatalf("read RESPONSE: %v", err)
	}
	if hdr[0] != version1 || hdr[1] != typeResponse {
		t.Fatalf("want a BH/1 RESPONSE frame, got header % x", hdr)
	}
	if len(p) < 2 {
		t.Fatalf("RESPONSE payload is %d bytes, under the 2-byte status", len(p))
	}
	r := response{status: int(binary.BigEndian.Uint16(p))}
	if r.headers, err = parseHeaders(p[2:]); err != nil {
		t.Fatalf("RESPONSE header block: %v", err)
	}
	for end := hdr[2]&flagEnd != 0; !end; end = hdr[2]&flagEnd != 0 {
		if hdr, p, err = readFrame(c); err != nil {
			t.Fatalf("read DATA: %v", err)
		}
		if hdr[0] != version1 || hdr[1] != typeData {
			t.Fatalf("want a BH/1 DATA frame, got header % x", hdr)
		}
		r.body = append(r.body, p...)
		r.dataLens = append(r.dataLens, len(p))
	}
	return r
}

func parseHeaders(b []byte) (map[string]string, error) {
	h := map[string]string{}
	for len(b) > 0 {
		id := b[0]
		b = b[1:]
		name := headerNames[id]
		if id == 0 {
			if len(b) < 1 || b[0] == 0 || len(b) < 1+int(b[0]) {
				return nil, errors.New("bad literal name")
			}
			name = string(b[1 : 1+b[0]])
			b = b[1+int(b[0]):]
		}
		if len(b) < 2 {
			return nil, errors.New("truncated value length")
		}
		n := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < n {
			return nil, errors.New("truncated value")
		}
		if name != "" {
			h[name] = string(b[:n])
		}
		b = b[n:]
	}
	return h, nil
}

// ---- assertions ----

func wantStatus(t *testing.T, r response, status int) {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d, want %d (headers %v, body %q)", r.status, status, r.headers, r.body)
	}
}

func wantBody(t *testing.T, r response, body []byte) {
	t.Helper()
	wantStatus(t, r, 200)
	if !bytes.Equal(r.body, body) {
		t.Fatalf("body is %d bytes and differs from the %d-byte file", len(r.body), len(body))
	}
}

// expectOpen proves the connection still works: a new request gets its answer.
func expectOpen(t *testing.T, c net.Conn) {
	t.Helper()
	send(t, c, get("/hello.txt"))
	wantBody(t, readResponse(t, c), []byte(helloBody))
}

// expectClosed proves the server closed the connection and sent nothing more.
func expectClosed(t *testing.T, c net.Conn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(readTimeout))
	var buf [1]byte
	n, err := c.Read(buf[:])
	if n > 0 {
		t.Fatalf("want the connection closed, got byte 0x%02x", buf[0])
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatal("want the connection closed, it stayed open")
	}
	// io.EOF and a reset both mean closed.
	if err == nil {
		t.Fatal("want the connection closed, read gave no error")
	}
}

// ---- tests: SPEC.md section 4, one exchange ----

func TestGetSmallFile(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, get("/hello.txt"))
	r := readResponse(t, c)
	wantBody(t, r, []byte(helloBody))
	if r.headers["content-type"] == "" {
		t.Error("no content-type")
	}
	if got, want := r.headers["content-length"], strconv.Itoa(len(helloBody)); got != want {
		t.Errorf("content-length = %q, want %q", got, want)
	}
	// README.md: the other headers on a 200.
	if got := r.headers["server"]; got != "bserve/1" {
		t.Errorf("server = %q, want bserve/1", got)
	}
	for _, name := range []string{"date", "last-modified"} {
		if r.headers[name] == "" {
			t.Errorf("no %s", name)
		}
	}
	if len(r.dataLens) == 0 {
		t.Error("a non-empty body needs DATA frames")
	}
}

func TestGetLargeFile(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, get("/big.bin"))
	r := readResponse(t, c)
	wantBody(t, r, bigBody)
	if len(r.dataLens) < 2 {
		t.Errorf("got %d DATA frames, want several", len(r.dataLens))
	}
	for i, n := range r.dataLens {
		if n > maxDataChunk {
			t.Errorf("DATA frame %d has %d bytes, over %d", i, n, maxDataChunk)
		}
	}
	if got := r.headers["content-length"]; got != strconv.Itoa(bigSize) {
		t.Errorf("content-length = %q, want %d", got, bigSize)
	}
}

func TestGetEmptyFile(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, get("/empty.txt"))
	r := readResponse(t, c)
	wantStatus(t, r, 200)
	if len(r.dataLens) != 0 {
		t.Errorf("got %d DATA frames, want RESPONSE with END and no DATA", len(r.dataLens))
	}
	expectOpen(t, c)
}

func TestSlashMapsToIndex(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	for path, body := range map[string]string{"/": indexBody, "/sub/": subIndexBody} {
		send(t, c, get(path))
		r := readResponse(t, c)
		if !bytes.Equal(r.body, []byte(body)) {
			t.Errorf("GET %s: status %d, body %q, want %q", path, r.status, r.body, body)
		}
	}
}

// ---- tests: SPEC.md section 5, statuses ----

func TestNotFoundKeepsConnection(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	// "/..x" has no ".." segment, so it is a legal path to a missing file.
	for _, path := range []string{"/missing.txt", "/sub", "/nodir/", "/hello.txt/", "/..x"} {
		t.Run(path, func(t *testing.T) {
			send(t, c, get(path))
			wantStatus(t, readResponse(t, c), 404)
			expectOpen(t, c)
		})
	}
}

func TestMalformedRequestKeepsConnection(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	host := entry(idHost, "localhost")
	cases := []struct {
		name    string
		payload []byte
	}{
		{"dotdot at start", requestPayload(methodGet, "/../hello.txt", host)},
		{"dotdot inside", requestPayload(methodGet, "/sub/../hello.txt", host)},
		{"dotdot at end", requestPayload(methodGet, "/sub/..", host)},
		{"no leading slash", requestPayload(methodGet, "hello.txt", host)},
		{"empty path", requestPayload(methodGet, "", host)},
		{"nul in path", requestPayload(methodGet, "/hello\x00.txt", host)},
		{"path length past payload", []byte{methodGet, 0x00, 0x32, '/', 'x'}},
		{"empty payload", nil},
		{"payload of 1 byte", []byte{methodGet}},
		{"payload of 2 bytes", []byte{methodGet, 0x00}},
		{"entry without value length", requestPayload(methodGet, "/hello.txt", []byte{idHost, 0x00})},
		{"entry value cut short", requestPayload(methodGet, "/hello.txt", []byte{idHost, 0x00, 0x05, 'a'})},
		{"unknown ID value cut short", requestPayload(methodGet, "/hello.txt", []byte{idUnknown, 0x00, 0x09, 'a'})},
		{"literal without name length", requestPayload(methodGet, "/hello.txt", []byte{0x00})},
		{"literal name cut short", requestPayload(methodGet, "/hello.txt", []byte{0x00, 0x03, 'a'})},
		{"literal name length 0", requestPayload(methodGet, "/hello.txt", []byte{0x00, 0x00, 0x00, 0x00})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, addr)
			send(t, c, frame(typeRequest, flagEnd, tc.payload))
			wantStatus(t, readResponse(t, c), 400)
			expectOpen(t, c)
		})
	}
}

func TestMethodNotAllowed(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, frame(typeRequest, flagEnd, requestPayload(methodOther, "/hello.txt", entry(idHost, "localhost"))))
	r := readResponse(t, c)
	wantStatus(t, r, 405)
	if got := r.headers["allow"]; got != "GET" {
		t.Errorf("allow = %q, want GET", got)
	}
	expectOpen(t, c)
}

// A bad header loses the frame boundary, so the server answers 400 and closes.
// Each case sends only header bytes: unread bytes at close would make the
// kernel send a reset, which can drop the 400 before the test reads it.
func TestBadHeaderCloses(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"version 2", header(0x02, typeRequest, 0)},
		{"text HTTP", []byte("GET / HT")},
		{"length one over the cap", header(version1, typeRequest, maxLength+1)},
		{"length 0xffffffff", header(version1, typeRequest, 0xffffffff)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, addr)
			send(t, c, tc.bytes)
			r := readResponse(t, c)
			wantStatus(t, r, 400)
			if got := r.headers["connection"]; got != "close" {
				t.Errorf("connection = %q, want close", got)
			}
			expectClosed(t, c)
		})
	}
}

func TestEOFInsideFrameClosesSilently(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	full := get("/hello.txt")
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"inside the header", full[:5]},
		{"inside the payload", full[:headerLen+3]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dial(t, addr)
			send(t, c, tc.bytes)
			if err := c.CloseWrite(); err != nil {
				t.Fatal(err)
			}
			expectClosed(t, c)
		})
	}
}

func TestEOFBetweenFramesCloses(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	expectOpen(t, c)
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	expectClosed(t, c)
}

func TestSymlinkOutsideRootNotServed(t *testing.T) {
	t.Parallel()
	addr, root := setup(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	c := dial(t, addr)
	for _, path := range []string{"/link.txt", "/out/secret.txt"} {
		send(t, c, get(path))
		r := readResponse(t, c)
		if r.status == 200 || bytes.Contains(r.body, []byte(secret)) {
			t.Errorf("GET %s: status %d, body %q: a file outside the root leaked", path, r.status, r.body)
		}
	}
}

func TestUnreadableFile(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file, so no 500 is possible")
	}
	addr, root := setup(t)
	if err := os.WriteFile(filepath.Join(root, "locked.txt"), []byte("no"), 0o000); err != nil {
		t.Fatal(err)
	}
	c := dial(t, addr)
	send(t, c, get("/locked.txt"))
	wantStatus(t, readResponse(t, c), 500)
	expectOpen(t, c)
}

// ---- tests: SPEC.md sections 1 to 3, the receiver rules ----

func TestSkipRule(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)

	t.Run("unknown and client DATA frames", func(t *testing.T) {
		c := dial(t, addr)
		var b []byte
		b = append(b, frame(typeUnknown, 0, []byte("ignore me"))...)
		b = append(b, frame(typeUnknown, flagEnd, nil)...)
		b = append(b, frame(typeData, flagEnd, []byte("client data"))...)
		b = append(b, get("/hello.txt")...)
		b = append(b, frame(0x00, 0, []byte{0x01})...)
		b = append(b, get("/")...)
		send(t, c, b)
		wantBody(t, readResponse(t, c), []byte(helloBody))
		wantBody(t, readResponse(t, c), []byte(indexBody))
	})

	t.Run("unknown frame at the length cap", func(t *testing.T) {
		c := dial(t, addr)
		b := append(frame(typeUnknown, 0, make([]byte, maxLength)), get("/hello.txt")...)
		// The write is large, so give it time beyond the usual deadline.
		c.SetWriteDeadline(time.Now().Add(10 * readTimeout))
		if _, err := c.Write(b); err != nil {
			t.Fatalf("write: %v", err)
		}
		wantBody(t, readResponse(t, c), []byte(helloBody))
	})
}

func TestIgnoredFields(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	host := entry(idHost, "localhost")
	cases := []struct {
		name  string
		frame []byte
	}{
		{"header ID 200", frame(typeRequest, flagEnd, requestPayload(methodGet, "/hello.txt", entry(idUnknown, "x"), host))},
		{"literal header", frame(typeRequest, flagEnd, requestPayload(methodGet, "/hello.txt", literal("x-custom", "1"), host))},
		{"nonzero reserved byte", rawFrame(version1, typeRequest, flagEnd, 0xff, requestPayload(methodGet, "/hello.txt", host))},
		{"unknown flag bits", rawFrame(version1, typeRequest, 0xff, 0, requestPayload(methodGet, "/hello.txt", host))},
	}
	c := dial(t, addr)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			send(t, c, tc.frame)
			wantBody(t, readResponse(t, c), []byte(helloBody))
		})
	}
}

func TestRequestOneByteAtATime(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	for _, b := range get("/hello.txt") {
		send(t, c, []byte{b})
	}
	wantBody(t, readResponse(t, c), []byte(helloBody))
}

// ---- tests: many requests and many connections ----

func TestPipelinedRequestsInOrder(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, append(get("/hello.txt"), get("/sub/")...))
	wantBody(t, readResponse(t, c), []byte(helloBody))
	wantBody(t, readResponse(t, c), []byte(subIndexBody))
}

func TestFiftyRequestsOneConnection(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	const requests = 50
	for i := range requests {
		path, body := "/hello.txt", helloBody
		if i%2 == 1 {
			path, body = "/", indexBody
		}
		send(t, c, get(path))
		r := readResponse(t, c)
		if r.status != 200 || !bytes.Equal(r.body, []byte(body)) {
			t.Fatalf("request %d, GET %s: status %d, body %q", i, path, r.status, r.body)
		}
	}
}

func TestConcurrentConnections(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)

	t.Run("two at once", func(t *testing.T) {
		// a stops in the middle of a header, so a server that handles one
		// connection at a time never gets to b.
		req := get("/hello.txt")
		a := dial(t, addr)
		send(t, a, req[:4])
		b := dial(t, addr)
		send(t, b, req)
		wantBody(t, readResponse(t, b), []byte(helloBody))
		send(t, a, req[4:])
		wantBody(t, readResponse(t, a), []byte(helloBody))
	})

	t.Run("new connection after a bad header", func(t *testing.T) {
		bad := dial(t, addr)
		send(t, bad, header(0x02, typeRequest, 0))
		wantStatus(t, readResponse(t, bad), 400)
		expectClosed(t, bad)
		expectOpen(t, dial(t, addr))
	})

	t.Run("new connection after end of file inside a frame", func(t *testing.T) {
		cut := dial(t, addr)
		send(t, cut, get("/hello.txt")[:3])
		if err := cut.CloseWrite(); err != nil {
			t.Fatal(err)
		}
		expectClosed(t, cut)
		expectOpen(t, dial(t, addr))
	})
}
