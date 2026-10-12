package adapters

// The antivirus adapter against a REAL clamd (not a fake that "inspects bytes"): the EICAR test string is FOUND, ordinary bytes are OK, a file larger than clamd's
// stream limit is an ERROR (never a clean verdict), and a stopped or unreachable clamd is an ERROR (the outbound path then answers 503 and stores nothing).
// Skipped unless OMNIRA_CLAMD_ADDR names a clamd; scripts/test-clamd-real.sh starts a throwaway one (clamav/clamav:1.4) and stops it in the middle on purpose.

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestRealClamdFindsEICARAndFailsClosed(t *testing.T) {
	addr := os.Getenv("OMNIRA_CLAMD_ADDR")
	if addr == "" {
		t.Skip("OMNIRA_CLAMD_ADDR not set (scripts/test-clamd-real.sh starts a throwaway clamd)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	av := NewClamAV(addr)
	eicar := []byte(`X5O!P%@AP[4\PZX54(P^)7CC)7}$EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*`)
	// the script stops the container and asks again: with the real clamd STOPPED a scan is an error (never a clean verdict) and so is a ping
	if os.Getenv("OMNIRA_CLAMD_STOPPED") == "1" {
		if v, err := av.Scan(ctx, eicar); err == nil {
			t.Fatalf("the clamd is stopped and a scan still returned %+v", v)
		}
		if v, err := av.Scan(ctx, []byte("ordinary bytes")); err == nil || v.Infected {
			t.Fatalf("the clamd is stopped and ordinary bytes got %+v, %v (must be an error, never clean)", v, err)
		}
		if err := av.Ping(ctx); err == nil {
			t.Fatal("the clamd is stopped and Ping succeeded")
		}
		return
	}
	if err := av.Ping(ctx); err != nil {
		t.Fatalf("the clamd does not answer: %v", err)
	}

	v, err := av.Scan(ctx, eicar)
	if err != nil || !v.Infected || v.Signature == "" {
		t.Fatalf("EICAR: verdict=%+v err=%v, want infected with a signature name", v, err)
	}
	t.Logf("EICAR is FOUND as %q", v.Signature)

	// (EICAR is detected by the hash of the WHOLE file, so a document that merely contains the string is not 'infected' - measured with this clamd; the test
	// string exists to prove the pipe, not to model real malware.)

	// ordinary bytes of several sizes (below, across and above the adapter's 64 KiB chunk) are OK
	for _, n := range []int{1, 1000, 64 * 1024, 64*1024 + 1, 1 << 20} {
		v, err := av.Scan(ctx, bytes.Repeat([]byte("a"), n))
		if err != nil || v.Infected {
			t.Fatalf("%d clean bytes: verdict=%+v err=%v", n, v, err)
		}
	}

	// a JPEG header followed by zeros is not a threat: the antivirus does not judge file types
	if v, err := av.Scan(ctx, append([]byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), make([]byte, 5000)...)); err != nil || v.Infected {
		t.Fatalf("JPEG-like bytes: verdict=%+v err=%v", v, err)
	}

	// nothing listening: an ERROR, never "clean" (this is what makes the upload answer 503 with the real clamd stopped)
	l, lerr := net.Listen("tcp", "127.0.0.1:0")
	if lerr != nil {
		t.Fatal(lerr)
	}
	dead := l.Addr().String()
	l.Close()
	if v, err := NewClamAV(dead).Scan(ctx, []byte("anything")); err == nil || v.Infected {
		t.Fatalf("unreachable clamd: verdict=%+v err=%v, want an error", v, err)
	}
	if err := NewClamAV(dead).Ping(ctx); err == nil {
		t.Fatal("Ping succeeded against nothing")
	}
}
