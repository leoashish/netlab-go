// Package tcp implements the raw TCP primitives every network server in this course builds on:
// a listening echo server with a disciplined shutdown, a one-shot echo round trip,
// a deadline-aware line reader, and half-close (CloseWrite).
package tcp

import (
	"net"
	"time"

	"netlab/internal/todo"
)

// EchoServer accepts TCP connections on 127.0.0.1:0 and echoes every byte read back
// until the peer closes the connection or Close is called.
//
// The zero value is not usable; always build one with NewEchoServer.
type EchoServer struct {
	// unexported fields are an implementation detail — see the reference solution
}

// NewEchoServer binds a TCP listener on 127.0.0.1:0 (the kernel assigns a free port) and starts
// accepting connections in a goroutine. It returns an error if the listen fails.
func NewEchoServer() (*EchoServer, error) {
	return nil, todo.NotImplemented("Ch04: NewEchoServer")
}

// Addr returns the listener's address as "127.0.0.1:NNNNN". It is valid before and after Close.
func (s *EchoServer) Addr() string {
	return "" // zero value on the unfinished stub
}

// Close stops accepting new connections, closes the listener AND all active connections
// (blocked Read/Write calls unblock with "use of closed network connection" errors), and waits
// for the handler goroutines to exit. It is idempotent: calling Close more than once is safe
// and returns the same result.
func (s *EchoServer) Close() error {
	return todo.NotImplemented("Ch04: Close")
}

// Echo is a one-shot echo round trip: dial addr with a 2s timeout, write message,
// read exactly len(message) bytes back, and return them as a string.
// Write and read deadlines are also 2s. A failure at any step returns an error.
func Echo(addr, message string) (string, error) {
	return "", todo.NotImplemented("Ch04: Echo")
}

// ReadLineWithTimeout reads one newline-terminated line from conn and returns it without the
// trailing "\n"; a trailing "\r" (CRLF input) is trimmed too.
//
// It sets a read deadline of d from now. The maximum line length is 1<<16 bytes; a longer line
// returns an error. If the deadline is hit, the returned error matches errors.Is(err, os.ErrDeadlineExceeded).
// If the peer closes the connection before the newline arrives, the error matches errors.Is(err, io.EOF).
//
// Bytes past the newline are never read from conn, so the caller may use conn for the next line.
func ReadLineWithTimeout(conn net.Conn, d time.Duration) (string, error) {
	return "", todo.NotImplemented("Ch04: ReadLineWithTimeout")
}

// CloseWrite puts the connection into half-close mode: the local side sends FIN (it will not
// write anything more) but still reads. conn must be a *net.TCPConn; anything else returns an error.
func CloseWrite(conn net.Conn) error {
	return todo.NotImplemented("Ch04: CloseWrite")
}
