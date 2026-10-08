// Package frame encodes and decodes BH/1 frames and payloads, as SPEC.md defines them.
// The API in this file is frozen: tests and both programs build against it.
package frame

import (
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const (
	Version    = 0x01
	HeaderLen  = 8
	MaxPayload = 1<<24 - 1 // SPEC.md section 1: the cap is a rule, not a width.

	TypeRequest  = 0x01
	TypeResponse = 0x02
	TypeData     = 0x03

	FlagEnd = 0x01

	MethodGet = 0x01

	MaxDataChunk = 65536 // SPEC.md section 4: the largest DATA payload a sender uses.
)

var (
	// ErrBadVersion and ErrTooLarge mean the header is bad, so the frame boundary is lost.
	ErrBadVersion = errors.New("frame: version is not 1")
	ErrTooLarge   = errors.New("frame: length over the cap")
	// ErrMalformed means a payload is bad, but the frame boundary is still known.
	ErrMalformed = errors.New("frame: malformed payload")
)

// Frame is one frame. Reserved is never exposed: Write sends 0, Read ignores it.
type Frame struct {
	Type    uint8
	Flags   uint8
	Payload []byte
}

// Read reads one whole frame of any type, known or not.
// It returns io.EOF at a clean frame boundary, io.ErrUnexpectedEOF inside a frame,
// and ErrBadVersion or ErrTooLarge for a bad header (checked before the payload is read).
func Read(r io.Reader) (Frame, error) {
	var h [HeaderLen]byte
	// ReadFull gives io.EOF only when it read 0 bytes: a clean frame boundary.
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	if h[0] != Version {
		return Frame{}, ErrBadVersion
	}
	n := binary.BigEndian.Uint32(h[4:])
	if n > MaxPayload {
		return Frame{}, ErrTooLarge
	}
	f := Frame{Type: h[1], Flags: h[2]}
	if n == 0 {
		return f, nil
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		if err == io.EOF { // EOF inside a frame is an error, not a clean close.
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return f, nil
}

// Write writes one frame. It returns ErrTooLarge if the payload is over MaxPayload.
func Write(w io.Writer, f Frame) error {
	if len(f.Payload) > MaxPayload {
		return ErrTooLarge
	}
	// One Write call, so a frame is not split across TCP segments without need.
	buf := make([]byte, HeaderLen+len(f.Payload))
	buf[0], buf[1], buf[2] = Version, f.Type, f.Flags
	binary.BigEndian.PutUint32(buf[4:], uint32(len(f.Payload)))
	copy(buf[HeaderLen:], f.Payload)
	_, err := w.Write(buf)
	return err
}

type Header struct{ Name, Value string }

// EncodeHeaders uses a table ID for the ten names in SPEC.md section 3, a literal for any other.
func EncodeHeaders(hs []Header) []byte {
	var b []byte
	for _, h := range hs {
		if id := tableID(h.Name); id != 0 {
			b = append(b, id)
		} else {
			b = append(b, 0, byte(len(h.Name)))
			b = append(b, h.Name...)
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(h.Value)))
		b = append(b, h.Value...)
	}
	return b
}

// headerNames is the SPEC.md section 3 table. The ID is the index plus one.
var headerNames = [...]string{
	"host", "user-agent", "accept", "connection", "content-type",
	"content-length", "server", "date", "last-modified", "allow",
}

// tableID returns 0 when the name is not in the table.
func tableID(name string) byte {
	for i, n := range headerNames {
		if n == name {
			return byte(i + 1)
		}
	}
	return 0
}

// take returns the first n bytes of b and the rest, or ok=false if b is short.
func take(b []byte, n int) (head, rest []byte, ok bool) {
	if len(b) < n {
		return nil, nil, false
	}
	return b[:n], b[n:], true
}

// DecodeHeaders maps table IDs to names, skips IDs 11 to 255, and returns ErrMalformed on a truncated entry or a literal name length of 0.
func DecodeHeaders(b []byte) ([]Header, error) {
	var hs []Header
	for len(b) > 0 {
		id := b[0]
		b = b[1:]
		var name string
		switch {
		case id == 0:
			l, rest, ok := take(b, 1)
			if !ok || l[0] == 0 {
				return nil, ErrMalformed
			}
			nb, rest, ok := take(rest, int(l[0]))
			if !ok {
				return nil, ErrMalformed
			}
			name, b = string(nb), rest
		case int(id) <= len(headerNames):
			name = headerNames[id-1]
		}
		l, rest, ok := take(b, 2)
		if !ok {
			return nil, ErrMalformed
		}
		v, rest, ok := take(rest, int(binary.BigEndian.Uint16(l)))
		if !ok {
			return nil, ErrMalformed
		}
		b = rest
		if name != "" { // an unknown ID 11 to 255 has no name: skip it.
			hs = append(hs, Header{name, string(v)})
		}
	}
	return hs, nil
}

type Request struct {
	Method  uint8
	Path    string
	Headers []Header
}

func (r Request) Encode() []byte {
	b := []byte{r.Method}
	b = binary.BigEndian.AppendUint16(b, uint16(len(r.Path)))
	b = append(b, r.Path...)
	return append(b, EncodeHeaders(r.Headers)...)
}

// DecodeRequest returns ErrMalformed for any rule in SPEC.md section 3. It does not check the method.
func DecodeRequest(p []byte) (Request, error) {
	if len(p) < 3 {
		return Request{}, ErrMalformed
	}
	path, rest, ok := take(p[3:], int(binary.BigEndian.Uint16(p[1:3])))
	if !ok || len(path) == 0 || path[0] != '/' || strings.IndexByte(string(path), 0) >= 0 {
		return Request{}, ErrMalformed
	}
	for _, seg := range strings.Split(string(path), "/") {
		if seg == ".." {
			return Request{}, ErrMalformed
		}
	}
	hs, err := DecodeHeaders(rest)
	if err != nil {
		return Request{}, err
	}
	return Request{Method: p[0], Path: string(path), Headers: hs}, nil
}

type Response struct {
	Status  uint16
	Headers []Header
}

func (r Response) Encode() []byte {
	b := binary.BigEndian.AppendUint16(nil, r.Status)
	return append(b, EncodeHeaders(r.Headers)...)
}

func DecodeResponse(p []byte) (Response, error) {
	if len(p) < 2 {
		return Response{}, ErrMalformed
	}
	hs, err := DecodeHeaders(p[2:])
	if err != nil {
		return Response{}, err
	}
	return Response{Status: binary.BigEndian.Uint16(p), Headers: hs}, nil
}
