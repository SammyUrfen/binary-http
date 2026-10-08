// External test package: the helpers here cannot collide with unexported names in the implementation.
package frame_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	. "github.com/SammyUrfen/binary-http/internal/frame"
)

// specExample is the SPEC.md section 6 request, byte by byte.
const specExample = `
01 01 01 00 00 00 00 29
01
00 0b 2f 69 6e 64 65 78 2e 68 74 6d 6c
01 00 0e 6c 6f 63 61 6c 68 6f 73 74 3a 39 30 30 30
02 00 07 62 63 75 72 6c 2f 31`

// tableNames is the SPEC.md section 3 table. The ID is the index plus one.
var tableNames = []string{
	"host", "user-agent", "accept", "connection", "content-type",
	"content-length", "server", "date", "last-modified", "allow",
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatalf("bad hex in test: %v", err)
	}
	return b
}

// sameHeaders treats nil and empty as equal.
func sameHeaders(a, b []Header) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hdrBytes(typ, flags, reserved byte, length uint32) []byte {
	return []byte{Version, typ, flags, reserved, byte(length >> 24), byte(length >> 16), byte(length >> 8), byte(length)}
}

var specRequest = Request{
	Method: MethodGet,
	Path:   "/index.html",
	Headers: []Header{
		{"host", "localhost:9000"},
		{"user-agent", "bcurl/1"},
	},
}

func TestSpecExampleEncode(t *testing.T) {
	want := mustHex(t, specExample)
	var buf bytes.Buffer
	if err := Write(&buf, Frame{Type: TypeRequest, Flags: FlagEnd, Payload: specRequest.Encode()}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("got  %x\nwant %x", buf.Bytes(), want)
	}
}

