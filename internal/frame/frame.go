// Package frame encodes and decodes BH/1 frames and payloads, as SPEC.md defines them.
// The API in this file is frozen: tests and both programs build against it.
package frame

import (
	"errors"
	"io"
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
func Read(r io.Reader) (Frame, error) { panic("todo") }

// Write writes one frame. It returns ErrTooLarge if the payload is over MaxPayload.
func Write(w io.Writer, f Frame) error { panic("todo") }

type Header struct{ Name, Value string }

// EncodeHeaders uses a table ID for the ten names in SPEC.md section 3, a literal for any other.
func EncodeHeaders(hs []Header) []byte { panic("todo") }

// DecodeHeaders maps table IDs to names, skips IDs 11 to 255, and returns ErrMalformed on a truncated entry or a literal name length of 0.
func DecodeHeaders(b []byte) ([]Header, error) { panic("todo") }

type Request struct {
	Method  uint8
	Path    string
	Headers []Header
}

func (r Request) Encode() []byte { panic("todo") }

// DecodeRequest returns ErrMalformed for any rule in SPEC.md section 3. It does not check the method.
func DecodeRequest(p []byte) (Request, error) { panic("todo") }

type Response struct {
	Status  uint16
	Headers []Header
}

func (r Response) Encode() []byte { panic("todo") }

func DecodeResponse(p []byte) (Response, error) { panic("todo") }
