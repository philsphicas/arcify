// Package tests holds end-to-end integration tests that require live Azure
// credentials and a real VM. They are skipped unless ARCIFY_E2E=1 is set in the
// environment so that `go test ./...` remains hermetic in CI and on dev
// machines.
//
// To run:
//
//	export ARCIFY_E2E=1
//	export ARCIFY_E2E_VM_ID=/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
//	# (optional) export ARCIFY_E2E_ARC_RG=<arc-rg>
//	# (optional) export ARCIFY_E2E_ARC_NAME=<arc-name>
//	go test ./tests/... -run TestE2E -v -timeout 20m
//
// The test invokes the built arcify binary in --dry-run mode by default to
// avoid mutating ARM; set ARCIFY_E2E_LIVE=1 to perform a real enrollment.
package tests

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2E_Smoke(t *testing.T) {
	if os.Getenv("ARCIFY_E2E") != "1" {
		t.Skip("set ARCIFY_E2E=1 to run end-to-end test")
	}
	vmID := os.Getenv("ARCIFY_E2E_VM_ID")
	if vmID == "" {
		t.Fatal("ARCIFY_E2E_VM_ID is required")
	}

	bin, err := buildBinary(t)
	if err != nil {
		t.Fatalf("build arcify: %v", err)
	}

	args := []string{vmID}
	live := os.Getenv("ARCIFY_E2E_LIVE") == "1"
	if !live {
		args = append(args, "--dry-run")
	}
	if rg := os.Getenv("ARCIFY_E2E_ARC_RG"); rg != "" {
		args = append(args, "--arc-rg", rg)
	}
	if name := os.Getenv("ARCIFY_E2E_ARC_NAME"); name != "" {
		args = append(args, "--arc-name", name)
	}

	cmd := exec.Command(bin, args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	t.Logf("arcify stdout:\n%s", stdout.String())
	t.Logf("arcify stderr:\n%s", stderr.String())
	if runErr != nil {
		t.Fatalf("arcify %v: %v", args, runErr)
	}

	// Progress logging always goes to stderr, never stdout.
	if !strings.Contains(stderr.String(), "arcify:") {
		t.Errorf("expected 'arcify:' progress on stderr, got: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "arcify:") {
		t.Errorf("progress text leaked to stdout: %s", stdout.String())
	}

	// Output contract:
	//   --dry-run  -> stdout is empty.
	//   live       -> stdout is a single line: the Arc machine ARM ID.
	out := strings.TrimRight(stdout.String(), "\n")
	if !live {
		if out != "" {
			t.Errorf("--dry-run stdout should be empty, got %q", out)
		}
		return
	}
	if strings.Contains(out, "\n") {
		t.Errorf("live stdout should be a single line, got %q", out)
	}
	if !strings.HasPrefix(out, "/subscriptions/") ||
		!strings.Contains(out, "/providers/Microsoft.HybridCompute/machines/") {
		t.Errorf("live stdout should be an Arc machine ARM ID, got %q", out)
	}
}

func buildBinary(t *testing.T) (string, error) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "arcify")
	cmd := exec.Command("go", "build", "-o", out, "..")
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out, nil
}
