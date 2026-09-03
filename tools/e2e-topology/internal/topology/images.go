package topology

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxLoadImages = 8

// imageRefPattern accepts only lowercase repository references with an explicit tag.
var imageRefPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._/-][a-z0-9]+)*:[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// LoadImages exports exact image references from the host Docker store and
// imports them into the containerd of every owned k3s machine, verifying each
// import. It fails closed: machines must be running and owned by this instance.
func (t *Topology) LoadImages(ctx context.Context, refs []string) error {
	if len(refs) == 0 || len(refs) > maxLoadImages {
		return fmt.Errorf("topology: load-images accepts 1..%d explicit image references", maxLoadImages)
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateImageRef(ref); err != nil {
			return err
		}
		if _, exists := seen[ref]; exists {
			return fmt.Errorf("topology: duplicate image reference %q", ref)
		}
		seen[ref] = struct{}{}
	}
	if err := t.orbStackReady(ctx); err != nil {
		return err
	}

	machines := make([]orbMachine, 0, len(t.config.Clusters()))
	for _, cluster := range t.config.Clusters() {
		machine, err := t.requireRunningMachine(ctx, cluster)
		if err != nil {
			return err
		}
		if !vmUserPattern.MatchString(machine.Config.DefaultUsername) {
			return fmt.Errorf("topology: machine %s has an unexpected default user", cluster.Name)
		}
		machines = append(machines, machine)
	}

	imagesDir := filepath.Join(t.config.StateDir, "images")
	if err := os.MkdirAll(imagesDir, 0o750); err != nil {
		return fmt.Errorf("creating image staging directory: %w", err)
	}

	for _, ref := range refs {
		if err := t.loadImage(ctx, machines, imagesDir, ref); err != nil {
			return err
		}
	}
	for _, machine := range machines {
		if err := t.verifyMachineImages(ctx, machine, refs); err != nil {
			return err
		}
	}

	t.logger.InfoContext(
		ctx,
		"topology images loaded",
		"instance", t.config.Instance,
		"image_count", len(refs),
	)
	return nil
}

// loadImage saves one image on the host and imports it into every machine.
func (t *Topology) loadImage(ctx context.Context, machines []orbMachine, imagesDir, ref string) error {
	tarName := imageTarName(ref)
	tarPath := filepath.Join(imagesDir, tarName)

	commandCtx, cancel := context.WithTimeout(ctx, createTimeout)
	_, err := t.runner.Run(commandCtx, Command{
		Program: "docker",
		Args:    []string{"save", "-o", tarPath, ref},
	})
	cancel()
	if err != nil {
		return fmt.Errorf("saving image %s: %w", ref, err)
	}
	defer func() {
		_ = os.Remove(tarPath)
	}()

	for _, machine := range machines {
		if err := t.pushToMachine(ctx, machine.Name, tarPath, tarName); err != nil {
			return err
		}
		guestTar := "/home/" + machine.Config.DefaultUsername + "/" + tarName
		if err := t.runMachineSudo(ctx, createTimeout, machine.Name,
			"k3s", "ctr", "-n", "k8s.io", "images", "import", guestTar); err != nil {
			return fmt.Errorf("importing image %s in %s: %w", ref, machine.Name, err)
		}
		if _, err := t.runMachineCommand(ctx, commandTimeout, machine.Name, "rm", "-f", guestTar); err != nil {
			return fmt.Errorf("removing image archive in %s: %w", machine.Name, err)
		}
	}
	return nil
}

// verifyMachineImages proves that every exact reference is present in containerd.
func (t *Topology) verifyMachineImages(ctx context.Context, machine orbMachine, refs []string) error {
	result, err := t.runMachineSudoResult(ctx, commandTimeout, machine.Name,
		"k3s", "ctr", "-n", "k8s.io", "images", "ls", "-q")
	if err != nil {
		return fmt.Errorf("listing images in %s: %w", machine.Name, err)
	}
	names := strings.Fields(result.Stdout)
	for _, ref := range refs {
		if !imageImported(names, ref) {
			return fmt.Errorf("topology: image %s is missing in %s after import", ref, machine.Name)
		}
	}
	return nil
}

// runMachineCommand executes one argv-only command as the default machine user.
func (t *Topology) runMachineCommand(
	ctx context.Context,
	timeout time.Duration,
	name string,
	args ...string,
) (Result, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandArgs := []string{"run", "-m", name}
	commandArgs = append(commandArgs, args...)
	return t.runner.Run(commandCtx, Command{Program: "orbctl", Args: commandArgs})
}

// runMachineSudoResult executes one argv-only command as root and returns its output.
func (t *Topology) runMachineSudoResult(
	ctx context.Context,
	timeout time.Duration,
	name string,
	args ...string,
) (Result, error) {
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	commandArgs := []string{"run", "-m", name, "sudo", "-n"}
	commandArgs = append(commandArgs, args...)
	return t.runner.Run(commandCtx, Command{Program: "orbctl", Args: commandArgs})
}

func validateImageRef(ref string) error {
	if !imageRefPattern.MatchString(ref) {
		return fmt.Errorf("topology: image reference %q must be a lowercase repository with an explicit tag", ref)
	}
	return nil
}

// imageTarName derives a filesystem-safe archive name from an image reference.
func imageTarName(ref string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(ref) + ".tar"
}

// imageImported reports whether the reference is present; containerd normalizes
// registry-less references to the docker.io namespace on import.
func imageImported(names []string, ref string) bool {
	for _, name := range names {
		if name == ref || name == "docker.io/"+ref {
			return true
		}
	}
	return false
}
