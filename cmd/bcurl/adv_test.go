// Hostile-server tests of bcurl. They reuse the fake server of bcurl_test.go.
package main_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const (
	maxLength = 1<<24 - 1
	// clientTimeout is the bcurl read deadline. silentTimeout is how long a run may take
	// before the test calls bcurl hung: the deadline plus room for a slow race build.
	clientTimeout = 10 * time.Second
	silentTimeout = clientTimeout + 10*time.Second
)

// runFor is run with a longer limit, for the cases that wait on the bcurl read deadline.
func runFor(t *testing.T, limit time.Duration, args ...string) (stdout string, code int, took time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	var so bytes.Buffer
	cmd.Stdout = &so
	start := time.Now()
	err := cmd.Run()
	took = time.Since(start)
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("bcurl hung for %v: stdout=%q", limit, so.String())
	case errors.As(err, &ee):
		code = ee.ExitCode()
	case err != nil:
		t.Fatal(err)
	}
	return so.String(), code, took
}

// silentAfter reads the request, writes reply, then holds the connection open and
// silent until the test ends.
func silentAfter(t *testing.T, reply []byte) *fakeServer {
	release := make(chan struct{})
	srv := newFake(t, func(s *fakeServer, c net.Conn) {
		c.SetDeadline(time.Time{}) // the fake must outlast the bcurl deadline
		if s.readRequest(c) == nil {
			return
		}
		c.Write(reply)
		<-release
	})
	t.Cleanup(func() { close(release) }) // runs before the fake server's own cleanup
	return srv
}

func TestAdvSilentServer(t *testing.T) {
	t.Parallel()
	cases := map[string][]byte{
		"silent before RESPONSE": nil,
		"RESPONSE never ends":    append(respFrame(200, 0), dataFrame(0, "part")...),
	}
	for name, reply := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := silentAfter(t, reply)
			_, code, took := runFor(t, silentTimeout, srv.hostport()+"/x")
			if code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
			if took < clientTimeout/2 {
				t.Fatalf("bcurl gave up after %v, before its read deadline", took)
			}
		})
	}
}

func TestAdvLengthOneOverCap(t *testing.T) {
	srv := newFake(t, scripted(binary.BigEndian.AppendUint32([]byte{1, typeResponse, flagEnd, 0}, maxLength+1)))
	if _, _, code := run(t, srv.hostport()+"/x"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

// README.md maps 1xx to 5xx to exit codes. Any other status fits no row, so it is a
// protocol error: exit 1.
func TestAdvStatusOutOfRange(t *testing.T) {
	for _, status := range []uint16{0, 99, 600, 999, 0xffff} {
		srv := newFake(t, scripted(respFrame(status, flagEnd)))
		if _, _, code := run(t, srv.hostport()+"/x"); code != 1 {
			t.Errorf("status %d: exit %d, want 1", status, code)
		}
	}
}

func TestAdvUnknownFrameAtCap(t *testing.T) {
	big := frm(0x7f, flagEnd, make([]byte, maxLength))
	srv := newFake(t, scripted(bytes.Join([][]byte{big, respFrame(200, 0), dataFrame(flagEnd, "ok")}, nil)))
	out, _, code := run(t, srv.hostport()+"/x")
	if code != 0 || out != "ok" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
}

// -v must show the bytes that came off the wire, also the reserved byte and the
// flag bits that bcurl ignores.
func TestAdvVerboseShowsWireBytes(t *testing.T) {
	resp := respFrame(200, 0x80)
	resp[3] = 0xab
	unknown := frm(0x7f, 0xfe, []byte("zz"))
	unknown[3] = 0x55
	data := dataFrame(0x81, "hi")
	data[3] = 0x01
	srv := newFake(t, scripted(bytes.Join([][]byte{resp, unknown, data}, nil)))
	out, errOut, code := run(t, "-v", srv.hostport()+"/x")
	if code != 0 || out != "hi" {
		t.Fatalf("exit %d, stdout %q, stderr:\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"< RESPONSE flags=0x80 length=2 status=200",
		"< DATA flags=0x81 length=2",
		dumpBlock("< ", resp),
		dumpBlock("< ", unknown),
		dumpBlock("< ", data),
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q\nstderr:\n%s", want, errOut)
		}
	}
}

// SPEC.md section 3 gives the path a 2-byte length. A longer path cannot go on the
// wire, so it is a usage error, not a frame with a wrapped length.
func TestAdvPathTooLong(t *testing.T) {
	srv := newFake(t, scripted(respFrame(200, flagEnd)))
	_, _, code := run(t, srv.hostport()+"/"+strings.Repeat("a", 1<<16))
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if n := len(srv.requests()); n != 0 {
		t.Fatalf("server got %d requests, want 0", n)
	}
}
