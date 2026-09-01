package topology

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxDownloadSize = 128 << 20

type asset struct {
	url    string
	sha256 string
}

var kindAssets = map[string]asset{
	"darwin/amd64": {
		url:    "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-darwin-amd64",
		sha256: "5a99f26f57246dc9319dd294803313197a0f34d33c525b3ea8b655db5916ece0",
	},
	"darwin/arm64": {
		url:    "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-darwin-arm64",
		sha256: "0c8c7dbe5e23594a198b786c4bc13dacc101fa6196b0cb0b23a1ca44e61f4b4f",
	},
	"linux/amd64": {
		url:    "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-amd64",
		sha256: "aee6151561422756b764a4ae28e7f44cda5af5a9eead3cc9985112b1de8d8e0d",
	},
	"linux/arm64": {
		url:    "https://github.com/kubernetes-sigs/kind/releases/download/v0.33.0/kind-linux-arm64",
		sha256: "20022bee6cfcd5086cb7234d218e3454e6090022f2a8f55d1fa7fcf42c3867a2",
	},
}

var kubectlAssets = map[string]asset{
	"darwin/amd64": {
		url:    "https://dl.k8s.io/release/v1.37.0/bin/darwin/amd64/kubectl",
		sha256: "d5276c0f4fde77fc446070290f345944a7f1fda153df6b960e5fde93b7a9bccd",
	},
	"darwin/arm64": {
		url:    "https://dl.k8s.io/release/v1.37.0/bin/darwin/arm64/kubectl",
		sha256: "583beedaebe422e71d3f1a96acef8b1fef86ea2f09a45ad01aa6c9ce287c1380",
	},
	"linux/amd64": {
		url:    "https://dl.k8s.io/release/v1.37.0/bin/linux/amd64/kubectl",
		sha256: "6129359f4e1f3848a5572ccb0b26cf28b8ca08cef38c95a765b2f64a2c961a2f",
	},
	"linux/arm64": {
		url:    "https://dl.k8s.io/release/v1.37.0/bin/linux/arm64/kubectl",
		sha256: "922df28df248cc00a9e025f947704f1d1482de64ece54cfe57e61f19eaf1eef3",
	},
}

// Bootstrapper installs the pinned local toolchain and builds the TCP probe.
type Bootstrapper struct {
	config     Config
	runner     Runner
	logger     *slog.Logger
	httpClient *http.Client
	goos       string
	goarch     string
}

// NewBootstrapper constructs a bootstrapper for a validated topology configuration.
func NewBootstrapper(config Config, runner Runner, logger *slog.Logger) *Bootstrapper {
	return &Bootstrapper{
		config: config,
		runner: runner,
		logger: logger,
		httpClient: &http.Client{
			Timeout: 2 * time.Minute,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("topology: too many download redirects")
				}
				return validateDownloadURL(request.URL)
			},
		},
		goos:   runtime.GOOS,
		goarch: runtime.GOARCH,
	}
}

// Bootstrap verifies or installs every pinned tool required by the topology.
func (b *Bootstrapper) Bootstrap(ctx context.Context) error {
	platform := b.goos + "/" + b.goarch
	kindAsset, ok := kindAssets[platform]
	if !ok {
		return fmt.Errorf("topology: kind is not pinned for %s", platform)
	}
	kubectlAsset, ok := kubectlAssets[platform]
	if !ok {
		return fmt.Errorf("topology: kubectl is not pinned for %s", platform)
	}

	if err := os.MkdirAll(b.config.BinDir, 0o750); err != nil {
		return fmt.Errorf("creating topology bin directory: %w", err)
	}
	if err := b.ensureAsset(ctx, "kind", b.config.KindPath, kindAsset); err != nil {
		return err
	}
	if err := b.ensureAsset(ctx, "kubectl", b.config.KubectlPath, kubectlAsset); err != nil {
		return err
	}
	if err := b.buildProbe(ctx); err != nil {
		return err
	}

	b.logger.InfoContext(
		ctx,
		"topology toolchain is ready",
		"kind_version",
		KindVersion,
		"kubernetes_version",
		KubernetesVersion,
	)
	return nil
}