func TestSpecExampleDecode(t *testing.T) {
	f, err := Read(bytes.NewReader(mustHex(t, specExample)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeRequest || f.Flags != FlagEnd || len(f.Payload) != 41 {
		t.Fatalf("got type=%d flags=%d len=%d", f.Type, f.Flags, len(f.Payload))
	}
	r, err := DecodeRequest(f.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if r.Method != MethodGet || r.Path != specRequest.Path || !sameHeaders(r.Headers, specRequest.Headers) {
		t.Fatalf("got %+v", r)
	}
}

func TestRead(t *testing.T) {
	tests := []struct {
		name    string
		in      []byte
		wantErr error
		want    Frame
	}{
		{"clean EOF", nil, io.EOF, Frame{}},
		{"EOF after 3 header bytes", []byte{1, 1, 1}, io.ErrUnexpectedEOF, Frame{}},
		{"EOF after 7 header bytes", hdrBytes(1, 0, 0, 0)[:7], io.ErrUnexpectedEOF, Frame{}},
		{"EOF in payload", append(hdrBytes(3, 0, 0, 5), 'a', 'b'), io.ErrUnexpectedEOF, Frame{}},
		{"EOF before payload", hdrBytes(3, 0, 0, 5), io.ErrUnexpectedEOF, Frame{}},
		{"version 2", []byte{2, 1, 1, 0, 0, 0, 0, 0}, ErrBadVersion, Frame{}},
		{"version 0", []byte{0, 1, 1, 0, 0, 0, 0, 0}, ErrBadVersion, Frame{}},
		{"text HTTP client", []byte("GET / HTTP/1.1\r\n"), ErrBadVersion, Frame{}},
		{"length 2^24, header only", hdrBytes(3, 0, 0, 1<<24), ErrTooLarge, Frame{}},
		{"length 2^32-1, header only", hdrBytes(3, 0, 0, 0xFFFFFFFF), ErrTooLarge, Frame{}},
		{"length at the cap is not too large", hdrBytes(3, 0, 0, MaxPayload), io.ErrUnexpectedEOF, Frame{}},
		{"nonzero reserved byte", append(hdrBytes(3, 0, 0xFF, 1), 'x'), nil, Frame{Type: TypeData, Payload: []byte("x")}},
		{"unknown type", append(hdrBytes(0x7F, FlagEnd, 0, 2), 'h', 'i'), nil, Frame{Type: 0x7F, Flags: FlagEnd, Payload: []byte("hi")}},
		{"zero length", hdrBytes(TypeResponse, FlagEnd, 0, 0), nil, Frame{Type: TypeResponse, Flags: FlagEnd}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Read(bytes.NewReader(tc.in))
			if tc.wantErr != nil {
				if err != tc.wantErr && !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if tc.wantErr == io.EOF && err != io.EOF {
					t.Fatalf("err = %v, want io.EOF itself", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.Type != tc.want.Type || f.Flags != tc.want.Flags || !bytes.Equal(f.Payload, tc.want.Payload) {
				t.Fatalf("got %+v, want %+v", f, tc.want)
			}
		})
	}
}

func TestReadSequenceOneByteAtATime(t *testing.T) {
	var in []byte
	in = append(in, hdrBytes(TypeResponse, 0, 0, 2)...)
	in = append(in, 0x00, 0xC8)
	in = append(in, hdrBytes(TypeData, FlagEnd, 0, 3)...)
	in = append(in, "abc"...)
	r := iotest.OneByteReader(bytes.NewReader(in))

	f1, err := Read(r)
	if err != nil || f1.Type != TypeResponse || !bytes.Equal(f1.Payload, []byte{0, 0xC8}) {
		t.Fatalf("frame 1: %+v, %v", f1, err)
	}
	f2, err := Read(r)
	if err != nil || f2.Type != TypeData || f2.Flags != FlagEnd || string(f2.Payload) != "abc" {
		t.Fatalf("frame 2: %+v, %v", f2, err)
	}
	if _, err := Read(r); err != io.EOF {
		t.Fatalf("frame 3: err = %v, want io.EOF", err)
	}
}

func TestWrite(t *testing.T) {
	tests := []struct {
		name string
		in   Frame
		want []byte
	}{
		{"data with END", Frame{Type: TypeData, Flags: FlagEnd, Payload: []byte("hi")}, []byte{1, 3, 1, 0, 0, 0, 0, 2, 'h', 'i'}},
		{"empty payload", Frame{Type: TypeResponse, Flags: FlagEnd}, []byte{1, 2, 1, 0, 0, 0, 0, 0}},
		{"unknown type, reserved byte stays 0", Frame{Type: 0x7F, Payload: []byte{9}}, []byte{1, 0x7F, 0, 0, 0, 0, 0, 1, 9}},
		{"length is big-endian", Frame{Type: TypeData, Payload: make([]byte, 0x010203)}, append([]byte{1, 3, 0, 0, 0, 1, 2, 3}, make([]byte, 0x010203)...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Write(&buf, tc.in); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(buf.Bytes(), tc.want) {
				t.Fatalf("got %x, want %x", buf.Bytes(), tc.want)
			}
		})
	}
}

func TestWriteCap(t *testing.T) {
	var buf bytes.Buffer
	err := Write(&buf, Frame{Type: TypeData, Payload: make([]byte, MaxPayload+1)})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("wrote %d bytes before it failed", buf.Len())
	}
	buf.Reset()
	if err := Write(&buf, Frame{Type: TypeData, Payload: make([]byte, MaxPayload)}); err != nil {
		t.Fatalf("payload at the cap: %v", err)
	}
	if buf.Len() != HeaderLen+MaxPayload {
		t.Fatalf("wrote %d bytes", buf.Len())
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 255, 256, MaxDataChunk, MaxDataChunk + 1} {
		p := bytes.Repeat([]byte{0xAB, 0x00, 0x01}, n/3+1)[:n]
		var buf bytes.Buffer
		in := Frame{Type: TypeData, Flags: FlagEnd, Payload: p}
		if err := Write(&buf, in); err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		out, err := Read(&buf)
		if err != nil || out.Type != in.Type || out.Flags != in.Flags || !bytes.Equal(out.Payload, p) {
			t.Fatalf("n=%d: got %+v, %v", n, out, err)
		}
	}
}

func TestEncodeHeadersTableUsesID(t *testing.T) {
	for i, name := range tableNames {
		got := EncodeHeaders([]Header{{name, "v"}})
		want := []byte{byte(i + 1), 0, 1, 'v'}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: got %x, want %x", name, got, want)
		}
	}
}

func TestEncodeHeadersLiteral(t *testing.T) {
	got := EncodeHeaders([]Header{{"x-foo", "bar"}})
	want := append([]byte{0, 5}, "x-foo"...)
	want = append(want, 0, 3)
	want = append(want, "bar"...)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
	if got := EncodeHeaders(nil); len(got) != 0 {
		t.Fatalf("no headers should encode to nothing, got %x", got)
	}
}

func TestHeadersRoundTrip(t *testing.T) {
	var all []Header
	for _, n := range tableNames {
		all = append(all, Header{n, "value of " + n})
	}
	tests := map[string][]Header{
		"none":            nil,
		"all ten names":   all,
		"literal":         {{"x-request-id", "abc"}},
		"mixed":           {{"host", "a:1"}, {"x-a", "1"}, {"allow", "GET"}, {"x-b", "2"}},
		"empty value":     {{"host", ""}, {"x-empty", ""}},
		"long value":      {{"x-long", strings.Repeat("v", 5000)}},
		"255-byte name":   {{strings.Repeat("n", 255), "v"}},
		"same name twice": {{"x-a", "1"}, {"x-a", "2"}},
	}
	for name, hs := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := DecodeHeaders(EncodeHeaders(hs))
			if err != nil {
				t.Fatal(err)
			}
			if !sameHeaders(got, hs) {
				t.Fatalf("got %v, want %v", got, hs)
			}
		})
	}
}

