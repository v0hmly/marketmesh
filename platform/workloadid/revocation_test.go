package workloadid

import (
	"math/big"
	"sync"
	"testing"
)

func TestSerialString(t *testing.T) {
	if got := SerialString(nil); got != "" {
		t.Fatalf("SerialString(nil): got %q, want empty", got)
	}
	if got := SerialString(big.NewInt(0xAB)); got != "ab" {
		t.Fatalf("SerialString(0xab): got %q, want %q", got, "ab")
	}
}

func TestInMemoryRevocationList(t *testing.T) {
	list := NewInMemoryRevocationList("ff")
	if !list.Revoked("ff") {
		t.Fatal("preloaded serial is not reported as revoked")
	}
	if list.Revoked("01") {
		t.Fatal("unknown serial reported as revoked")
	}
	list.Revoke("01")
	if !list.Revoked("01") {
		t.Fatal("revoked serial is not reported as revoked")
	}
	list.Revoke("01")
	if !list.Revoked("01") {
		t.Fatal("repeated revoke broke the list")
	}
}

func TestInMemoryRevocationListConcurrent(t *testing.T) {
	list := NewInMemoryRevocationList()
	const workers = 8
	const serialsPerWorker = 64

	var group sync.WaitGroup
	for worker := range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range serialsPerWorker {
				serial := SerialString(big.NewInt(int64(worker*serialsPerWorker + index + 1)))
				list.Revoke(serial)
				if !list.Revoked(serial) {
					t.Errorf("serial %q lost right after revoke", serial)
				}
				list.Revoked("never-revoked")
			}
		}()
	}
	group.Wait()

	for worker := range workers {
		for index := range serialsPerWorker {
			serial := SerialString(big.NewInt(int64(worker*serialsPerWorker + index + 1)))
			if !list.Revoked(serial) {
				t.Errorf("serial %q is not revoked after all workers finished", serial)
			}
		}
	}
	if list.Revoked("never-revoked") {
		t.Error("serial that was never revoked reported as revoked")
	}
}
