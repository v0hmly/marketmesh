package fixture

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleRejectsForeignVolumesWithoutContainers(t *testing.T) {
	for _, action := range []string{"up", "reset"} {
		t.Run(action, func(t *testing.T) {
			dir, cmd := lifecycle(t, action, "foreign")
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "another workspace") {
				t.Fatalf("foreign resource accepted: %v %s", err, output)
			}
			if _, err = os.Stat(filepath.Join(dir, ".state/marketmesh-account-testcase/.owner")); err != nil {
				t.Fatal("foreign rejection changed state")
			}
		})
	}
}
func TestLifecycleResetPartialFixtureWithoutComposeParsing(t *testing.T) {
	dir, cmd := lifecycle(t, "reset", "empty")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("partial reset: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(dir, ".state/marketmesh-account-testcase")); !os.IsNotExist(err) {
		t.Fatal("partial state not removed")
	}
}
func lifecycle(t *testing.T, action, mode string) (string, *exec.Cmd) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "infra/account-local")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../../infra/account-local/local.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "local.sh")
	if err = os.WriteFile(script, raw, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, ".state/marketmesh-account-testcase")
	if err = os.MkdirAll(state, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(state, ".owner"), []byte("marketmesh-account-testcase|"+dir+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err = os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
case "$1 $2" in
 "ps -aq"|"network ls") exit 0;;
 "compose --project-directory") printf '%s\n' "$@"; exit 0;;
 "volume ls") if [ "$FAKE_MODE" = foreign ]; then echo old-volume; fi; exit 0;;
 "volume inspect") echo /another/worktree/infra/account-local; exit 0;;
 *) echo unexpected-docker-command >&2; exit 99;;
esac
`
	if err = os.WriteFile(filepath.Join(bin, "docker"), []byte(fake), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, action)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "ACCOUNT_LOCAL_PROJECT=marketmesh-account-testcase", "FAKE_MODE="+mode)
	return dir, cmd
}

func TestLifecycleComposeWithOptionalAnalytics(t *testing.T) {
	for _, enabled := range []string{"false", "true"} {
		t.Run(enabled, func(t *testing.T) {
			_, cmd := lifecycle(t, "status", "empty")
			cmd.Env = append(cmd.Env, "ACCOUNT_RYBBIT_ENABLED="+enabled)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("compose failed: %v %s", err, output)
			}
			if !strings.Contains(string(output), "/account-local/compose.yml") {
				t.Fatal("base compose missing")
			}
			if strings.Contains(string(output), "/rybbit/account.override.yml") != (enabled == "true") {
				t.Fatal("unexpected analytics override")
			}
		})
	}
}
