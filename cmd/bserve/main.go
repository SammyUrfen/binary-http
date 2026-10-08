// Command bserve serves the files under a root folder over BH/1 (SPEC.md).
//
//	bserve ROOT PORT
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/SammyUrfen/binary-http/internal/frame"
)

const (
	// idleTimeout is the SPEC.md section 5 limit on silence between frames.
	idleTimeout = 30 * time.Second
	// writeTimeout bounds each write to the socket. A client that stops reading
	// fills the socket buffers, the write blocks, and the deadline frees the goroutine.
	// A slow client still gets a large file, because every write gets a fresh deadline.
	writeTimeout = 30 * time.Second
	// acceptBackoff pauses the accept loop after an error such as EMFILE.
	acceptBackoff = 50 * time.Millisecond
	// httpDate is the HTTP date layout. net/http is off limits, so it is copied here.
	httpDate      = "Mon, 02 Jan 2006 15:04:05 GMT"
	serverName    = "bserve/1"
	defaultType   = "application/octet-stream"
	indexFile     = "index.html"
	exitUsage     = 2
	exitStartFail = 1
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: bserve ROOT PORT")
		os.Exit(exitUsage)
	}
	if _, err := strconv.ParseUint(os.Args[2], 10, 16); err != nil {
		fmt.Fprintf(os.Stderr, "bserve: bad port %q\n", os.Args[2])
		os.Exit(exitUsage)
	}
	// os.Root refuses every path that leaves the root, also through a symbolic link,
	// so the server does not need its own EvalSymlinks check.
	root, err := os.OpenRoot(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bserve:", err)
		os.Exit(exitStartFail)
	}
	ln, err := net.Listen("tcp", ":"+os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "bserve:", err)
		os.Exit(exitStartFail)
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			// EMFILE and similar errors repeat at once. A short pause stops a hot loop.
			log.Print("accept: ", err)
			time.Sleep(acceptBackoff)
			continue
		}
		go serve(c, root)
	}
}

// serve answers the requests on one connection in order, one at a time.
func serve(c net.Conn, root *os.Root) {
	defer c.Close()
	br, bw := bufio.NewReader(c), bufio.NewWriter(deadlineWriter{c})
	remote := c.RemoteAddr().String()
	for {
		if err := c.SetReadDeadline(time.Now().Add(idleTimeout)); err != nil {
			return
		}
		// Keep only REQUEST payloads: a skipped frame of 16 MiB costs no memory.
		f, err := frame.ReadKeep(br, isRequest)
		switch {
		case errors.Is(err, frame.ErrBadVersion), errors.Is(err, frame.ErrTooLarge):
			// The frame boundary is lost, so say 400 once and close.
			log.Printf("%s - - 400", remote)
			reply(bw, 400, frame.Header{Name: "connection", Value: "close"})
			bw.Flush()
			return
		case err != nil:
			// A clean EOF, an EOF inside a frame, or an idle timeout: close and send nothing.
			return
		}
		if f.Type != frame.TypeRequest {
			continue // SPEC.md section 2: the skip rule.
		}
		req, err := frame.DecodeRequest(f.Payload)
		var status int
		var sendErr error
		switch {
		case err != nil:
			status = 400
			reply(bw, status)
		case req.Method != frame.MethodGet:
			status = 405
			reply(bw, status, frame.Header{Name: "allow", Value: "GET"})
		default:
			status, sendErr = sendFile(bw, root, req.Path)
		}
		log.Printf("%s %s %q %d", remote, methodName(req.Method), req.Path, status)
		if sendErr != nil {
			return // The 200 is out, so a broken body can only end with a drop.
		}
		if bw.Flush() != nil {
			return
		}
	}
}

func isRequest(typ uint8) bool { return typ == frame.TypeRequest }

// deadlineWriter gives each write to the connection a fresh writeTimeout.
type deadlineWriter struct{ net.Conn }

func (w deadlineWriter) Write(p []byte) (int, error) {
	if err := w.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return 0, err
	}
	return w.Conn.Write(p)
}

func methodName(m uint8) string {
	if m == frame.MethodGet {
		return "GET"
	}
	return "0x" + strconv.FormatUint(uint64(m), 16)
}

// reply writes a RESPONSE with END and no body. A write error shows at the next Flush.
func reply(w io.Writer, status int, hs ...frame.Header) {
	hs = append([]frame.Header{{Name: "server", Value: serverName}}, hs...)
	frame.Write(w, frame.Frame{Type: frame.TypeResponse, Flags: frame.FlagEnd,
		Payload: frame.Response{Status: uint16(status), Headers: hs}.Encode()})
}

// sendFile answers a GET. A non-nil error means the connection must drop.
func sendFile(w io.Writer, root *os.Root, p string) (int, error) {
	name := strings.TrimPrefix(p, "/")
	if name == "" || strings.HasSuffix(name, "/") {
		name += indexFile
	}
	// Stat before Open: opening a FIFO or a device for read can block forever.
	if st, err := root.Stat(name); err == nil && !st.Mode().IsRegular() {
		reply(w, 404)
		return 404, nil
	}
	file, err := root.Open(name)
	if err != nil {
		// Missing, not a folder, or out of the root are all 404. Only a refused read is 500.
		if errors.Is(err, fs.ErrPermission) {
			reply(w, 500)
			return 500, nil
		}
		reply(w, 404)
		return 404, nil
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		reply(w, 500)
		return 500, nil
	}
	if !st.Mode().IsRegular() {
		reply(w, 404)
		return 404, nil
	}

	ctype := mime.TypeByExtension(path.Ext(name))
	if ctype == "" {
		ctype = defaultType
	}
	size := st.Size()
	var flags uint8
	if size == 0 {
		flags = frame.FlagEnd
	}
	hs := []frame.Header{
		{Name: "server", Value: serverName},
		{Name: "content-type", Value: ctype},
		{Name: "content-length", Value: strconv.FormatInt(size, 10)},
		{Name: "date", Value: time.Now().UTC().Format(httpDate)},
		{Name: "last-modified", Value: st.ModTime().UTC().Format(httpDate)},
	}
	if err := frame.Write(w, frame.Frame{Type: frame.TypeResponse, Flags: flags,
		Payload: frame.Response{Status: 200, Headers: hs}.Encode()}); err != nil {
		return 200, err
	}

	// Send exactly the stat size, so END and content-length agree. A file that
	// shrinks under us gives a short read, and the connection drops.
	buf := make([]byte, frame.MaxDataChunk)
	for left := size; left > 0; {
		n := min(left, int64(len(buf)))
		if _, err := io.ReadFull(file, buf[:n]); err != nil {
			return 200, err
		}
		left -= n
		flags = 0
		if left == 0 {
			flags = frame.FlagEnd
		}
		if err := frame.Write(w, frame.Frame{Type: frame.TypeData, Flags: flags, Payload: buf[:n]}); err != nil {
			return 200, err
		}
	}
	return 200, nil
}
