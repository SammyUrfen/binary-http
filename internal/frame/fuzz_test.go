package frame_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"

	. "github.com/SammyUrfen/binary-http/internal/frame"
)

// allocSlack is the most a call can allocate beyond its input. It is far under the
// 16 MiB that a hostile length field asks for, and far over the small decode buffers.
const allocSlack = 1 << 20

// allocated returns the bytes that fn allocates on the heap.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func hdr(typ, flags byte, n uint32) []byte {
	return binary.BigEndian.AppendUint32([]byte{Version, typ, flags, 0}, n)
}

// A length field is a claim, not a fact. Read must not trust it with memory
// before the bytes arrive: 500 connections that each claim 16 MiB must not cost 8 GB.
func TestReadAllocatesOnlyWhatArrives(t *testing.T) {
	in := append(hdr(TypeData, 0, MaxPayload), "only ten b"...)
	var err error
	got := allocated(func() { _, err = Read(bytes.NewReader(in)) })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
	if got > allocSlack {
		t.Fatalf("Read allocated %d bytes for an 18-byte input", got)
	}
}

// SPEC.md section 2: a skipped frame is read and discarded. It must cost no memory.
func TestReadKeepDiscardsSkippedPayload(t *testing.T) {
	const unknown = 0x7f
	in := io.MultiReader(
		bytes.NewReader(hdr(unknown, FlagEnd, MaxPayload)),
		io.LimitReader(zeros{}, MaxPayload),
		bytes.NewReader(append(hdr(TypeRequest, FlagEnd, 3), 1, 0, 0)),
	)
	keepRequest := func(typ uint8) bool { return typ == TypeRequest }
	var f Frame
	var err error
	got := allocated(func() { f, err = ReadKeep(in, keepRequest) })
	if err != nil || f.Type != unknown || f.Flags != FlagEnd || f.Payload != nil {
		t.Fatalf("got type 0x%02x flags 0x%02x payload %d bytes (nil %t), err %v, want an unknown frame with a nil payload",
			f.Type, f.Flags, len(f.Payload), f.Payload == nil, err)
	}
	if got > allocSlack {
		t.Fatalf("skipping a 16 MiB frame allocated %d bytes", got)
	}
	f, err = ReadKeep(in, keepRequest)
	if err != nil || f.Type != TypeRequest || !bytes.Equal(f.Payload, []byte{1, 0, 0}) {
		t.Fatalf("next frame: got type 0x%02x payload % x, err %v", f.Type, f.Payload, err)
	}
	if _, err = ReadKeep(in, keepRequest); err != io.EOF {
		t.Fatalf("after the last frame: err = %v, want io.EOF", err)
	}
}

