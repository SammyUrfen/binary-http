// bcurl fetches files from a bserve server over one BH/1 connection.
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/SammyUrfen/binary-http/internal/frame"
)

const (
	readTimeout = 10 * time.Second // a dead server must not hang the client

	exitProtocol = 1
	exitUsage    = 2
	exitClient   = 4
	exitServer   = 5
)

var verbose bool

// show writes the summary line and hexdump of one frame. The bytes come from frame.Write,
// so they are exactly what goes on (or came off) the wire; the reserved byte is always 0.
func show(prefix, summary, extra string, f frame.Frame) {
	if !verbose {
		return
	}
	var buf bytes.Buffer
	_ = frame.Write(&buf, f)
	fmt.Fprintf(os.Stderr, "%s%s flags=0x%02x length=%d%s\n", prefix, summary, f.Flags, len(f.Payload), extra)
	d := hex.Dumper(&prefixWriter{prefix})
	d.Write(buf.Bytes())
	d.Close()
}

// prefixWriter puts prefix at the start of every line that hex.Dumper writes to stderr.
type prefixWriter struct {
	prefix string
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	for _, line := range strings.SplitAfter(string(b), "\n") {
		if line != "" {
			fmt.Fprint(os.Stderr, p.prefix+line)
		}
	}
	return len(b), nil
}

func fail(code int, format string, a ...any) {
	fmt.Fprintf(os.Stderr, "bcurl: "+format+"\n", a...)
	os.Exit(code)
}

func main() {
	flag.BoolVar(&verbose, "v", false, "dump every frame to stderr")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: bcurl [-v] HOST:PORT/PATH [PATH ...]") }
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(exitUsage)
	}
	hostport, first, ok := strings.Cut(flag.Arg(0), "/")
	if !ok || hostport == "" {
		flag.Usage()
		os.Exit(exitUsage)
	}
	paths := append([]string{"/" + first}, flag.Args()[1:]...)

	conn, err := net.Dial("tcp", hostport)
	if err != nil {
		fail(exitProtocol, "%v", err)
	}
	defer conn.Close()

	worst := 0
	for _, p := range paths {
		status := exchange(conn, hostport, p)
		switch {
		case status >= 500:
			worst = exitServer
		case status >= 400 && worst != exitServer:
			worst = exitClient
		}
	}
	os.Exit(worst)
}

// exchange sends one GET, copies the body to stdout and returns the status.
// A protocol error ends the program with exit 1.
func exchange(conn net.Conn, hostport, path string) uint16 {
	req := frame.Request{Method: frame.MethodGet, Path: path, Headers: []frame.Header{
		{Name: "host", Value: hostport},
		{Name: "user-agent", Value: "bcurl/1"},
		{Name: "accept", Value: "*/*"},
	}}
	out := frame.Frame{Type: frame.TypeRequest, Flags: frame.FlagEnd, Payload: req.Encode()}
	show("> ", "REQUEST", "", out)
	if err := frame.Write(conn, out); err != nil {
		fail(exitProtocol, "send: %v", err)
	}

	var status uint16
	gotResponse := false
	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		f, err := frame.Read(conn)
		if err != nil {
			fail(exitProtocol, "read: %v", err)
		}
		switch f.Type {
		case frame.TypeResponse:
			if gotResponse {
				fail(exitProtocol, "second RESPONSE inside a response")
			}
			r, err := frame.DecodeResponse(f.Payload)
			if err != nil {
				fail(exitProtocol, "bad RESPONSE: %v", err)
			}
			status, gotResponse = r.Status, true
			show("< ", "RESPONSE", fmt.Sprintf(" status=%d", r.Status), f)
		case frame.TypeData:
			if !gotResponse {
				fail(exitProtocol, "DATA before RESPONSE")
			}
			show("< ", "DATA", "", f)
			os.Stdout.Write(f.Payload)
		default:
			show("< ", fmt.Sprintf("UNKNOWN(0x%02x)", f.Type), "", f)
			continue // SPEC.md section 2: skip unknown types, and ignore their END flag
		}
		if f.Flags&frame.FlagEnd != 0 {
			return status
		}
	}
}
