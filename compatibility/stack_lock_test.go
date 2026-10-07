package compatibility_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/protocol"
)

const (
	wantReleaseID             = "idena-mainnet-legacy-compat-2026.10.07-rc39"
	wantNodeCommit            = "3126ef487c8a0d53ace23f4262ce6a9c7dd37ec4"
	wantRuntimeCommit         = "3126ef487c8a0d53ace23f4262ce6a9c7dd37ec4"
	wantBindingCommit         = "01ccca5cc3c94917725964541954a9f20e3412e9"
	wantGossipProtocol        = "/idena/gossip/1.1.0"
	wantMainnetNetwork uint32 = 1
)

type component struct {
	Name              string `json:"name"`
	Commit            string `json:"commit"`
	RuntimeCodeCommit string `json:"runtimeCodeCommit"`
}

type gateResult struct {
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
	SHA256   string `json:"sha256"`
}

type stackLock struct {
	Schema          int    `json:"schema"`
	ReleaseID       string `json:"releaseId"`
	Status          string `json:"status"`
	ChainInvariants struct {
		MainnetNetworkID        uint32 `json:"mainnetNetworkId"`
		GossipProtocol          string `json:"gossipProtocol"`
		ConsensusChangesAllowed bool   `json:"consensusChangesAllowed"`
	} `json:"chainInvariants"`
	Components    []component                  `json:"components"`
	ConsumerPins  map[string]map[string]string `json:"consumerPins"`
	RequiredGates []string                     `json:"requiredGates"`
	GateResults   map[string]gateResult        `json:"gateResults"`
}

func TestReleaseApprovalRequiresEvidenceForEveryGate(t *testing.T) {
	lock := loadLock(t)
	if lock.Status != "candidate" && lock.Status != "approved" {
		t.Fatalf("unsupported compatibility lock status %q", lock.Status)
	}
	if len(lock.RequiredGates) == 0 {
		t.Fatal("compatibility lock has no required gates")
	}
	seen := make(map[string]struct{}, len(lock.RequiredGates))
	digestPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)
	for _, gate := range lock.RequiredGates {
		if _, exists := seen[gate]; exists {
			t.Fatalf("duplicate required gate %q", gate)
		}
		seen[gate] = struct{}{}
		if lock.Status != "approved" {
			continue
		}
		result, exists := lock.GateResults[gate]
		if !exists || result.Status != "passed" {
			t.Fatalf("approved lock has no passing result for %q", gate)
		}
		evidencePath := path.Clean(result.Evidence)
		if evidencePath != result.Evidence || !strings.HasPrefix(evidencePath, "compatibility/evidence/") || path.Ext(evidencePath) != ".json" {
			t.Fatalf("approved gate %q has an unsafe evidence path", gate)
		}
		if !digestPattern.MatchString(result.SHA256) {
			t.Fatalf("approved gate %q has no evidence digest", gate)
		}
		raw, err := os.ReadFile(filepath.Join("..", filepath.FromSlash(evidencePath)))
		if err != nil {
			t.Fatalf("approved gate %q evidence cannot be read: %v", gate, err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != result.SHA256 {
			t.Fatalf("approved gate %q evidence digest drifted", gate)
		}
		var evidence struct {
			Schema       int    `json:"schema"`
			Gate         string `json:"gate"`
			Status       string `json:"status"`
			Source       string `json:"source"`
			TestedCommit string `json:"testedCommit"`
		}
		if err := json.Unmarshal(raw, &evidence); err != nil {
			t.Fatalf("approved gate %q evidence is invalid JSON: %v", gate, err)
		}
		if evidence.Schema != 1 || evidence.Gate != gate || evidence.Status != "passed" || evidence.TestedCommit != wantRuntimeCommit || !strings.HasPrefix(evidence.Source, "https://") {
			t.Fatalf("approved gate %q evidence metadata is invalid", gate)
		}
	}
	for gate := range lock.GateResults {
		if _, exists := seen[gate]; !exists {
			t.Fatalf("result provided for unrequired gate %q", gate)
		}
	}
}

func loadLock(t *testing.T) stackLock {
	t.Helper()
	raw, err := os.ReadFile("stack-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock stackLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestStackLockPinsReviewedRuntime(t *testing.T) {
	lock := loadLock(t)
	if lock.Schema != 1 || lock.ReleaseID != wantReleaseID {
		t.Fatalf("unexpected compatibility lock identity: schema=%d release=%q status=%q", lock.Schema, lock.ReleaseID, lock.Status)
	}
	components := make(map[string]component, len(lock.Components))
	for _, item := range lock.Components {
		if _, exists := components[item.Name]; exists {
			t.Fatalf("duplicate component %q", item.Name)
		}
		components[item.Name] = item
	}
	if got := components["idena-go"]; got.Commit != wantNodeCommit || got.RuntimeCodeCommit != wantRuntimeCommit {
		t.Fatalf("idena-go lock drifted: commit=%q runtime=%q", got.Commit, got.RuntimeCodeCommit)
	}
	for _, consumer := range []string{"idena-desktop", "idena-social-contract-runner", "idena-web", "idena-indexer", "P2poolBTC"} {
		pins, exists := lock.ConsumerPins[consumer]
		if !exists || pins["idena-go"] != wantNodeCommit {
			t.Fatalf("%s idena-go pin drifted: %q", consumer, pins["idena-go"])
		}
	}
	for consumer, pins := range lock.ConsumerPins {
		if commit, exists := pins["idena-go"]; exists && commit != wantNodeCommit {
			t.Fatalf("%s idena-go pin drifted: %q", consumer, commit)
		}
	}
	if got := components["idena-wasm-binding"].Commit; got != wantBindingCommit {
		t.Fatalf("binding lock drifted: %q", got)
	}
	sha1 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for name, item := range components {
		if !sha1.MatchString(item.Commit) {
			t.Fatalf("component %q has an invalid commit", name)
		}
	}
}

func TestCompiledChainIdentifiersMatchLock(t *testing.T) {
	lock := loadLock(t)
	if lock.ChainInvariants.ConsensusChangesAllowed {
		t.Fatal("compatibility candidate permits consensus changes")
	}
	if lock.ChainInvariants.MainnetNetworkID != wantMainnetNetwork || uint32(blockchain.Mainnet) != wantMainnetNetwork {
		t.Fatalf("mainnet network ID drifted: lock=%d binary=%d", lock.ChainInvariants.MainnetNetworkID, blockchain.Mainnet)
	}
	if lock.ChainInvariants.GossipProtocol != wantGossipProtocol || string(protocol.IdenaProtocol) != wantGossipProtocol {
		t.Fatalf("gossip protocol drifted: lock=%q binary=%q", lock.ChainInvariants.GossipProtocol, protocol.IdenaProtocol)
	}
}
