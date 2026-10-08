// bcurl fetches files from a bserve server over one BH/1 connection.
package main

import (
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
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

	// maxPath is the largest path the 2-byte path length of SPEC.md section 3 can carry.
	maxPath = 1<<16 - 1
	// README.md maps only 1xx to 5xx to exit codes. Any other status is a protocol error.
	minStatus = 100
	maxStatus = 599
)

var verbose bool

// show writes the summary line and hexdump of one frame. raw is the frame exactly as it
// went on or came off the wire, so a received reserved byte or flag bit shows as sent.
func show(prefix, summary, extra string, raw []byte) {
	if !verbose {
		return
	}
	fmt.Fprintf(os.Stderr, "%s%s flags=0x%02x length=%d%s\n", prefix, summary, raw[2], len(raw)-frame.HeaderLen, extra)
	var dump strings.Builder
	d := hex.Dumper(&dump)
	d.Write(raw)
	d.Close()
	for _, line := range strings.SplitAfter(dump.String(), "\n") {
		if line != "" {
			fmt.Fprint(os.Stderr, prefix+line)
		}
	}
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
	for _, p := range paths {
		if len(p) > maxPath {
			fail(exitUsage, "path is %d bytes, over the %d-byte limit", len(p), maxPath)
		}
	}

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
	// A request is far under the cap, so Write to a buffer cannot fail.
	var out bytes.Buffer
	frame.Write(&out, frame.Frame{Type: frame.TypeRequest, Flags: frame.FlagEnd, Payload: req.Encode()})
	show("> ", "REQUEST", "", out.Bytes())
	if _, err := conn.Write(out.Bytes()); err != nil {
		fail(exitProtocol, "send: %v", err)
	}

	// With -v, raw records each received frame as it comes off the wire.
	var raw bytes.Buffer
	var in io.Reader = conn
	if verbose {
		in = io.TeeReader(conn, &raw)
	}

	var status uint16
	gotResponse := false
	for {
		conn.SetReadDeadline(time.Now().Add(readTimeout))
		raw.Reset()
		// Skip unknown types without keeping them: a 16 MiB one costs no memory.
		f, err := frame.ReadKeep(in, isBody)
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
			show("< ", "RESPONSE", fmt.Sprintf(" status=%d", r.Status), raw.Bytes())
			if status < minStatus || status > maxStatus {
				fail(exitProtocol, "status %d is not 1xx to 5xx", status)
			}
		case frame.TypeData:
			if !gotResponse {
				fail(exitProtocol, "DATA before RESPONSE")
			}
			show("< ", "DATA", "", raw.Bytes())
			os.Stdout.Write(f.Payload)
		default:
			show("< ", fmt.Sprintf("UNKNOWN(0x%02x)", f.Type), "", raw.Bytes())
			continue // SPEC.md section 2: skip unknown types, and ignore their END flag
		}
		if f.Flags&frame.FlagEnd != 0 {
			return status
		}
	}
}

func isBody(typ uint8) bool { return typ == frame.TypeResponse || typ == frame.TypeData }
