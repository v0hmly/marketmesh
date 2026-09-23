//go:build integration

package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/clamav"
	filespostgres "github.com/v0hmly/marketmesh/services/files/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/raster"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/s3store"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/sandbox"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/application/processing"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type liveFixture struct {
	ctx         context.Context
	control     *files.Service
	repo        *filespostgres.Repository
	worker      *processing.Worker
	workerStore *s3store.Store
	db          *platformpostgres.Database
	http        *http.Client
	av          *clamav.Client
}

func live(t *testing.T) *liveFixture {
	t.Helper()
	dir := os.Getenv("FILES_FIXTURE")
	if dir == "" {
		t.Skip("FILES_FIXTURE not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	t.Cleanup(cancel)
	controlConfig, err := readFilesConfig(filepath.Join(dir, "control.json"))
	if err != nil {
		t.Fatal("control fixture unavailable")
	}
	workerConfig, err := readFilesConfig(filepath.Join(dir, "worker.json"))
	if err != nil {
		t.Fatal("worker fixture unavailable")
	}
	db, err := database(ctx, controlConfig)
	if err != nil {
		t.Fatal("control database unavailable", err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	wdb, err := database(ctx, workerConfig)
	if err != nil {
		t.Fatal("worker database unavailable", err)
	}
	t.Cleanup(func() { wdb.Close(context.Background()) })
	repo, err := filespostgres.New(db.RW())
	if err != nil {
		t.Fatal(err)
	}
	wr, err := filespostgres.New(wdb.RW())
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage(controlConfig)
	if err != nil {
		t.Fatal("control storage configuration")
	}
	workerStore, err := storage(workerConfig)
	if err != nil {
		t.Fatal("worker storage configuration")
	}
	service, err := files.New(repo, store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	av, err := clamav.New(workerConfig.ClamSocket)
	if err != nil {
		t.Fatal(err)
	}
	readyCtx, stopReady := context.WithTimeout(ctx, time.Minute)
	defer stopReady()
	for av.Ready(readyCtx) != nil {
		if readyCtx.Err() != nil {
			t.Fatal("AV did not become ready")
		}
		time.Sleep(250 * time.Millisecond)
	}
	cdr, err := sandbox.New(workerConfig.SandboxSocket)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := processing.New(wr, workerStore, av, cdr, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ca, err := roots(controlConfig.StorageCA)
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: ca}}, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &liveFixture{ctx: ctx, control: service, repo: repo, worker: worker, workerStore: workerStore, db: db, http: httpClient, av: av}
}
func opaque(t *testing.T) file.ID {
	t.Helper()
	var id file.ID
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatal(err)
	}
	return id
}
func owner(t *testing.T) file.Owner { return file.Owner{Tenant: opaque(t), Subject: opaque(t)} }
func manifest(data []byte, format file.Format) file.Manifest {
	m := file.Manifest{Format: format, Size: int64(len(data)), SHA256: file.Digest(sha256.Sum256(data))}
	for offset := 0; offset < len(data); offset += file.PartSize {
		p := data[offset:min(offset+file.PartSize, len(data))]
		m.Parts = append(m.Parts, file.Part{Size: int64(len(p)), SHA256: file.Digest(sha256.Sum256(p))})
	}
	return m
}
func (f *liveFixture) upload(t *testing.T, data []byte, format file.Format) (file.Owner, file.Record) {
	t.Helper()
	who := owner(t)
	key := opaque(t)
	m := manifest(data, format)
	u, err := f.control.Create(f.ctx, who, key, m)
	if err != nil {
		t.Fatal("create", err)
	}
	again, err := f.control.Create(f.ctx, who, key, m)
	if err != nil || again.File.ID != u.File.ID {
		t.Fatal("non-idempotent create", err)
	}
	for _, cap := range u.Parts {
		part := data[int(cap.PartNumber-1)*file.PartSize : min(int(cap.PartNumber)*file.PartSize, len(data))]
		req, err := http.NewRequestWithContext(f.ctx, cap.Method, cap.URL, bytes.NewReader(part))
		if err != nil {
			t.Fatal("request construction")
		}
		for name, value := range cap.Headers {
			req.Header.Set(name, value)
		}
		response, err := f.http.Do(req)
		if err != nil {
			t.Fatal("direct upload transport")
		}
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatal("direct upload rejected", response.StatusCode)
		}
	}
	resumed, err := f.control.Create(f.ctx, who, key, m)
	if err != nil || len(resumed.Parts) != 0 || len(resumed.Received) != len(m.Parts) {
		t.Fatal("resume lost acknowledged parts", err)
	}
	r, err := f.control.Complete(f.ctx, who, u.File.ID)
	if err != nil || r.State != file.Scanning {
		t.Fatal("complete", err)
	}
	if _, err = f.control.Download(f.ctx, who, r.ID); !errors.Is(err, file.ErrNotReady) {
		t.Fatal("quarantine download allowed")
	}
	if _, err = f.control.Status(f.ctx, owner(t), r.ID); !errors.Is(err, file.ErrNotFound) {
		t.Fatal("foreign owner access")
	}
	return who, r
}
func (f *liveFixture) advance(t *testing.T, who file.Owner, id file.ID, want file.State) {
	t.Helper()
	for attempt := 0; attempt < 200; attempt++ {
		r, err := f.control.Status(f.ctx, who, id)
		if err != nil {
			t.Fatal(err)
		}
		if r.State == want {
			return
		}
		if r.State == file.Rejected || r.State == file.Expired || r.State == file.Deleted {
			t.Fatalf("unexpected terminal state %s", r.State)
		}
		if err = f.worker.Step(f.ctx); err != nil && !errors.Is(err, file.ErrRejected) {
			t.Fatalf("worker step: %v", err)
		}
	}
	t.Fatal("state did not advance")
}

func TestLiveFilesPipeline(t *testing.T) {
	f := live(t)
	for _, format := range []file.Format{file.PNG, file.JPEG, file.PDF, file.DOCX, file.XLSX, file.PPTX, file.ODT, file.ODS, file.ODP} {
		t.Run(format.Extension(), func(t *testing.T) {
			data := sample(t, format)
			who, r := f.upload(t, data, format)
			f.advance(t, who, r.ID, file.Ready)
			ready, err := f.control.Status(f.ctx, who, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if ready.CleanFormat != file.PNG && ready.CleanFormat != file.PDF {
				t.Fatal("original office format leaked")
			}
			cap, err := f.control.Download(f.ctx, who, r.ID)
			if err != nil {
				t.Fatal("download capability", err)
			}
			req, _ := http.NewRequestWithContext(f.ctx, "GET", cap.URL, nil)
			response, err := f.http.Do(req)
			if err != nil {
				t.Fatal("download transport")
			}
			defer response.Body.Close()
			clean, err := io.ReadAll(io.LimitReader(response.Body, file.MaxSize+1))
			if err != nil || response.StatusCode != 200 || int64(len(clean)) != ready.CleanSize || file.Digest(sha256.Sum256(clean)) != ready.CleanSHA256 {
				t.Fatal("clean integrity mismatch")
			}
			if !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment;") || response.Header.Get("Cache-Control") != "private, no-store" {
				t.Fatal("unsafe delivery headers")
			}
			for _, marker := range []string{"/JavaScript", "/OpenAction", "/EmbeddedFile", "/AcroForm"} {
				if bytes.Contains(clean, []byte(marker)) {
					t.Fatal("active PDF material survived")
				}
			}
			// Independently read both DC copies again; one successful GET is insufficient.
			check := ready
			check.State = file.Replicating
			if err = f.workerStore.Replicate(f.ctx, check); err != nil {
				t.Fatal("two-DC integrity", err)
			}
			if err = f.control.Delete(f.ctx, who, r.ID); err != nil {
				t.Fatal(err)
			}
			if err = f.control.Delete(f.ctx, who, r.ID); err != nil {
				t.Fatal("delete not idempotent", err)
			}
			if _, err = f.control.Download(f.ctx, who, r.ID); !errors.Is(err, file.ErrNotReady) {
				t.Fatal("deleted file downloadable")
			}
			deleted, err := f.control.Status(f.ctx, who, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.workerStore.Purge(f.ctx, deleted); err != nil {
				t.Fatal("purge", err)
			}
			// Sandbox is deliberately one-shot; wait for the Compose restart before next job.
			time.Sleep(2 * time.Second)
		})
	}
}

func TestLiveRestartAndDeliveryOutage(t *testing.T) {
	f := live(t)
	who, r := f.upload(t, sample(t, file.PNG), file.PNG)
	f.advance(t, who, r.ID, file.Replicating)
	// Re-create application/pools as after a pod restart; all progress comes from PG/S3.
	restarted := live(t)
	record, err := restarted.control.Status(f.ctx, who, r.ID)
	if err != nil || record.State != file.Replicating {
		t.Fatal("durable state lost on restart")
	}
	cfg, err := readFilesConfig(filepath.Join(os.Getenv("FILES_FIXTURE"), "worker.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Delivery[1].APIEndpoint = "https://delivery-b:1"
	unavailable, err := storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := unavailable.Replicate(f.ctx, record); err == nil {
		t.Fatal("partial replica attempt succeeded")
	}
	unchanged, err := f.control.Status(f.ctx, who, r.ID)
	if err != nil || unchanged.State != file.Replicating {
		t.Fatal("unreplicated file became ready")
	}
	f.advance(t, who, r.ID, file.Ready)
	cfg, err = readFilesConfig(filepath.Join(os.Getenv("FILES_FIXTURE"), "control.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Delivery[0].APIEndpoint = "https://delivery-a:1"
	fallbackStore, err := storage(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fallback, err := files.New(restarted.repo, fallbackStore, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	cap, err := fallback.Download(f.ctx, who, r.ID)
	if err != nil || !strings.HasPrefix(cap.URL, "https://delivery-b:8333/") {
		t.Fatal("delivery failover unavailable")
	}
	if err := fallback.Delete(f.ctx, who, r.ID); err != nil {
		t.Fatal(err)
	}
}

func TestLiveDatabaseQuotaAndIsolation(t *testing.T) {
	f := live(t)
	who := owner(t)
	m := manifest([]byte("test"), file.PDF)
	var wg sync.WaitGroup
	results := make(chan error, 24)
	for range 24 {
		key := opaque(t)
		wg.Add(1)
		go func() { defer wg.Done(); _, err := f.control.Create(f.ctx, who, key, m); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted, limited := 0, 0
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, file.ErrLimit) {
			limited++
		} else {
			t.Fatal("concurrent create", err)
		}
	}
	if accepted != 10 || limited != 14 {
		t.Fatalf("quota race accepted=%d limited=%d", accepted, limited)
	}
	// The synchronous replica must already expose acknowledged metadata.
	var count int
	if err := f.db.RO().QueryRow(f.ctx, "SELECT count(*) FROM files.uploads WHERE tenant_id=$1 AND owner_id=$2", who.Tenant[:], who.Subject[:]).Scan(&count); err != nil || count != 10 {
		t.Fatal("sync metadata lost", err)
	}
	if _, err := f.db.RO().Exec(f.ctx, "UPDATE files.uploads SET version=version+1"); err == nil {
		t.Fatal("RO role wrote metadata")
	}
}

func TestLiveAntivirusRejectsAndLimits(t *testing.T) {
	f := live(t)
	// Harmless industry-standard antivirus test string, assembled to avoid file scanners.
	eicar := []byte("X5O!P%@AP[4\\PZX54(P^)7CC)7}$" + "EICAR-STANDARD-ANTIVIRUS-TEST-FILE!$H+H*")
	if err := f.av.Scan(f.ctx, bytes.NewReader(eicar), int64(len(eicar))); !errors.Is(err, file.ErrRejected) {
		t.Fatal("AV did not reject EICAR", err)
	}
	var bomb bytes.Buffer
	z := zip.NewWriter(&bomb)
	entry, err := z.Create("expanded.txt")
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1024*1024)
	for range 513 {
		if _, err = entry.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if err = z.Close(); err != nil {
		t.Fatal(err)
	}
	// ClamAV may return OK for a skipped ZIP (#633); independent container
	// limits must still reject it before any clean object can become READY.
	bombOwner, bombRecord := f.upload(t, bomb.Bytes(), file.DOCX)
	f.advance(t, bombOwner, bombRecord.ID, file.Rejected)
	who, r := f.upload(t, eicar, file.PDF)
	f.advance(t, who, r.ID, file.Rejected)
}

func sample(t *testing.T, format file.Format) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 12, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 12; x++ {
			img.Set(x, y, color.NRGBA{R: byte(x * 20), G: byte(y * 30), A: 255})
		}
	}
	switch format {
	case file.PNG:
		if err := png.Encode(&data, img); err != nil {
			t.Fatal(err)
		}
	case file.JPEG:
		if err := jpeg.Encode(&data, img, nil); err != nil {
			t.Fatal(err)
		}
	case file.PDF:
		var frames bytes.Buffer
		raster.Header(&frames, 1)
		raster.Page(&frames, img)
		if _, err := raster.Reconstruct(&frames, false, &data); err != nil {
			t.Fatal(err)
		}
	default:
		path := filepath.Join(os.Getenv("FILES_FIXTURE"), "documents", "sample."+format.Extension())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("office fixture unavailable", format.Extension())
		}
		return raw
	}
	return data.Bytes()
}

// Keep fixture parsing strict: private credentials are never formatted by tests.
func fixtureJSON(t *testing.T, name string, destination any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("FILES_FIXTURE"), name))
	if err != nil || json.Unmarshal(raw, destination) != nil {
		t.Fatal("fixture JSON unavailable")
	}
}
