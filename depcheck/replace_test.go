package depcheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

// requireReplacement fails unless module resolves to path@version. It inspects
// the selected module, not just a replace line that Go may not use.
func requireReplacement(t *testing.T, module, path, version, reason string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-mod=readonly", "-m", "-json", module)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("resolve %s: %v: %s", module, err, exitErr.Stderr)
		}
		t.Fatalf("resolve %s: %v", module, err)
	}

	var selected struct {
		Path    string
		Replace *struct {
			Path    string
			Version string
		}
	}
	if err := json.Unmarshal(output, &selected); err != nil {
		t.Fatalf("parse selected module: %v", err)
	}
	if selected.Path != module || selected.Replace == nil ||
		selected.Replace.Path != path ||
		selected.Replace.Version != version {
		t.Fatalf("%s must resolve to %s@%s (%s); got %+v",
			module, path, version, reason, selected)
	}
}
