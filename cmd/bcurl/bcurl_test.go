package main_test

// External test package: the helpers here cannot collide with names in the real main.go.
// Black-box tests: build the real binary, point it at a fake server that speaks hand-written bytes.
// This file must not import internal/frame (CLAUDE.md rule 5): a bug in frame must not hide itself.

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	ioDeadline = 5 * time.Second  // a fake server never waits longer than this for the client
	runTimeout = 10 * time.Second // a client that hangs fails the test instead of the whole run

	typeRequest  = 1
	typeResponse = 2
	typeData     = 3
	flagEnd      = 1
)

var binPath string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bcurl-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "bcurl")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// --- hand-written wire bytes ---

func frm(typ, flags byte, payload []byte) []byte {
	b := []byte{1, typ, flags, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(b[4:], uint32(len(payload)))
	return append(b, payload...)
}

func idHdr(id byte, v string) []byte {
	return append([]byte{id, byte(len(v) >> 8), byte(len(v))}, v...)
}

func litHdr(name, v string) []byte {
	b := append([]byte{0, byte(len(name))}, name...)
	b = append(b, byte(len(v)>>8), byte(len(v)))
	return append(b, v...)
}

func respFrame(status uint16, flags byte, hdrs ...[]byte) []byte {
	p := []byte{byte(status >> 8), byte(status)}
	for _, h := range hdrs {
		p = append(p, h...)
	}
	return frm(typeResponse, flags, p)
}

func dataFrame(flags byte, body string) []byte { return frm(typeData, flags, []byte(body)) }

// wantRequest is the exact request that README.md promises.
func wantRequest(hostport, path string) []byte {
	p := []byte{1, byte(len(path) >> 8), byte(len(path))}
	p = append(p, path...)
	p = append(p, idHdr(1, hostport)...)
	p = append(p, idHdr(2, "bcurl/1")...)
	p = append(p, idHdr(3, "*/*")...)
	return frm(typeRequest, flagEnd, p)
}

// --- fake server ---

type fakeServer struct {
	t       *testing.T
	ln      net.Listener
	accepts atomic.Int32
	mu      sync.Mutex
	reqs    [][]byte
	wg      sync.WaitGroup
}

// newFake starts a server on a random free port. Each connection runs handle.
func newFake(t *testing.T, handle func(s *fakeServer, c net.Conn)) *fakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, ln: ln}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepts.Add(1)
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer c.Close()
				c.SetDeadline(time.Now().Add(ioDeadline))
				handle(s, c)
			}()
		}
	}()
	t.Cleanup(func() { ln.Close(); s.wg.Wait() })
	return s
}

func (s *fakeServer) hostport() string { return s.ln.Addr().String() }

// readRequest reads one whole frame from the client and records its raw bytes.
func (s *fakeServer) readRequest(c net.Conn) []byte {
	hdr := make([]byte, 8)
	if _, err := readFull(c, hdr); err != nil {
		s.t.Errorf("fake server: reading request header: %v", err)
		return nil
	}
	body := make([]byte, binary.BigEndian.Uint32(hdr[4:]))
	if _, err := readFull(c, body); err != nil {
		s.t.Errorf("fake server: reading request payload: %v", err)
		return nil
	}
	raw := append(hdr, body...)
	s.mu.Lock()
	s.reqs = append(s.reqs, raw)
	s.mu.Unlock()
	return raw
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// scripted reads one request, then writes the next scripted bytes, once per element.
func scripted(replies ...[]byte) func(*fakeServer, net.Conn) {
	return func(s *fakeServer, c net.Conn) {
		for _, r := range replies {
			if s.readRequest(c) == nil {
				return
			}
			if _, err := c.Write(r); err != nil {
				s.t.Errorf("fake server: write: %v", err)
				return
			}
		}
	}
}

func (s *fakeServer) requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.reqs...)
}

