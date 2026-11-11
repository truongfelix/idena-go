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
	"time"

	"github.com/idena-network/idena-go/blockchain"
	"github.com/idena-network/idena-go/common"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/protocol"
)

const (
	wantReleaseID             = "idena-mainnet-legacy-compat-2026.11.09-rc59"
	wantNodeCommit            = "43da1bdcff0feb94ac993c8b48b3bdaaa04aaea9"
	wantRuntimeCommit         = "43da1bdcff0feb94ac993c8b48b3bdaaa04aaea9"
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

// consensusChange declares one consensus version the candidate adds to the legacy rules.
type consensusChange struct {
	Version           uint16   `json:"version"`
	FrozenAddresses   []string `json:"frozenAddresses"`
	VotingWindowStart string   `json:"votingWindowStart"`
	VotingWindowEnd   string   `json:"votingWindowEnd"`
}

type stackLock struct {
	Schema          int    `json:"schema"`
	ReleaseID       string `json:"releaseId"`
	Status          string `json:"status"`
	ChainInvariants struct {
		MainnetNetworkID        uint32            `json:"mainnetNetworkId"`
		GossipProtocol          string            `json:"gossipProtocol"`
		ConsensusChangesAllowed bool              `json:"consensusChangesAllowed"`
		ConsensusChanges        []consensusChange `json:"consensusChanges"`
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
	checkDeclaredConsensusChanges(t, lock)
	if lock.ChainInvariants.MainnetNetworkID != wantMainnetNetwork || uint32(blockchain.Mainnet) != wantMainnetNetwork {
		t.Fatalf("mainnet network ID drifted: lock=%d binary=%d", lock.ChainInvariants.MainnetNetworkID, blockchain.Mainnet)
	}
	if lock.ChainInvariants.GossipProtocol != wantGossipProtocol || string(protocol.IdenaProtocol) != wantGossipProtocol {
		t.Fatalf("gossip protocol drifted: lock=%q binary=%q", lock.ChainInvariants.GossipProtocol, protocol.IdenaProtocol)
	}
}

// The lock declares every consensus change the node carries: a node running the default (legacy) rules keeps the
// v1.1.2 behaviour, and each declared version matches the code exactly (no other version, address or window).
func checkDeclaredConsensusChanges(t *testing.T, lock stackLock) {
	t.Helper()
	changes := lock.ChainInvariants.ConsensusChanges
	if !lock.ChainInvariants.ConsensusChangesAllowed {
		if len(changes) != 0 {
			t.Fatal("consensus changes are declared while the lock allows none")
		}
		if len(config.ConsensusVersions) != int(config.ConsensusV12-config.ConsensusV9+1) {
			t.Fatalf("the node carries %d consensus versions while the lock allows no change", len(config.ConsensusVersions))
		}
		return
	}
	if len(changes) == 0 {
		t.Fatal("the lock allows consensus changes without declaring any")
	}
	for version := config.ConsensusV9; version <= config.ConsensusV12; version++ {
		legacy, ok := config.ConsensusVersions[version]
		if !ok || legacy.Version != version || legacy.EnableUpgrade13 || len(legacy.FrozenAddresses) != 0 {
			t.Fatalf("legacy version %d carries a declared change", version)
		}
	}
	if len(config.ConsensusVersions) != int(config.ConsensusV12-config.ConsensusV9+1)+len(changes) {
		t.Fatalf("the node carries %d consensus versions, the lock declares %d changes", len(config.ConsensusVersions), len(changes))
	}
	for i, change := range changes {
		version := config.ConsensusVerson(change.Version)
		if want := config.ConsensusV12 + config.ConsensusVerson(i) + 1; version != want {
			t.Fatalf("declared change %d is version %d, want %d", i, version, want)
		}
		cfg, ok := config.ConsensusVersions[version]
		if !ok || cfg.Version != version {
			t.Fatalf("declared version %d is not in the node", version)
		}
		if len(cfg.FrozenAddresses) != len(change.FrozenAddresses) {
			t.Fatalf("version %d freezes %d addresses, the lock declares %d", version, len(cfg.FrozenAddresses), len(change.FrozenAddresses))
		}
		for _, address := range change.FrozenAddresses {
			if address != strings.ToLower(address) || !cfg.IsFrozenAddress(common.HexToAddress(address)) {
				t.Fatalf("version %d does not freeze the declared address %s", version, address)
			}
		}
		for name, pair := range map[string][2]interface{}{
			"start": {change.VotingWindowStart, cfg.StartActivationDate},
			"end":   {change.VotingWindowEnd, cfg.EndActivationDate},
		} {
			declared, err := time.Parse(time.RFC3339, pair[0].(string))
			if err != nil {
				t.Fatalf("version %d voting window %s: %v", version, name, err)
			}
			if declared.Unix() != pair[1].(int64) {
				t.Fatalf("version %d voting window %s is %s in the lock, %s in the code", version, name, declared.UTC(), time.Unix(pair[1].(int64), 0).UTC())
			}
		}
	}
}
