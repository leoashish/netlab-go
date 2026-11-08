// Package httpmini parses and writes HTTP/1.1 messages — request line, status line, headers,
// bodies and chunked transfer encoding — straight from raw bytes.
//
// It implements the framing rules of RFC 9112 (the current name of RFC 7230): CRLF line endings,
// the empty line (CRLFCRLF) between headers and body, Content-Length / Transfer-Encoding framing,
// and the duplicate-Content-Length hardening that blocks a whole class of request-smuggling attacks.
//
// It deliberately does NOT implement: connection management (keep-alive, pipelining), redirects,
// caching, compression codecs, or HTTP/2. You hand it an io.Reader or io.Writer; it moves one message.
//
// See docs/chapters/ch08-http11-from-scratch.md for the guided lesson.
package httpmini

import (
	"errors"
	"io"

	"netlab/internal/todo"
)

// The sentinel errors every reader/writer returns. They are always wrapped with %w, so
// errors.Is(err, ErrMalformedRequest) keeps working no matter how much context is added.
var (
	// ErrMalformedRequest means the bytes on the wire do not form a valid HTTP request:
	// bad request line, bad header line, conflicting Content-Length, truncated body, ...
	ErrMalformedRequest = errors.New("malformed HTTP request")
	// ErrMalformedResponse means the bytes on the wire do not form a valid HTTP response:
	// bad status line, bad header line, conflicting Content-Length, truncated body, ...
	ErrMalformedResponse = errors.New("malformed HTTP response")
	// ErrHeaderTooLarge means the request/status line plus headers exceeded MaxHeaderBytes
	// before the CRLFCRLF terminator was found. The connection is unusable afterwards.
	ErrHeaderTooLarge = errors.New("headers exceed the 64KB limit")
	// ErrBodyTooLarge means a body declared (or discovered) larger than MaxBody. It is returned
	// BEFORE allocating the body, so a hostile peer cannot OOM you with a single length field.
	ErrBodyTooLarge = errors.New("body exceeds the 4MB limit")
)

// Limits that keep a hostile peer from making you allocate gigabytes. They are fixed by this
// package, not configurable: the tests check the exact boundaries.
const (
	// MaxHeaderBytes caps the request/status line + all header lines + the CRLFCRLF terminator.
	// A header block of exactly MaxHeaderBytes bytes is accepted; one byte more is ErrHeaderTooLarge.
	MaxHeaderBytes = 64 << 10 // 64 KiB

	// MaxBody caps any body: Content-Length, chunked, or read-until-EOF. A body of exactly
	// MaxBody bytes is accepted; one byte more is ErrBodyTooLarge.
	MaxBody = 4 << 20 // 4 MiB
)

// Request is one HTTP request message. The zero value is not a valid message.
type Request struct {
	Method string              // e.g. "GET"; must be a non-empty token (RFC 9110 tchar)
	Path   string              // e.g. "/index.html?x=1"; must start with "/"
	Proto  string              // "HTTP/1.0" or "HTTP/1.1" only
	Header map[string][]string // header names as received; multiple values keep their order
	Body   []byte              // nil when the request has no body
}

// Response is one HTTP response message. The zero value is not a valid message.
type Response struct {
	StatusCode int                 // 100..599
	Reason     string              // reason phrase; empty on WRITE means DefaultReason(StatusCode)
	Proto      string              // "HTTP/1.0" or "HTTP/1.1" only
	Header     map[string][]string // header names as received; multiple values keep their order
	Body       []byte              // nil when the response has no body
}

// DefaultReason returns the standard reason phrase for a status code, e.g. 200 -> "OK",
// 404 -> "Not Found". Unknown codes (including 1xx) return "Unknown Status".
//
// (A structure like this is what net/http's StatusText does with a bigger table.)
func DefaultReason(code int) string {
	// TODO Ch08: a small map of the common codes; anything else is "Unknown Status".
	return ""
}

// ReadRequest reads exactly one HTTP request message from r and returns it.
//
// Framing, in order:
//
//  1. Read byte by byte until the four bytes CRLFCRLF ("\r\n\r\n") arrive: that is the header
//     block. If it exceeds MaxHeaderBytes before the terminator, return ErrHeaderTooLarge.
//     If the stream ends before any byte at all, return io.EOF unchanged; if it ends mid-block,
//     return an error wrapping ErrMalformedRequest.
//  2. Request line: "METHOD SP PATH SP PROTO", with exactly two spaces and no leading/trailing
//     space. METHOD must be a non-empty token; PATH must start with "/" (absolute-form such as
//     "http://host/" is rejected — see the lesson); PROTO must be exactly "HTTP/1.0" or "HTTP/1.1".
//  3. Header lines: "Name: value". Trim spaces/tabs around name and value; the name must be a
//     non-empty token. A line without a colon, or with an invalid name, is malformed.
//     Duplicate names append to the same slice, in arrival order.
//  4. Body framing (RFC 9112 §6.3), in this priority order:
//     a. Transfer-Encoding, if any value contains the token "chunked" (case-insensitive,
//     comma-separated list allowed), means the body is chunked: parse with ParseChunked.
//     b. Otherwise, Content-Length, if present, must be a non-negative decimal integer and is
//     the exact body size. More than one Content-Length is fine only if every value is
//     identical (the smuggling guard); conflicting values are malformed. A length greater
//     than MaxBody is ErrBodyTooLarge — checked BEFORE reading, never allocate on a promise.
//     c. Neither header: the request has no body (nil).
//
// A body shorter than Content-Length promised, or a malformed chunked body, wraps
// ErrMalformedRequest. ErrBodyTooLarge from the chunked parser is preserved.
//
// Clean boundary note: r is wrapped in a bufio.Reader (reused if r already is one), so bytes
// read past this message stay buffered inside the package unless you pass your own *bufio.Reader.
func ReadRequest(r io.Reader) (*Request, error) {
	// TODO Ch08: header block → request line → headers → body framing (chunked / Content-Length /
	// empty). Honour MaxHeaderBytes and MaxBody before allocating.
	return nil, todo.NotImplemented("Ch08: ReadRequest")
}

