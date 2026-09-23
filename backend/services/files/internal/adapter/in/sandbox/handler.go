// Package sandbox contains the untrusted parsers. Deploy only in a separate
// container without networking, storage credentials or shared worker temp files.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/content"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/out/raster"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
)

type Handler struct {
	tempDir string
	active  chan struct{}
	done    chan struct{}
}

func New(tempDir string) (*Handler, error) {
	if !filepath.IsAbs(tempDir) {
		return nil, file.ErrInvalid
	}
	return &Handler{tempDir: tempDir, active: make(chan struct{}, 1), done: make(chan struct{})}, nil
}

// Done closes after the only accepted job. The container must then exit so its
// entire PID namespace is destroyed before another private document is parsed.
func (h *Handler) Done() <-chan struct{} { return h.done }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet && r.URL.Path == "/ready" && r.URL.RawQuery == "" {
		if len(h.active) != 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
		return
	}
	if r.Method != http.MethodPost || r.URL.Path != "/clean" || r.URL.RawQuery != "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	format := file.Format(r.Header.Get("Content-Type"))
	if r.ContentLength <= 0 || r.ContentLength > file.MaxSize || format.Extension() == "" {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	select {
	case h.active <- struct{}{}:
		defer close(h.done)
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Minute)
	defer cancel()
	dir, err := os.MkdirTemp(h.tempDir, "job-*")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer os.RemoveAll(dir)
	input := filepath.Join(dir, "input."+format.Extension())
	f, err := os.OpenFile(input, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	n, copyErr := io.Copy(f, http.MaxBytesReader(w, r.Body, file.MaxSize))
	err = content.Validate(f, n, format)
	f.Close()
	if copyErr != nil || n != r.ContentLength || err != nil {
		w.WriteHeader(http.StatusUnprocessableEntity)
		return
	}
	output, err := os.CreateTemp(dir, "pixels-*")
	if err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	defer output.Close()
	if err = convert(ctx, dir, input, format, output); err != nil {
		if errors.Is(err, file.ErrRejected) {
			w.WriteHeader(http.StatusUnprocessableEntity)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}
	if _, err = output.Seek(0, io.SeekStart); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/x-marketmesh-rgb")
	w.WriteHeader(http.StatusOK)
	io.Copy(w, output)
}

func convert(ctx context.Context, dir, input string, format file.Format, output io.Writer) error {
	if format == file.PNG || format == file.JPEG {
		img, err := decodeImage(input)
		if err != nil {
			return err
		}
		if err = raster.Header(output, 1); err != nil {
			return err
		}
		return raster.Page(output, img)
	}
	pdf := input
	if format != file.PDF {
		// No shell, client paths, filenames, converter options or environment.
		if _, err := run(ctx, dir, "/usr/bin/libreoffice", "-env:UserInstallation=file://"+filepath.Join(dir, "profile"), "--headless", "--nologo", "--nodefault", "--nofirststartwizard", "--nolockcheck", "--convert-to", "pdf", "--outdir", dir, input); err != nil {
			return err
		}
		pdf = filepath.Join(dir, "input.pdf")
	}
	stat, err := os.Stat(pdf)
	if err != nil || !stat.Mode().IsRegular() || stat.Size() <= 0 || stat.Size() > file.MaxSize {
		return file.ErrRejected
	}
	info, err := run(ctx, dir, "/usr/bin/pdfinfo", pdf)
	if err != nil {
		return err
	}
	pages := 0
	encrypted := false
	for _, line := range strings.Split(string(info), "\n") {
		if value, ok := strings.CutPrefix(line, "Pages:"); ok {
			pages, err = strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				return file.ErrRejected
			}
		}
		if value, ok := strings.CutPrefix(line, "Encrypted:"); ok {
			encrypted = true
			if strings.TrimSpace(value) != "no" {
				return file.ErrRejected
			}
		}
	}
	if pages <= 0 || pages > raster.MaxPages || !encrypted {
		return file.ErrRejected
	}
	if err = raster.Header(output, uint32(pages)); err != nil {
		return err
	}
	var total int64
	for i := 1; i <= pages; i++ {
		if ctx.Err() != nil {
			return file.ErrUnavailable
		}
		target := filepath.Join(dir, "page")
		if _, err = run(ctx, dir, "/usr/bin/pdftoppm", "-f", strconv.Itoa(i), "-l", strconv.Itoa(i), "-singlefile", "-scale-to", "2400", "-png", "-q", pdf, target); err != nil {
			return err
		}
		img, err := decodeImage(target + ".png")
		if err != nil {
			return err
		}
		total += int64(img.Bounds().Dx()) * int64(img.Bounds().Dy())
		if total > raster.MaxPixels {
			return file.ErrRejected
		}
		if err = raster.Page(output, img); err != nil {
			return err
		}
		if err = os.Remove(target + ".png"); err != nil {
			return file.ErrUnavailable
		}
	}
	return nil
}

func decodeImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, file.ErrRejected
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() > file.MaxSize {
		return nil, file.ErrRejected
	}
	cfg, _, err := image.DecodeConfig(f)
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > raster.MaxEdge || cfg.Height > raster.MaxEdge || int64(cfg.Width)*int64(cfg.Height) > raster.MaxPixels {
		return nil, file.ErrRejected
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return nil, file.ErrUnavailable
	}
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, file.ErrRejected
	}
	return img, nil
}

type boundedOutput struct{ data bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.data.Len()+len(p) > 16*1024 {
		return 0, file.ErrRejected
	}
	return b.data.Write(p)
}

func run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "TMPDIR=" + dir, "LANG=C.UTF-8", "TZ=UTC", "SAL_USE_VCLPLUGIN=svp"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	var out boundedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, file.ErrUnavailable
		}
		var unavailable *exec.Error
		if errors.As(err, &unavailable) {
			return nil, file.ErrUnavailable
		}
		return nil, file.ErrRejected
	}
	return out.data.Bytes(), nil
}
