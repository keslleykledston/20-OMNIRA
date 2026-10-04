package adapters

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnira/omnira/internal/media/ports"
)

// fakeClamd speaks just enough of the INSTREAM protocol to verify what the client sends and to inject replies.
func fakeClamd(t *testing.T, reply string, onData func([]byte)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				cmd := make([]byte, len("zINSTREAM\x00"))
				if _, err := io.ReadFull(c, cmd); err != nil || string(cmd) != "zINSTREAM\x00" {
					return
				}
				var got []byte
				for {
					var n [4]byte
					if _, err := io.ReadFull(c, n[:]); err != nil {
						return
					}
					size := binary.BigEndian.Uint32(n[:])
					if size == 0 {
						break
					}
					chunk := make([]byte, size)
					if _, err := io.ReadFull(c, chunk); err != nil {
						return
					}
					got = append(got, chunk...)
				}
				if onData != nil {
					onData(got)
				}
				_, _ = c.Write([]byte(reply + "\x00"))
			}()
		}
	}()
	return ln.Addr().String()
}

func TestClamAVStreamsTheExactBytesAndParsesVerdicts(t *testing.T) {
	payload := make([]byte, 200_000) // spans several 64 KiB chunks
	for i := range payload {
		payload[i] = byte(i)
	}
	var received []byte
	addr := fakeClamd(t, "stream: OK", func(b []byte) { received = b })
	v, err := NewClamAV(addr).Scan(context.Background(), payload)
	if err != nil || v.Infected {
		t.Fatalf("verdict %+v err %v", v, err)
	}
	if string(received) != string(payload) {
		t.Fatalf("clamd received %d bytes that differ from the %d sent", len(received), len(payload))
	}

	addr = fakeClamd(t, "stream: Win.Test.EICAR_HDB-1 FOUND", nil)
	v, err = NewClamAV(addr).Scan(context.Background(), []byte("x"))
	if err != nil || !v.Infected || v.Signature != "Win.Test.EICAR_HDB-1" {
		t.Fatalf("verdict %+v err %v", v, err)
	}
}

func TestClamAVNeverReportsCleanOnAnythingButAnExplicitOK(t *testing.T) {
	for _, reply := range []string{"", "stream: ERROR", "INSTREAM size limit exceeded. ERROR", "stream: OK but also junk", "garbage", "stream:  FOUND ok"} {
		addr := fakeClamd(t, reply, nil)
		v, err := NewClamAV(addr).Scan(context.Background(), []byte("hello"))
		if err == nil && !v.Infected && reply != "stream: OK" {
			t.Fatalf("reply %q was treated as clean", reply)
		}
	}
}

func TestClamAVUnreachableOrSilentIsAnError(t *testing.T) {
	if _, err := NewClamAV("127.0.0.1:1").Scan(context.Background(), []byte("x")); err == nil {
		t.Fatal("an unreachable antivirus must be an error, not a clean verdict")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			time.Sleep(2 * time.Second)
			c.Close()
		}
	}()
	c := NewClamAV(ln.Addr().String())
	c.Timeout = 300 * time.Millisecond
	if _, err := c.Scan(context.Background(), []byte("x")); err == nil {
		t.Fatal("a silent antivirus must time out as an error")
	}
	if _, err := NewClamAV("").Scan(context.Background(), []byte("x")); err == nil {
		t.Fatal("a missing address must be an error")
	}
}

func TestFileStoreKeepsQuarantineOutOfTheServedArea(t *testing.T) {
	root := t.TempDir()
	s, err := NewFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	w := ports.Work{ID: uuid.New(), TenantID: uuid.New()}
	if err := s.PutQuarantine(w, []byte("data")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenClean(w.TenantID.String(), w.ID.String()); err == nil {
		t.Fatal("a quarantined file must not be openable as clean")
	}
	if err := s.Promote(w); err != nil {
		t.Fatal(err)
	}
	f, err := s.OpenClean(w.TenantID.String(), w.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := s.GetQuarantine(w); err == nil {
		t.Fatal("promotion must move the file out of quarantine")
	}
	if err := s.Remove(w); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(w); err != nil {
		t.Fatalf("removing twice must not fail: %v", err)
	}
	info, _ := os.Stat(filepath.Join(root, "quarantine"))
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("quarantine mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestFileStoreRequiresAnAbsoluteRoot(t *testing.T) {
	if _, err := NewFileStore("relative/dir"); err == nil {
		t.Fatal("relative root must be refused")
	}
}

func TestRebaseKeepsOnlyThePathUnderApiFiles(t *testing.T) {
	base := "http://waha:3000"
	ok, err := rebase(base, "http://localhost:3000/api/files/omnira_x/ABC.jpeg")
	if err != nil || ok != "http://waha:3000/api/files/omnira_x/ABC.jpeg" {
		t.Fatalf("got %q %v", ok, err)
	}
	for _, bad := range []string{
		"http://localhost:3000/api/sessions",
		"http://evil.example/other/path",
		"http://localhost:3000/api/files/../sessions",
		"http://localhost:3000//api/files/x",
		"http://user:pw@localhost:3000/api/files/x",
		"://broken",
		"",
	} {
		if got, err := rebase(base, bad); err == nil {
			t.Fatalf("%q must be refused, got %q", bad, got)
		}
	}
	// Even a hostile host in the reference cannot redirect the call: only the path survives.
	got, _ := rebase(base, "http://evil.example/api/files/x")
	if !strings.HasPrefix(got, base+"/") {
		t.Fatalf("host must be replaced, got %q", got)
	}
}

func TestOpenFileStoreNeverCreatesAnythingAndNeedsAnExistingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := OpenFileStore(missing); err == nil {
		t.Fatal("a missing directory must be an error")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("OpenFileStore must not create the directory")
	}
	root := t.TempDir()
	if _, err := OpenFileStore(root); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("OpenFileStore must not create quarantine/clean folders")
	}
	if _, err := OpenFileStore("relative"); err == nil {
		t.Fatal("relative path must be refused")
	}
}
