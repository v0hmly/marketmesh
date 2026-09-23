package clamav

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

func TestScannerFailClosed(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, result string
		age          time.Duration
		want         error
	}{
		{"clean", "stream: OK\x00", time.Hour, nil},
		{"infected", "stream: Eicar-Test-Signature FOUND\x00", time.Hour, file.ErrRejected},
		{"limit", "stream: Heuristics.Limits.Exceeded FOUND\x00", time.Hour, file.ErrRejected},
		{"error", "INSTREAM size limit exceeded. ERROR\x00", time.Hour, file.ErrUnavailable},
		{"ambiguous", "stream: OK extra\x00", time.Hour, file.ErrUnavailable},
		{"stale", "stream: OK\x00", 49 * time.Hour, file.ErrUnavailable},
		{"future", "stream: OK\x00", -time.Hour, file.ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Keep Unix paths below Darwin's 104-byte bound.
			dir, err := newSocketDir(t)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "clam.sock")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			var streamed atomic.Int64
			done := make(chan struct{})
			defer func() { listener.Close(); <-done }()
			go func() {
				defer close(done)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					func() {
						defer conn.Close()
						conn.SetDeadline(time.Now().Add(time.Second))
						r := bufio.NewReader(conn)
						command, _ := r.ReadString(0)
						switch command {
						case "zVERSION\x00":
							io.WriteString(conn, "ClamAV 1.5.0/28000/"+now.Add(-tc.age).Format("Mon Jan _2 15:04:05 2006")+"\x00")
						case "zINSTREAM\x00":
							for {
								var n uint32
								if binary.Read(r, binary.BigEndian, &n) != nil {
									return
								}
								if n == 0 {
									break
								}
								copied, err := io.CopyN(io.Discard, r, int64(n))
								streamed.Add(copied)
								if err != nil {
									return
								}
							}
							io.WriteString(conn, tc.result)
						}
					}()
				}
			}()
			client, _ := New(path)
			client.now = func() time.Time { return now }
			err = client.Scan(context.Background(), bytes.NewReader([]byte("data")), 4)
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
			if tc.name == "stale" || tc.name == "future" {
				if streamed.Load() != 0 {
					t.Fatal("scanned with unusable signatures")
				}
			} else if streamed.Load() != 4 {
				t.Fatal("wrong stream length")
			}
		})
	}
}

func newSocketDir(t *testing.T) (string, error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mm43-clam-")
	if err == nil {
		t.Cleanup(func() { os.RemoveAll(dir) })
	}
	return dir, err
}
