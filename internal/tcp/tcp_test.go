package tcp_test

// Chapter 04 -- TCP sockets with Go.   Run:  go test ./internal/tcp -run Ch04 -race -v
//
// All tests use loopback TCP only (127.0.0.1, kernel-assigned ports via 127.0.0.1:0).
// No fixed ports, no internet, no sleeps: deadline-based tests wait on the deadline itself.

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"netlab/internal/tcp"
	"netlab/internal/testutil"
)

// newEchoServer builds an EchoServer, failing the test if it cannot start, and registers a
// Close backstop so a failing test cannot leak the accept loop goroutine.
func newEchoServer(t *testing.T) *tcp.EchoServer {
	t.Helper()
	srv, err := tcp.NewEchoServer()
	if err != nil {
		t.Fatalf("NewEchoServer: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// rawListener is a scriptable "peer" for tests that need a raw TCP endpoint
// (the silent server, the \r\n writer, the mid-line closer). It accepts every connection,
// runs handle in a goroutine, and Close cleans up the listener and every accepted conn.
type rawListener struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newRawListener(t *testing.T, handle func(net.Conn)) *rawListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("raw listener: %v", err)
	}
	r := &rawListener{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed by the cleanup
			}
			r.mu.Lock()
			r.conns = append(r.conns, c)
			r.mu.Unlock()
			if handle != nil {
				go handle(c)
			}
		}
	}()
	t.Cleanup(func() {
		_ = r.ln.Close()
		r.mu.Lock()
		defer r.mu.Unlock()
		for _, c := range r.conns {
			_ = c.Close()
		}
	})
	return r
}

func (r *rawListener) addr() string { return r.ln.Addr().String() }