func TestDecodeHeaders(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []Header
		wantErr error
	}{
		{"empty block", "", nil, nil},
		{"ID 11 is skipped", "0b 00 02 61 62  01 00 01 78", []Header{{"host", "x"}}, nil},
		{"ID 255 is skipped", "ff 00 00  02 00 01 79", []Header{{"user-agent", "y"}}, nil},
		{"only a skipped entry", "0c 00 03 61 62 63", nil, nil},
		{"literal", "00 01 61 00 01 78", []Header{{"a", "x"}}, nil},
		{"ID alone", "01", nil, ErrMalformed},
		{"ID and half a length", "01 00", nil, ErrMalformed},
		{"value shorter than its length", "01 00 05 61", nil, ErrMalformed},
		{"literal, no name length", "00", nil, ErrMalformed},
		{"literal, name truncated", "00 03 61 62", nil, ErrMalformed},
		{"literal, no value length", "00 01 61", nil, ErrMalformed},
		{"literal, value truncated", "00 01 61 00 02 78", nil, ErrMalformed},
		{"literal name length 0", "00 00 00 01 78", nil, ErrMalformed},
		{"skipped entry, truncated", "0b 00 05 61", nil, ErrMalformed},
		{"skipped entry, no length", "0b", nil, ErrMalformed},
		{"good entry then a truncated one", "01 00 01 78  02 00 09 61", nil, ErrMalformed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeHeaders(mustHex(t, tc.in))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !sameHeaders(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// reqPayload hand-builds a REQUEST payload, so the malformed cases do not depend on Encode.
func reqPayload(method byte, path string, block []byte) []byte {
	p := []byte{method, byte(len(path) >> 8), byte(len(path))}
	p = append(p, path...)
	return append(p, block...)
}

func TestDecodeRequestMalformed(t *testing.T) {
	good := mustHex(t, "01 00 01 78") // host: x
	tests := []struct {
		name string
		in   []byte
	}{
		{"empty payload", nil},
		{"method only", []byte{1}},
		{"half a path length", []byte{1, 0}},
		{"path longer than payload", []byte{1, 0, 9, '/', 'a'}},
		{"empty path", reqPayload(1, "", nil)},
		{"path without leading slash", reqPayload(1, "index.html", nil)},
		{"NUL in path", reqPayload(1, "/a\x00b", nil)},
		{"NUL at the end of path", reqPayload(1, "/a\x00", nil)},
		{"dot-dot only", reqPayload(1, "/..", nil)},
		{"dot-dot first", reqPayload(1, "/../x", nil)},
		{"dot-dot last", reqPayload(1, "/a/..", nil)},
		{"dot-dot middle", reqPayload(1, "/a/../b", nil)},
		{"truncated header entry", reqPayload(1, "/", []byte{1, 0, 5, 'a'})},
		{"literal name length 0", reqPayload(1, "/", []byte{0, 0, 0, 0})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeRequest(tc.in); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	// Sanity: the same builder with a good block decodes, so the cases above fail for their own reason.
	if _, err := DecodeRequest(reqPayload(1, "/", good)); err != nil {
		t.Fatalf("control case: %v", err)
	}
}

func TestDecodeRequestValid(t *testing.T) {
	tests := []struct {
		name   string
		method byte
		path   string
	}{
		{"root", 1, "/"},
		{"directory", 1, "/dir/"},
		{"dots inside a name", 1, "/a..b"},
		{"leading dots in a name", 1, "/..a"},
		{"three dots", 1, "/..."},
		{"unknown method 2 is not an error", 2, "/x"},
		{"unknown method 0 is not an error", 0, "/x"},
		{"unknown method 255 is not an error", 255, "/x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := DecodeRequest(reqPayload(tc.method, tc.path, nil))
			if err != nil {
				t.Fatal(err)
			}
			if r.Method != tc.method || r.Path != tc.path || len(r.Headers) != 0 {
				t.Fatalf("got %+v", r)
			}
		})
	}
}

func TestRequestRoundTrip(t *testing.T) {
	in := Request{Method: MethodGet, Path: "/a/b c.txt", Headers: []Header{{"host", "h:1"}, {"x-k", "v"}, {"accept", "*/*"}}}
	out, err := DecodeRequest(in.Encode())
	if err != nil {
		t.Fatal(err)
	}
	if out.Method != in.Method || out.Path != in.Path || !sameHeaders(out.Headers, in.Headers) {
		t.Fatalf("got %+v", out)
	}
}

func TestResponseEncodeBytes(t *testing.T) {
	got := Response{Status: 200}.Encode()
	if !bytes.Equal(got, []byte{0x00, 0xC8}) {
		t.Fatalf("got %x", got)
	}
	got = Response{Status: 405, Headers: []Header{{"allow", "GET"}}}.Encode()
	want := []byte{0x01, 0x95, 10, 0, 3, 'G', 'E', 'T'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	for _, in := range []Response{
		{Status: 200},
		{Status: 404, Headers: []Header{{"content-type", "text/plain"}}},
		{Status: 500, Headers: []Header{{"server", "bserve/1"}, {"x-extra", "1"}}},
	} {
		out, err := DecodeResponse(in.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if out.Status != in.Status || !sameHeaders(out.Headers, in.Headers) {
			t.Fatalf("got %+v, want %+v", out, in)
		}
	}
}

func TestDecodeResponseMalformed(t *testing.T) {
	// ponytail: SPEC.md does not say what a payload under 2 bytes gives. ErrMalformed is the simplest reading.
	for name, in := range map[string][]byte{
		"empty":                 nil,
		"half a status":         {0x00},
		"truncated header":      {0x00, 0xC8, 1, 0, 5, 'a'},
		"literal name length 0": {0x00, 0xC8, 0, 0, 0, 0},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResponse(in); !errors.Is(err, ErrMalformed) {
				t.Fatalf("err = %v, want ErrMalformed", err)
			}
		})
	}
	r, err := DecodeResponse([]byte{0x00, 0xC8, 0xC8, 0x00, 0x01, 'x', 3, 0, 1, 'y'})
	if err != nil || r.Status != 200 || !sameHeaders(r.Headers, []Header{{"accept", "y"}}) {
		t.Fatalf("unknown ID skip: %+v, %v", r, err)
	}
}