func TestReadKeepTruncatedSkip(t *testing.T) {
	in := append(hdr(0x7f, 0, 100), make([]byte, 10)...)
	if _, err := ReadKeep(bytes.NewReader(in), func(uint8) bool { return false }); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

// A path of 65,535 slashes is 64 KiB of input. Splitting it into 65,536 strings costs 1 MiB.
func TestDecodeRequestLongPathAllocation(t *testing.T) {
	p := Request{Method: MethodGet, Path: strings.Repeat("/", 1<<16-1)}.Encode()
	var err error
	got := allocated(func() { _, err = DecodeRequest(p) })
	if err != nil {
		t.Fatal(err)
	}
	if got > 4*uint64(len(p)) {
		t.Fatalf("DecodeRequest allocated %d bytes for a %d-byte payload", got, len(p))
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// ---- fuzz targets: no panic, and every result fits in its input ----

func FuzzRead(f *testing.F) {
	f.Add(mustHexF(specExample))
	f.Add(hdr(TypeData, FlagEnd, 0))
	f.Add(hdr(TypeData, 0, MaxPayload))
	f.Add(hdr(0x7f, 0xff, 1<<32-1))
	f.Add([]byte{2, 1, 1, 0, 0, 0, 0, 0})
	f.Add([]byte("GET / HTTP/1.1\r\n\r\n"))
	f.Fuzz(func(t *testing.T, in []byte) {
		r := bytes.NewReader(in)
		for {
			f, err := Read(r)
			if err != nil {
				return
			}
			if len(f.Payload) > len(in)-HeaderLen {
				t.Fatalf("payload %d bytes from a %d-byte input", len(f.Payload), len(in))
			}
		}
	})
}

func FuzzReadKeep(f *testing.F) {
	f.Add(mustHexF(specExample))
	f.Add(append(hdr(0x7f, 0, 3), 1, 2, 3))
	f.Fuzz(func(t *testing.T, in []byte) {
		r := bytes.NewReader(in)
		for {
			f, err := ReadKeep(r, func(typ uint8) bool { return typ == TypeRequest })
			if err != nil {
				return
			}
			if f.Type != TypeRequest && f.Payload != nil {
				t.Fatalf("type 0x%02x kept a payload", f.Type)
			}
		}
	})
}

func FuzzDecodeRequest(f *testing.F) {
	f.Add(mustHexF(specExample)[HeaderLen:])
	f.Add([]byte{1, 0, 3, '/', '.', '.'})
	f.Add([]byte{1, 0, 2, '/', 0})
	f.Add([]byte{1, 0xff, 0xff, '/'})
	f.Add([]byte{1, 0, 1, '/', 0, 0})
	f.Add([]byte{1, 0, 1, '/', 200, 0, 1, 'x'})
	f.Fuzz(func(t *testing.T, p []byte) {
		r, err := DecodeRequest(p)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			return
		}
		if !strings.HasPrefix(r.Path, "/") || strings.IndexByte(r.Path, 0) >= 0 {
			t.Fatalf("accepted bad path %q", r.Path)
		}
		for _, seg := range strings.Split(r.Path, "/") {
			if seg == ".." {
				t.Fatalf("accepted a .. segment in %q", r.Path)
			}
		}
		if n := 3 + len(r.Path) + headerBytes(r.Headers); n > len(p) {
			t.Fatalf("decoded %d bytes from a %d-byte payload", n, len(p))
		}
	})
}

func FuzzDecodeResponse(f *testing.F) {
	f.Add([]byte{0, 200})
	f.Add([]byte{0})
	f.Add(Response{Status: 404, Headers: []Header{{"server", "x"}, {"x-a", "b"}}}.Encode())
	f.Fuzz(func(t *testing.T, p []byte) {
		r, err := DecodeResponse(p)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			return
		}
		if r.Status != binary.BigEndian.Uint16(p) {
			t.Fatalf("status %d, bytes % x", r.Status, p[:2])
		}
		if n := 2 + headerBytes(r.Headers); n > len(p) {
			t.Fatalf("decoded %d bytes from a %d-byte payload", n, len(p))
		}
	})
}

func FuzzDecodeHeaders(f *testing.F) {
	f.Add(EncodeHeaders([]Header{{"host", "a"}, {"x-y", "z"}}))
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 1})
	f.Add([]byte{11, 0, 1, 'x'})
	f.Add([]byte{255, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, b []byte) {
		hs, err := DecodeHeaders(b)
		if err != nil {
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
			return
		}
		if n := headerBytes(hs); n > len(b) {
			t.Fatalf("decoded %d bytes from a %d-byte block", n, len(b))
		}
		for _, h := range hs {
			if h.Name == "" {
				t.Fatal("decoded an entry with an empty name")
			}
		}
	})
}

// headerBytes is the least number of wire bytes that hs needs: one ID byte and a
// two-byte value length for each entry, plus the value. A table name costs no name bytes.
func headerBytes(hs []Header) int {
	n := 0
	for _, h := range hs {
		n += 3 + len(h.Value)
	}
	return n
}

func mustHexF(s string) []byte {
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		panic(err)
	}
	return b
}
