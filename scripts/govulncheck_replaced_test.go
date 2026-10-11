package main

import (
	"bytes"
	"strings"
	"testing"
)

const replacedGoMod = `{"Module":{"Path":"github.com/idena-network/idena-go"},"Replace":[
	{"Old":{"Path":"github.com/cosmos/iavl"},"New":{"Path":"github.com/truongfelix/iavl","Version":"v0.12.3-0.20211223100228-a33b117aa31e"}},
	{"Old":{"Path":"github.com/libp2p/go-libp2p-kad-dht"},"New":{"Path":"github.com/truongfelix/go-libp2p-kad-dht","Version":"v0.41.1-0.20260924080750-83f1403bcb17"}}
]}`

const replacedIndex = `[
	{"path":"github.com/libp2p/go-libp2p-kad-dht","vulns":[{"id":"GO-2024-3218","modified":"2024-12-12T15:16:20Z"}]},
	{"path":"golang.org/x/net","vulns":[{"id":"GO-2099-0002"}]}
]`

func runReplaced(goMod, index, allow string) (int, string) {
	var stderr bytes.Buffer
	code := runReplacedCheck(strings.NewReader(goMod), strings.NewReader(index), &stderr, allow)
	return code, stderr.String()
}

func TestRunReplacedCheckAllowsScopedAdvisory(t *testing.T) {
	code, out := runReplaced(replacedGoMod, replacedIndex, "GO-2024-3218@github.com/libp2p/go-libp2p-kad-dht")

	if code != 0 {
		t.Fatalf("expected success, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "allowed GO-2024-3218 in replaced github.com/libp2p/go-libp2p-kad-dht (built from github.com/truongfelix/go-libp2p-kad-dht v0.41.1-0.20260924080750-83f1403bcb17)") {
		t.Fatalf("missing allowed advisory output: %s", out)
	}
	if !strings.Contains(out, "checked 2 replaced module(s) against 2 indexed module(s)") {
		t.Fatalf("missing count output: %s", out)
	}
}

func TestRunReplacedCheckBlocksUnlistedAdvisory(t *testing.T) {
	code, out := runReplaced(replacedGoMod, replacedIndex, "")

	if code != 1 {
		t.Fatalf("expected policy failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "blocked GO-2024-3218 in replaced github.com/libp2p/go-libp2p-kad-dht") {
		t.Fatalf("missing blocked advisory output: %s", out)
	}
}

func TestRunReplacedCheckBlocksAdvisoryAllowedForAnotherModule(t *testing.T) {
	code, out := runReplaced(replacedGoMod, replacedIndex, "GO-2024-3218@example.com/other")

	if code != 1 {
		t.Fatalf("expected policy failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "blocked GO-2024-3218 in replaced github.com/libp2p/go-libp2p-kad-dht") {
		t.Fatalf("missing blocked advisory output: %s", out)
	}
}

func TestRunReplacedCheckBlocksAdvisoryOfNewReplacement(t *testing.T) {
	goMod := `{"Replace":[{"Old":{"Path":"golang.org/x/net"},"New":{"Path":"../net"}}]}`

	code, out := runReplaced(goMod, replacedIndex, "")

	if code != 1 {
		t.Fatalf("expected policy failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "blocked GO-2099-0002 in replaced golang.org/x/net (built from ../net)") {
		t.Fatalf("missing blocked advisory output: %s", out)
	}
}

func TestRunReplacedCheckIgnoresModulesThatAreNotReplaced(t *testing.T) {
	goMod := `{"Replace":[{"Old":{"Path":"github.com/cosmos/iavl"},"New":{"Path":"github.com/truongfelix/iavl","Version":"v0.12.3"}}]}`

	code, out := runReplaced(goMod, replacedIndex, "")

	if code != 0 {
		t.Fatalf("expected success, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "checked 1 replaced module(s) against 2 indexed module(s)") {
		t.Fatalf("missing count output: %s", out)
	}
}

func TestRunReplacedCheckBlocksUnusedAllowance(t *testing.T) {
	goMod := `{"Replace":[{"Old":{"Path":"github.com/cosmos/iavl"},"New":{"Path":"github.com/truongfelix/iavl","Version":"v0.12.3"}}]}`

	code, out := runReplaced(goMod, replacedIndex, "GO-2024-3218@github.com/libp2p/go-libp2p-kad-dht")

	if code != 1 {
		t.Fatalf("expected policy failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "allowance GO-2024-3218@github.com/libp2p/go-libp2p-kad-dht matches no replaced module") {
		t.Fatalf("missing unused allowance output: %s", out)
	}
}

func TestRunReplacedCheckRejectsUnscopedAllowance(t *testing.T) {
	code, out := runReplaced(replacedGoMod, replacedIndex, "GO-2024-3218")

	if code != 2 {
		t.Fatalf("expected usage failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "replaced-module allowance GO-2024-3218 must name its module") {
		t.Fatalf("missing unscoped allowance output: %s", out)
	}
}

func TestRunReplacedCheckFailsOnEmptyIndex(t *testing.T) {
	code, out := runReplaced(replacedGoMod, `[]`, "GO-2024-3218@github.com/libp2p/go-libp2p-kad-dht")

	if code != 2 {
		t.Fatalf("expected input failure, got exit code %d: %s", code, out)
	}
	if !strings.Contains(out, "the vulnerability index lists no modules") {
		t.Fatalf("missing empty index output: %s", out)
	}
}

func TestRunReplacedCheckFailsOnUnreadableInput(t *testing.T) {
	if code, out := runReplaced(`{`, replacedIndex, ""); code != 2 {
		t.Fatalf("expected go.mod parse failure, got exit code %d: %s", code, out)
	}
	if code, out := runReplaced(replacedGoMod, `<html>`, ""); code != 2 {
		t.Fatalf("expected index parse failure, got exit code %d: %s", code, out)
	}
}