func (r *rawListener) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", r.addr(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial %s: %v", r.addr(), err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestCh04_Echo_RoundTripsASCII(t *testing.T) {
	testutil.NoLeaks(t)
	srv := newEchoServer(t)
	for _, msg := range []string{"hello, netlab", "", "multi\nline\nmessage", "héllo, 世界 🌍"} {
		got, err := tcp.Echo(srv.Addr(), msg)
		if err != nil {
			t.Fatalf("Echo(%q): %v", msg, err)
		}
		if got != msg {
			t.Fatalf("Echo(%q) = %q, want %q", msg, got, msg)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCh04_Echo_RoundTripsBinary(t *testing.T) {
	testutil.NoLeaks(t)
	srv := newEchoServer(t)
	payload := make([]byte, 256)
	for i := range payload {
		payload[i] = byte(i) // every byte 0x00..0xff, NULs and newlines included
	}
	got, err := tcp.Echo(srv.Addr(), string(payload))
	if err != nil {
		t.Fatalf("Echo(binary): %v", err)
	}
	if !bytes.Equal([]byte(got), payload) {
		t.Fatalf("binary round trip mismatch: got %d bytes, want the original 256", len(got))
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCh04_Echo_ToClosedServer_ReturnsError(t *testing.T) {
	srv := newEchoServer(t)
	addr := srv.Addr()
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// the port is closed now: the kernel answers RST and dial fails fast (connection refused)
	_, err := tcp.Echo(addr, "ping")
	testutil.ExpectError(t, err)
}

func TestCh04_Echo_Concurrent_8Goroutines(t *testing.T) {
	testutil.NoLeaks(t)
	srv := newEchoServer(t)
	msgs := []string{
		"alpha", "bravo", "charlie", "delta",
		"echo-echo", "foxtrot\nwith\nnewlines", "héllo 🌍", strings.Repeat("x", 4096),
	}
	var wg sync.WaitGroup
	results := make([]string, len(msgs))
	errs := make([]error, len(msgs))
	for i, m := range msgs {
		wg.Add(1)
		go func(i int, m string) {
			defer wg.Done()
			got, err := tcp.Echo(srv.Addr(), m)
			results[i], errs[i] = got, err
		}(i, m)
	}
	wg.Wait()
	for i, m := range msgs {
		if errs[i] != nil {
			t.Fatalf("Echo #%d (%q): %v", i, m, errs[i])
		}
		if results[i] != m {
			t.Fatalf("Echo #%d = %q, want %q", i, results[i], m)
		}
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestCh04_EchoServer_Close_IsIdempotent(t *testing.T) {
	srv := newEchoServer(t)
	if err := srv.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	// two goroutines closing at the same moment: exactly one does the work, both see the same result
	srv2 := newEchoServer(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- srv2.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
}

func TestCh04_EchoServer_Addr_IsLoopbackWithKernelAssignedPort(t *testing.T) {
	srv := newEchoServer(t)
	defer func() { _ = srv.Close() }()
	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("Addr %q is not host:port: %v", srv.Addr(), err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("Addr host = %q, want 127.0.0.1 (loopback only)", host)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		t.Fatalf("Addr port = %q, want a kernel-assigned port in 1..65535", portStr)
	}
}

func TestCh04_ReadLineWithTimeout_ReadsLine(t *testing.T) {
	raw := newRawListener(t, func(c net.Conn) {
		_, _ = c.Write([]byte("hello\n"))
	})
	conn := raw.dial(t)
	line, err := tcp.ReadLineWithTimeout(conn, 2*time.Second)
	if err != nil {
		t.Fatalf("ReadLineWithTimeout: %v", err)
	}
	if line != "hello" {
		t.Fatalf("line = %q, want %q", line, "hello")
	}
}

func TestCh04_ReadLineWithTimeout_StripsCRLF(t *testing.T) {
	raw := newRawListener(t, func(c net.Conn) {
		_, _ = c.Write([]byte("hello\r\n"))
	})
	conn := raw.dial(t)
	line, err := tcp.ReadLineWithTimeout(conn, 2*time.Second)
	if err != nil {
		t.Fatalf("ReadLineWithTimeout: %v", err)
	}
	if line != "hello" {
		t.Fatalf("line = %q, want %q (CRLF should be tolerated)", line, "hello")
	}
}

func TestCh04_ReadLineWithTimeout_SilentServer_DeadlineExceeded(t *testing.T) {
	// a peer that accepts but never writes: indistinguishable from a slow one until the deadline
	raw := newRawListener(t, nil)
	conn := raw.dial(t)
	line, err := tcp.ReadLineWithTimeout(conn, 150*time.Millisecond)
	if line != "" {
		t.Fatalf("line = %q, want empty from a silent server", line)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("err = %v, want errors.Is(err, os.ErrDeadlineExceeded)", err)
	}
}

func TestCh04_ReadLineWithTimeout_LineTooLong_Error(t *testing.T) {
	// 1<<16+1 bytes without a newline: one byte over the cap
	raw := newRawListener(t, func(c net.Conn) {
		_, _ = c.Write(bytes.Repeat([]byte("a"), 1<<16+1))
	})
	conn := raw.dial(t)
	line, err := tcp.ReadLineWithTimeout(conn, 2*time.Second)
	if line != "" {
		t.Fatalf("line = %q, want empty on error", line)
	}
	testutil.ExpectError(t, err)
}

func TestCh04_ReadLineWithTimeout_PeerClosedMidLine_ReturnsEOF(t *testing.T) {
	raw := newRawListener(t, func(c net.Conn) {
		_, _ = c.Write([]byte("partial"))
		_ = c.Close() // FIN without a newline
	})
	conn := raw.dial(t)
	_, err := tcp.ReadLineWithTimeout(conn, 2*time.Second)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("err = %v, want errors.Is(err, io.EOF)", err)
	}
}

func TestCh04_CloseWrite_SendsFIN_PeerSeesEOF(t *testing.T) {
	type readResult struct {
		n   int
		err error
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	peerRead := make(chan readResult, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			peerRead <- readResult{err: err}
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 16)
		n, rerr := c.Read(buf)
		peerRead <- readResult{n: n, err: rerr}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := tcp.CloseWrite(conn); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	select {
	case res := <-peerRead:
		if res.n != 0 {
			t.Fatalf("peer read n = %d, want 0 (FIN carries no bytes)", res.n)
		}
		if !errors.Is(res.err, io.EOF) {
			t.Fatalf("peer read err = %v, want io.EOF", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not see the FIN within 2s of CloseWrite")
	}
}

func TestCh04_CloseWrite_HalfClose_StillReadsPeerData(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			accepted <- c // hand the open conn to the test; it is closed in the cleanup below
		}
	}()

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := tcp.CloseWrite(conn); err != nil { // FIN sent, but the read side stays open
		t.Fatalf("CloseWrite: %v", err)
	}

	var peer net.Conn
	select {
	case peer = <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("peer never accepted the connection")
	}
	t.Cleanup(func() { _ = peer.Close() })
	if _, err := peer.Write([]byte("after-fin")); err != nil {
		t.Fatalf("peer write after FIN: %v", err)
	}
	buf := make([]byte, len("after-fin"))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read after CloseWrite: %v", err)
	}
	if string(buf) != "after-fin" {
		t.Fatalf("read %q, want %q (half-closed conn must still read)", buf, "after-fin")
	}
}

func TestCh04_CloseWrite_RejectsNonTCPConn(t *testing.T) {
	a, b := net.Pipe() // an in-memory pipe: NOT a *net.TCPConn, so CloseWrite must refuse
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	testutil.ExpectError(t, tcp.CloseWrite(a))
}
