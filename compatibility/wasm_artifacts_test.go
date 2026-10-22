package compatibility_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const bindingModule = "github.com/idena-network/idena-wasm-binding"

type artifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

func TestBindingLibrariesMatchLockDigests(t *testing.T) {
	raw, err := os.ReadFile("stack-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Artifacts []artifact `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	if len(lock.Artifacts) == 0 {
		t.Fatal("compatibility lock lists no artifacts")
	}

	module := bindingModuleInfo(t)
	if module.Replace == nil || module.Replace.Path != "github.com/truongfelix/idena-wasm-binding" {
		t.Fatalf("binding module is not replaced by the locked fork: %+v", module.Replace)
	}
	if !strings.HasSuffix(module.Replace.Version, "-"+wantBindingCommit[:12]) {
		t.Fatalf("binding module version %q is not the locked commit %s", module.Replace.Version, wantBindingCommit)
	}
	libDir := filepath.Join(module.Replace.Dir, "lib")

	want := make(map[string]string, len(lock.Artifacts))
	for _, item := range lock.Artifacts {
		if _, exists := want[item.Name]; exists {
			t.Fatalf("duplicate artifact %q", item.Name)
		}
		if filepath.Base(item.Name) != item.Name || !strings.HasPrefix(item.Name, "libidena_wasm_") || filepath.Ext(item.Name) != ".a" {
			t.Fatalf("artifact %q is not a binding library name", item.Name)
		}
		want[item.Name] = item.SHA256
	}
	found, err := filepath.Glob(filepath.Join(libDir, "*.a"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, file := range found {
		got = append(got, filepath.Base(file))
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("binding module has libraries %v, the lock lists %d", got, len(want))
	}
	for _, name := range got {
		digest, listed := want[name]
		if !listed {
			t.Fatalf("binding library %q is not in the lock", name)
		}
		content, err := os.ReadFile(filepath.Join(libDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if sum := fmt.Sprintf("%x", sha256.Sum256(content)); sum != digest {
			t.Fatalf("binding library %q digest %s, the lock says %s", name, sum, digest)
		}
	}
	t.Logf("%d binding libraries match the lock (%s)", len(got), module.Replace.Version)
}

type moduleInfo struct {
	Path    string
	Version string
	Replace *struct {
		Path    string
		Version string
		Dir     string
	}
}

func bindingModuleInfo(t *testing.T) moduleInfo {
	t.Helper()
	// go test puts its own go command first in PATH.
	cmd := exec.Command("go", "mod", "download", "-json", bindingModule)
	cmd.Dir = ".."
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go mod download %s: %v\n%s", bindingModule, err, output)
	}
	cmd = exec.Command("go", "list", "-m", "-json", bindingModule)
	cmd.Dir = ".."
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m %s: %v", bindingModule, err)
	}
	var info moduleInfo
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatal(err)
	}
	if info.Replace == nil || info.Replace.Dir == "" {
		t.Fatalf("binding module has no replacement directory: %s", output)
	}
	return info
}
