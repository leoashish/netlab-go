// Package tcp implements the raw TCP primitives every network server in this course builds on:
// a listening echo server with a disciplined shutdown, a one-shot echo round trip,
// a deadline-aware line reader, and half-close (CloseWrite).
package tcp

import (
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// EchoServer accepts TCP connections on 127.0.0.1:0 and echoes every byte read back
// until the peer closes the connection or Close is called.
//
// The zero value is not usable; always build one with NewEchoServer.
type EchoServer struct {
	ln        net.Listener
	mu        sync.Mutex
	conns     map[net.Conn]struct{} // active, not yet finished
	closing   bool                  // set by Close before the listener goes away
	wg        sync.WaitGroup        // accept loop + one per handler
	closeOnce sync.Once
	closeErr  error // result of the first (and only) Close, returned by every later call
}

// NewEchoServer binds a TCP listener on 127.0.0.1:0 (the kernel assigns a free port) and starts
// accepting connections in a goroutine. It returns an error if the listen fails.
func NewEchoServer() (*EchoServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen on 127.0.0.1:0: %w", err)
	}
	s := &EchoServer{ln: ln, conns: make(map[net.Conn]struct{})}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr returns the listener's address as "127.0.0.1:NNNNN". It is valid before and after Close.
func (s *EchoServer) Addr() string {
	return s.ln.Addr().String()
}

// Close stops accepting new connections, closes the listener AND all active connections
// (blocked Read/Write calls unblock with "use of closed network connection" errors), and waits
// for the handler goroutines to exit. It is idempotent: calling Close more than once is safe
// and returns the same result.
func (s *EchoServer) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		closeErr := s.ln.Close() // pending Accept() calls now return an error
		for c := range s.conns {
			_ = c.Close() // handlers blocked in Read unblock with an error and exit
		}
		s.mu.Unlock()

		s.wg.Wait() // the accept loop and every handler has exited; no goroutine is left behind
		s.closeErr = closeErr
	})
	return s.closeErr
}

func (s *EchoServer) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return // listener closed (or a broken accept loop): stop accepting
		}
		s.mu.Lock()
		if s.closing {
			// Close won the race: the listener is going down and nobody will wait on this conn.
			s.mu.Unlock()
			_ = c.Close()
			return
		}
		s.conns[c] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveEcho(c)
	}
}

func (s *EchoServer) serveEcho(c net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		_ = c.Close()
	}()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := c.Read(buf)
		if n > 0 {
			if _, writeErr := c.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil { // peer FIN (io.EOF) or the conn was closed by Close
			return
		}
	}
}

// Echo is a one-shot echo round trip: dial addr with a 2s timeout, write message,
// read exactly len(message) bytes back, and return them as a string.
// Write and read deadlines are also 2s. A failure at any step returns an error.
func Echo(addr, message string) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return "", fmt.Errorf("dial %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return "", fmt.Errorf("set write deadline: %w", err)
	}
	// net.Conn.Write returns n < len(p) only with a non-nil error, so checking err is enough.
	if _, err := conn.Write([]byte(message)); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}
	buf := make([]byte, len(message))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", fmt.Errorf("read %d bytes: %w", len(message), err)
	}
	return string(buf), nil
}

const maxLineLength = 1 << 16

// ReadLineWithTimeout reads one newline-terminated line from conn and returns it without the
// trailing "\n"; a trailing "\r" (CRLF input) is trimmed too.
//
// It sets a read deadline of d from now. The maximum line length is 1<<16 bytes; a longer line
// returns an error. If the deadline is hit, the returned error matches errors.Is(err, os.ErrDeadlineExceeded).
// If the peer closes the connection before the newline arrives, the error matches errors.Is(err, io.EOF).
//
// Bytes past the newline are never read from conn, so the caller may use conn for the next line.
// It reads one byte at a time on purpose: a big buffered reader would swallow bytes belonging
// to the next line. For a teaching implementation the syscall count is a fair price.
func ReadLineWithTimeout(conn net.Conn, d time.Duration) (string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}
	var line []byte
	buf := make([]byte, 1)
	for {
		n, readErr := conn.Read(buf)
		if n > 0 {
			b := buf[0]
			if b == '\n' {
				return strings.TrimSuffix(string(line), "\r"), nil
			}
			line = append(line, b)
			if len(line) > maxLineLength {
				return "", fmt.Errorf("line exceeds %d bytes without a newline", maxLineLength)
			}
		}
		if readErr != nil {
			// covers the deadline (os.ErrDeadlineExceeded) and io.EOF; %w keeps both matchable
			return "", fmt.Errorf("read line: %w", readErr)
		}
	}
}

// CloseWrite puts the connection into half-close mode: the local side sends FIN (it will not
// write anything more) but still reads. conn must be a *net.TCPConn; anything else returns an error.
func CloseWrite(conn net.Conn) error {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return fmt.Errorf("CloseWrite needs a *net.TCPConn, got %T", conn)
	}
	return tcpConn.CloseWrite()
}
