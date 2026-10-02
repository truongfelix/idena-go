package depcheck

// This guard protects the Idena peer-record fix in a replaced dependency.
// Upstream kad-dht counts nonpublic advertised addresses when filtering DHT
// responses, which can prevent Idena nodes from finding peers and IPFS blocks.
// The pinned fork ignores those addresses while retaining public IP diversity.

import (
	"encoding/json"
	"os"
	"os/exec"
	"testing"
)

const (
	kadDHTModule         = "github.com/libp2p/go-libp2p-kad-dht"
	patchedKadDHTMod     = "github.com/truongfelix/go-libp2p-kad-dht"
	patchedKadDHTVersion = "v0.41.1-0.20260924080750-83f1403bcb17"
)

func TestKadDHTReplaceDirectivePresent(t *testing.T) {
	// Inspect the selected module, not just a replace line that Go may not use.
	cmd := exec.Command("go", "list", "-mod=readonly", "-m", "-json", kadDHTModule)
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Fatalf("resolve %s: %v: %s", kadDHTModule, err, exitErr.Stderr)
		}
		t.Fatalf("resolve %s: %v", kadDHTModule, err)
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
	if selected.Path != kadDHTModule || selected.Replace == nil ||
		selected.Replace.Path != patchedKadDHTMod ||
		selected.Replace.Version != patchedKadDHTVersion {
		t.Fatalf("%s must resolve to %s@%s (containing the Idena DHT fix); got %+v",
			kadDHTModule, patchedKadDHTMod, patchedKadDHTVersion, selected)
	}
}
