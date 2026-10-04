package adapters

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/omnira/omnira/internal/media/ports"
)

// ClamAV talks to clamd over TCP with the INSTREAM command. The bytes are streamed, never written to a path
// clamd could be tricked into reading. Anything other than an explicit "OK" or "FOUND" is an error, and the
// caller treats an error as "not scanned" (fail closed).
type ClamAV struct {
	Addr    string
	Timeout time.Duration
	dial    func(ctx context.Context, network, addr string) (net.Conn, error)
}

var _ ports.Scanner = (*ClamAV)(nil)

func NewClamAV(addr string) *ClamAV {
	d := &net.Dialer{Timeout: 5 * time.Second}
	return &ClamAV{Addr: addr, Timeout: 60 * time.Second, dial: d.DialContext}
}

const clamChunk = 64 * 1024

func (c *ClamAV) Scan(ctx context.Context, data []byte) (ports.Verdict, error) {
	if c.Addr == "" {
		return ports.Verdict{}, errors.New("clamav: address not configured")
	}
	conn, err := c.dial(ctx, "tcp", c.Addr)
	if err != nil {
		return ports.Verdict{}, fmt.Errorf("clamav: connect: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.Timeout))
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return ports.Verdict{}, fmt.Errorf("clamav: write command: %w", err)
	}
	var size [4]byte
	for off := 0; off < len(data); off += clamChunk {
		end := off + clamChunk
		if end > len(data) {
			end = len(data)
		}
		binary.BigEndian.PutUint32(size[:], uint32(end-off))
		if _, err := conn.Write(size[:]); err != nil {
			return ports.Verdict{}, fmt.Errorf("clamav: write chunk size: %w", err)
		}
		if _, err := conn.Write(data[off:end]); err != nil {
			return ports.Verdict{}, fmt.Errorf("clamav: write chunk: %w", err)
		}
	}
	if _, err := conn.Write([]byte{0, 0, 0, 0}); err != nil {
		return ports.Verdict{}, fmt.Errorf("clamav: write terminator: %w", err)
	}

	reply := make([]byte, 0, 256)
	buf := make([]byte, 256)
	for {
		n, err := conn.Read(buf)
		reply = append(reply, buf[:n]...)
		if i := bytes.IndexByte(reply, 0); i >= 0 {
			reply = reply[:i]
			break
		}
		if err != nil {
			break
		}
		if len(reply) > 4096 {
			return ports.Verdict{}, errors.New("clamav: oversized reply")
		}
	}
	return parseClamReply(string(reply))
}

// parseClamReply accepts only the two definitive answers. "ERROR", an empty reply, a size-limit message or
// anything unexpected is an error, never a clean verdict.
func parseClamReply(reply string) (ports.Verdict, error) {
	reply = strings.TrimSpace(reply)
	switch {
	case reply == "stream: OK":
		return ports.Verdict{}, nil
	case strings.HasPrefix(reply, "stream: ") && strings.HasSuffix(reply, " FOUND"):
		sig := strings.TrimSuffix(strings.TrimPrefix(reply, "stream: "), " FOUND")
		if sig == "" {
			sig = "unknown"
		}
		return ports.Verdict{Infected: true, Signature: sig}, nil
	}
	return ports.Verdict{}, fmt.Errorf("clamav: unexpected reply %q", truncate(reply, 120))
}

// Ping reports whether clamd answers; used for the worker's startup log and health.
func (c *ClamAV) Ping(ctx context.Context) error {
	conn, err := c.dial(ctx, "tcp", c.Addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("zPING\x00")); err != nil {
		return err
	}
	b := make([]byte, 16)
	n, _ := conn.Read(b)
	if !strings.HasPrefix(string(b[:n]), "PONG") {
		return fmt.Errorf("clamav: unexpected ping reply %q", string(b[:n]))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
