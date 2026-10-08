// Hostile-peer tests of bserve. They reuse the black-box helpers of bserve_test.go.
package main_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const (
	// serverTimeout is the idle and write limit of bserve (SPEC.md section 5 gives 30 s).
	serverTimeout = 30 * time.Second
	// timeoutSlack is how long after serverTimeout the server must have acted.
	timeoutSlack = 15 * time.Second
	maxPathLen   = 1<<16 - 1
)

// fetch is readResponse for a goroutine: it returns an error instead of t.Fatal.
func fetch(c net.Conn) (status int, body []byte, err error) {
	hdr, p, err := readFrame(c)
	if err != nil {
		return 0, nil, err
	}
	if hdr[1] != typeResponse || len(p) < 2 {
		return 0, nil, fmt.Errorf("want a RESPONSE, got header % x", hdr)
	}
	status = int(binary.BigEndian.Uint16(p))
	for hdr[2]&flagEnd == 0 {
		if hdr, p, err = readFrame(c); err != nil {
			return status, body, err
		}
		body = append(body, p...)
	}
	return status, body, nil
}

func TestAdvPathEscape(t *testing.T) {
	t.Parallel()
	addr, root := setup(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"loop":   "loop",
		"abs":    filepath.Join(outside, "secret.txt"),
		"outdir": outside,
		"rel":    "../" + filepath.Base(outside) + "/secret.txt",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		path   string
		status int
	}{
		{"/..", 400},
		{"/a/../..", 400},
		{"/sub/..", 400},
		{"//etc/passwd", 404},
		{"/./hello.txt", 200},
		{`/..\secret.txt`, 404},
		{"/sub\\index.html", 404},
		{"/\xff\xfe.txt", 404},
		{"/" + strings.Repeat("a", maxPathLen-1), 404},
		{"/loop", 404},
		{"/loop/", 404},
		{"/abs", 404},
		{"/outdir/secret.txt", 404},
		{"/outdir/", 404},
		{"/rel", 404},
		{"/sub", 404},
		{"/hello.txt/", 404},
	}
	c := dial(t, addr)
	for _, tc := range cases {
		send(t, c, get(tc.path))
		r := readResponse(t, c)
		name := tc.path
		if len(name) > 40 {
			name = name[:40] + "..."
		}
		if r.status != tc.status {
			t.Errorf("GET %q: status %d, want %d", name, r.status, tc.status)
		}
		if bytes.Contains(r.body, []byte(secret)) || bytes.Contains(r.body, []byte("root:")) {
			t.Errorf("GET %q: a file outside the root leaked", name)
		}
	}
	expectOpen(t, c)
}

// One byte per second keeps every single read alive. Only a deadline on the whole
// frame ends it.
func TestAdvSlowFrameTimesOut(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, header(version1, typeRequest, 1000))
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				c.SetWriteDeadline(time.Now().Add(readTimeout))
				if _, err := c.Write([]byte{0}); err != nil {
					return
				}
			}
		}
	}()
	c.SetReadDeadline(time.Now().Add(serverTimeout + timeoutSlack))
	var buf [1]byte
	n, err := c.Read(buf[:])
	var ne net.Error
	if n > 0 || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("server kept a trickled frame open: n=%d err=%v", n, err)
	}
}

// A client that sends requests but never reads must not hold a server goroutine forever.
func TestAdvClientThatNeverReads(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	// Far more than the socket buffers on both ends can hold.
	const requests = 200
	send(t, c, bytes.Repeat(get("/big.bin"), requests))
	// The wait is the subject of the test: the server must act on its own clock.
	<-time.After(serverTimeout + timeoutSlack)
	got := 0
	for ; got < requests; got++ {
		if _, _, err := fetch(c); err != nil {
			t.Logf("after %d responses: %v", got, err)
			break
		}
	}
	if got == requests {
		t.Fatalf("all %d responses arrived: the server waited forever for a reader", requests)
	}
}

func TestAdvManyConnections(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	const conns = 500
	cs := make([]net.Conn, conns)
	for i := range cs {
		cs[i] = dial(t, addr)
	}
	var wg sync.WaitGroup
	errs := make(chan error, conns)
	for i, c := range cs {
		wg.Go(func() {
			c.SetWriteDeadline(time.Now().Add(readTimeout))
			if _, err := c.Write(get("/hello.txt")); err != nil {
				errs <- fmt.Errorf("conn %d: write: %v", i, err)
				return
			}
			status, body, err := fetch(c)
			if err != nil || status != 200 || string(body) != helloBody {
				errs <- fmt.Errorf("conn %d: status %d, body %q, err %v", i, status, body, err)
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestAdvTenThousandHeaders(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	const entries = 10000
	var hs [][]byte
	for i := range entries {
		if i%2 == 0 {
			hs = append(hs, literal("x-a", fmt.Sprint(i)))
		} else {
			hs = append(hs, entry(idUnknown, "y"))
		}
	}
	c := dial(t, addr)
	send(t, c, frame(typeRequest, flagEnd, requestPayload(methodGet, "/hello.txt", hs...)))
	wantBody(t, readResponse(t, c), []byte(helloBody))
	expectOpen(t, c)
}

func TestAdvThousandPipelinedReadAtEnd(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	const requests = 1000
	c := dial(t, addr)
	var b []byte
	for i := range requests {
		path := "/hello.txt"
		if i%3 == 2 {
			path = "/nope"
		}
		b = append(b, get(path)...)
	}
	send(t, c, b)
	for i := range requests {
		status, body, err := fetch(c)
		want := 200
		if i%3 == 2 {
			want = 404
		}
		if err != nil || status != want || want == 200 && string(body) != helloBody {
			t.Fatalf("response %d: status %d, want %d, err %v", i, status, want, err)
		}
	}
}

func TestAdvHalfCloseStillAnswered(t *testing.T) {
	t.Parallel()
	addr, _ := setup(t)
	c := dial(t, addr)
	send(t, c, append(get("/hello.txt"), get("/big.bin")...))
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	wantBody(t, readResponse(t, c), []byte(helloBody))
	wantBody(t, readResponse(t, c), bigBody)
	if _, _, err := readFrame(c); err != io.EOF {
		t.Fatalf("after the last response: err = %v, want io.EOF", err)
	}
}

// A FIFO in the root must not hang the connection: opening one for read blocks until a writer appears.
func TestFIFONotServed(t *testing.T) {
	addr, root := setup(t)
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	c := dial(t, addr)
	send(t, c, get("/pipe"))
	wantStatus(t, readResponse(t, c), 404)
	expectOpen(t, c)
}