func (b *Bootstrapper) ensureAsset(
	ctx context.Context,
	name string,
	destination string,
	item asset,
) error {
	matches, err := fileMatchesSHA256(destination, item.sha256)
	if err != nil {
		return fmt.Errorf("checking %s checksum: %w", name, err)
	}
	if matches {
		return nil
	}

	b.logger.InfoContext(ctx, "downloading pinned tool", "tool", name)
	if err := downloadAsset(ctx, b.httpClient, destination, item); err != nil {
		return fmt.Errorf("downloading %s: %w", name, err)
	}
	return nil
}

func (b *Bootstrapper) buildProbe(ctx context.Context) error {
	probeDir := filepath.Join(b.config.RepositoryRoot, "tools", "e2e-topology")
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	_, err := b.runner.Run(buildCtx, Command{
		Program: "go",
		Args: []string{
			"build",
			"-trimpath",
			"-o",
			b.config.ProbePath,
			"./cmd/tcpprobe",
		},
		Env: []string{
			"CGO_ENABLED=0",
			"GOARCH=" + b.goarch,
			"GOOS=linux",
			"GOWORK=off",
		},
		Dir: probeDir,
	})
	if err != nil {
		return fmt.Errorf("building topology tcp probe: %w", err)
	}
	// #nosec G302 -- the probe is an executable built locally from repository source.
	if err := os.Chmod(b.config.ProbePath, 0o750); err != nil {
		return fmt.Errorf("setting tcp probe permissions: %w", err)
	}
	return nil
}

func downloadAsset(
	ctx context.Context,
	client *http.Client,
	destination string,
	item asset,
) (returnErr error) {
	parsedURL, err := url.Parse(item.url)
	if err != nil {
		return errors.New("topology: invalid pinned download url")
	}
	if err := validateDownloadURL(parsedURL); err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("requesting pinned asset: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, response.Body.Close())
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("topology: download returned http status %d", response.StatusCode)
	}
	if response.ContentLength > maxDownloadSize {
		return errors.New("topology: download exceeds size limit")
	}

	return installAsset(destination, response.Body, response.ContentLength, item.sha256)
}

func installAsset(
	destination string,
	source io.Reader,
	contentLength int64,
	expectedChecksum string,
) (returnErr error) {
	if contentLength > maxDownloadSize {
		return errors.New("topology: download exceeds size limit")
	}
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, ".download-*")
	if err != nil {
		return fmt.Errorf("creating temporary download: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("removing temporary download: %w", err))
		}
	}()

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(source, maxDownloadSize+1))
	closeErr := temporary.Close()
	if copyErr != nil {
		return fmt.Errorf("writing downloaded asset: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("closing downloaded asset: %w", closeErr)
	}
	if written > maxDownloadSize {
		return errors.New("topology: download exceeds size limit")
	}
	actualChecksum := hex.EncodeToString(hash.Sum(nil))
	if actualChecksum != expectedChecksum {
		return errors.New("topology: downloaded asset checksum mismatch")
	}
	// #nosec G302 -- the verified pinned asset must be executable by the operator.
	if err := os.Chmod(temporaryPath, 0o750); err != nil {
		return fmt.Errorf("setting downloaded asset permissions: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("installing downloaded asset: %w", err)
	}
	return nil
}

func validateDownloadURL(downloadURL *url.URL) error {
	if downloadURL.Scheme != "https" {
		return errors.New("topology: download url must use https")
	}
	host := strings.ToLower(downloadURL.Hostname())
	isGitHub := host == "github.com" || strings.HasSuffix(host, ".githubusercontent.com")
	isKubernetes := host == "dl.k8s.io" || host == "cdn.dl.k8s.io"
	if !isGitHub && !isKubernetes {
		return errors.New("topology: download host is not allowlisted")
	}
	return nil
}

func fileMatchesSHA256(path, expected string) (matches bool, returnErr error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, root.Close())
	}()

	file, err := root.Open(filepath.Base(path))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, file.Close())
	}()

	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxDownloadSize {
		return false, nil
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxDownloadSize+1))
	if err != nil {
		return false, err
	}
	if written > maxDownloadSize {
		return false, nil
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected, nil
}