// ReadResponse reads exactly one HTTP response message from r and returns it.
//
// Same header reading as ReadRequest (CRLFCRLF terminator, MaxHeaderBytes cap, io.EOF only when
// zero bytes arrived). Status line: "PROTO SP CODE SP REASON":
//
//   - PROTO must be exactly "HTTP/1.0" or "HTTP/1.1";
//   - CODE is three digits in 100..599;
//   - REASON may be empty (the rest of the line, spaces allowed). It is stored as received;
//     the "fill in the default" behaviour belongs to WriteResponse.
//
// Interim 1xx responses are NOT supported: any code below 200 returns ErrMalformedResponse.
// This package models one final response; handling 100-continue is a stretch challenge.
//
// Body framing:
//
//   - Transfer-Encoding containing "chunked" → ParseChunked;
//   - else Content-Length (same rules and smuggling guard as ReadRequest);
//   - else the body runs until EOF: this is the legal HTTP/1.0 style (close-delimited), and
//     what a response with neither header means in this package. The EOF read is capped at
//     MaxBody too: more bytes than that is ErrBodyTooLarge.
func ReadResponse(r io.Reader) (*Response, error) {
	// TODO Ch08: header block → status line (reject 1xx) → headers → body framing, including
	// the read-until-EOF case.
	return nil, todo.NotImplemented("Ch08: ReadResponse")
}

// WriteRequest writes req to w in wire format:
//
//	METHOD SP PATH SP PROTO CRLF
//	Name: value CRLF            (one line PER VALUE, never comma-joined)
//	...
//	CRLF
//	body bytes
//
// Validation (returns an error wrapping ErrMalformedRequest): Method must be a non-empty token,
// Path must start with "/", Proto must be "HTTP/1.0" or "HTTP/1.1".
//
// Header lines are emitted in ALPHABETICAL order of the header NAMES (byte-wise, case-sensitive)
// so the output is deterministic; values of the same name keep their slice order. Presence checks
// for Content-Length / Transfer-Encoding are case-insensitive.
//
// If neither a Content-Length nor a Transfer-Encoding header is present and len(Body) > 0, a
// "Content-Length: <len>" header is added automatically. An empty body adds nothing (a request
// with no body needs no length). A Content-Length you set yourself is written as-is — the writer
// trusts you; it never mutates req.
func WriteRequest(w io.Writer, req *Request) error {
	// TODO Ch08: validate; copy the header map (do not mutate req); add Content-Length when
	// needed; sort names; write lines + blank line + body.
	return todo.NotImplemented("Ch08: WriteRequest")
}

// WriteResponse writes resp to w in wire format:
//
//	PROTO SP CODE SP REASON CRLF
//	Name: value CRLF            (one line PER VALUE)
//	...
//	CRLF
//	body bytes
//
// If Reason is empty, DefaultReason(StatusCode) is used. Validation (error wrapping
// ErrMalformedResponse): StatusCode must be in 100..599, Proto must be "HTTP/1.0" or "HTTP/1.1".
//
// Everything else matches WriteRequest: alphabetical header names, one line per value, automatic
// Content-Length when absent and the body is non-empty, the caller's map is never mutated.
func WriteResponse(w io.Writer, resp *Response) error {
	// TODO Ch08: validate; default the reason; copy + auto-length; write.
	return todo.NotImplemented("Ch08: WriteResponse")
}

// AppendChunk appends one chunked-transfer-encoding chunk to dst and returns the grown slice:
//
//	<lowercase-hex length> CRLF <chunk bytes> CRLF
//
// An empty chunk writes exactly "0\r\n\r\n" — the terminating chunk, which in real chunked
// encoding is followed by the (possibly empty) trailer section. Build a complete body with:
//
//	body := AppendChunk(AppendChunk(AppendChunk(nil, part1), part2), nil)
func AppendChunk(dst []byte, chunk []byte) []byte {
	// TODO Ch08: hex length + CRLF + chunk + CRLF (and "0\r\n\r\n" for an empty chunk).
	return dst
}

// ParseChunked reads a chunked-transfer-encoding body from r until the terminating zero-size
// chunk, then reads and DISCARDS trailer lines up to the blank line that ends them. It returns
// the concatenated chunk data.
//
//	3\r\nabc\r\n3;ext=ignored\r\ndef\r\n0\r\nTrailer: x\r\n\r\n   →   "abcdef"
//
// Rules:
//
//   - a chunk-size line is hex (either case), optionally followed by ";" extensions, which are
//     ignored. A size line that is not valid hex is an error, and every line — size, data
//     terminator or trailer — MUST end with CRLF;
//   - read exactly <size> bytes plus the CRLF that follows them: a short chunk is an error
//     (io.ErrUnexpectedEOF-style), not silently accepted;
//   - when the running total would exceed MaxBody, return an error wrapping ErrBodyTooLarge
//     BEFORE reading the offending chunk (again: never allocate on a promise);
//   - after the "0" chunk, discard lines until an empty line. A missing final blank line is an error.
//
// The returned slice is non-nil even for an empty body ("0\r\n\r\n" → []byte{}).
func ParseChunked(r io.Reader) ([]byte, error) {
	// TODO Ch08: loop over size lines → check the cap → read exactly size bytes + CRLF →
	// until size 0 → discard trailers to the blank line.
	return nil, todo.NotImplemented("Ch08: ParseChunked")
}