func (s *fakeServer) expectRequests(paths ...string) {
	s.t.Helper()
	got := s.requests()
	if len(got) != len(paths) {
		s.t.Fatalf("server got %d requests, want %d", len(got), len(paths))
	}
	for i, p := range paths {
		if want := wantRequest(s.hostport(), p); !bytes.Equal(got[i], want) {
			s.t.Errorf("request %d (%s):\n got  %x\n want %x", i, p, got[i], want)
		}
	}
}

// run starts bcurl and returns what it wrote and how it exited.
func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		t.Fatalf("bcurl hung: stdout=%q stderr=%q", so.String(), se.String())
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatal(err)
	}
	return so.String(), se.String(), code
}

// --- the cases ---

func TestOneResponseTwoDataFrames(t *testing.T) {
	srv := newFake(t, scripted(
		append(respFrame(200, 0, idHdr(5, "text/plain")), append(dataFrame(0, "hello, "), dataFrame(flagEnd, "world")...)...),
	))
	out, _, code := run(t, srv.hostport()+"/hello.txt")
	if code != 0 || out != "hello, world" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	srv.expectRequests("/hello.txt")
}

func TestEmptyBody(t *testing.T) {
	tests := map[string][]byte{
		"RESPONSE with END":               respFrame(200, flagEnd),
		"zero-length DATA with END":       append(respFrame(200, 0), dataFrame(flagEnd, "")...),
		"DATA, then zero-length DATA END": append(respFrame(200, 0), append(dataFrame(0, ""), dataFrame(flagEnd, "")...)...),
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			srv := newFake(t, scripted(reply))
			out, _, code := run(t, srv.hostport()+"/empty")
			if code != 0 || out != "" {
				t.Fatalf("code=%d stdout=%q", code, out)
			}
		})
	}
}

func TestThreePathsOneConnection(t *testing.T) {
	srv := newFake(t, scripted(
		append(respFrame(200, 0), dataFrame(flagEnd, "AAA")...),
		append(respFrame(200, 0), dataFrame(flagEnd, "BB")...),
		respFrame(200, flagEnd, idHdr(6, "0")),
	))
	// The third body is empty, so the output is exactly the first two bodies.
	out, _, code := run(t, srv.hostport()+"/a", "/b", "/c")
	if code != 0 || out != "AAABB" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	if n := srv.accepts.Load(); n != 1 {
		t.Fatalf("server accepted %d connections, want 1", n)
	}
	srv.expectRequests("/a", "/b", "/c")
}

func TestThreeBodiesJoined(t *testing.T) {
	srv := newFake(t, scripted(
		append(respFrame(200, 0), dataFrame(flagEnd, "one|")...),
		append(respFrame(200, 0), dataFrame(flagEnd, "two|")...),
		append(respFrame(200, 0), dataFrame(flagEnd, "three")...),
	))
	out, _, code := run(t, srv.hostport()+"/a", "/b", "/c")
	if code != 0 || out != "one|two|three" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
	if n := srv.accepts.Load(); n != 1 {
		t.Fatalf("server accepted %d connections, want 1", n)
	}
}

func TestExitCodesFromStatus(t *testing.T) {
	tests := []struct {
		name     string
		statuses []uint16
		want     int
	}{
		{"200", []uint16{200}, 0},
		{"301", []uint16{301}, 0},
		{"400", []uint16{400}, 4},
		{"404", []uint16{404}, 4},
		{"405", []uint16{405}, 4},
		{"500", []uint16{500}, 5},
		{"404 then 500", []uint16{404, 500}, 5},
		{"500 then 404", []uint16{500, 404}, 5},
		{"404 then 200", []uint16{404, 200}, 4},
		{"200 then 404 then 200", []uint16{200, 404, 200}, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var replies [][]byte
			paths := []string{}
			for i, st := range tc.statuses {
				replies = append(replies, append(respFrame(st, 0), dataFrame(flagEnd, fmt.Sprintf("b%d;", i))...))
				paths = append(paths, fmt.Sprintf("/p%d", i))
			}
			srv := newFake(t, scripted(replies...))
			out, _, code := run(t, append([]string{srv.hostport() + paths[0]}, paths[1:]...)...)
			if code != tc.want {
				t.Fatalf("exit %d, want %d", code, tc.want)
			}
			// README: every response body goes to stdout, an error body too.
			var wantOut string
			for i := range tc.statuses {
				wantOut += fmt.Sprintf("b%d;", i)
			}
			if out != wantOut {
				t.Fatalf("stdout=%q, want %q", out, wantOut)
			}
		})
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // the port is free and nothing listens on it
	_, _, code := run(t, addr+"/x")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
}

func TestUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"no arguments": nil,
		"only -v":      {"-v"},
		"unknown flag": {"-z", "127.0.0.1:1/x"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, code := run(t, args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
		})
	}
}

func TestSkipsUnknownFrameAndUnknownHeader(t *testing.T) {
	srv := newFake(t, scripted(bytes.Join([][]byte{
		// SPEC.md section 3: skip IDs 11 to 255 by value length.
		respFrame(200, 0, idHdr(200, "unknown id"), idHdr(5, "text/plain"), litHdr("x-extra", "y")),
		frm(0x7F, flagEnd, []byte("ignore me")), // an unknown type with END set must not end the body
		dataFrame(0, "ab"),
		frm(0x55, 0, nil),
		dataFrame(flagEnd, "cd"),
	}, nil)))
	out, _, code := run(t, srv.hostport()+"/x")
	if code != 0 || out != "abcd" {
		t.Fatalf("code=%d stdout=%q", code, out)
	}
}

func TestProtocolErrors(t *testing.T) {
	tests := map[string][]byte{
		"DATA before RESPONSE":         dataFrame(flagEnd, "x"),
		"version 2 from the server":    {2, typeResponse, flagEnd, 0, 0, 0, 0, 0},
		"length over the cap":          {1, typeData, 0, 0, 0xFF, 0xFF, 0xFF, 0xFF},
		"close before RESPONSE":        nil,
		"EOF between frames in a body": append(respFrame(200, 0), dataFrame(0, "abc")...),
		"EOF inside a DATA frame":      append(respFrame(200, 0), []byte{1, typeData, 0, 0, 0, 0, 0, 10, 'a', 'b', 'c'}...),
		"EOF inside a RESPONSE frame":  {1, typeResponse, flagEnd, 0, 0, 0, 0, 9, 0, 200},
	}
	for name, reply := range tests {
		t.Run(name, func(t *testing.T) {
			srv := newFake(t, scripted(reply))
			_, _, code := run(t, srv.hostport()+"/x")
			if code != 1 {
				t.Fatalf("exit %d, want 1", code)
			}
		})
	}
}

// dumpBlock is the hexdump of b in encoding/hex.Dumper form, with prefix before every line.
func dumpBlock(prefix string, b []byte) string {
	var sb strings.Builder
	d := hex.Dumper(&sb)
	d.Write(b)
	d.Close()
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

func TestVerbose(t *testing.T) {
	respHead := respFrame(200, 0, idHdr(5, "text/plain"))
	srv := newFake(t, scripted(append(append([]byte(nil), respHead...), dataFrame(flagEnd, "hi")...)))
	out, errOut, code := run(t, "-v", srv.hostport()+"/index.html")
	if code != 0 {
		t.Fatalf("exit %d, stderr=%q", code, errOut)
	}
	if out != "hi" {
		t.Fatalf("stdout=%q, want only the body", out)
	}
	req := wantRequest(srv.hostport(), "/index.html")
	for _, want := range []string{
		fmt.Sprintf("> REQUEST flags=0x01 length=%d", len(req)-8),
		fmt.Sprintf("< RESPONSE flags=0x00 length=%d status=200", len(respHead)-8),
		dumpBlock("> ", req),
		dumpBlock("< ", respHead),
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q\nstderr:\n%s", want, errOut)
		}
	}
	srv.expectRequests("/index.html")
}
