package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseCompatibilityValidatorVerifiesEvidenceDigest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release validation runs on Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	const (
		gate         = "legacy-state-replay-differential"
		testedCommit = "0123456789abcdef0123456789abcdef01234567"
		evidencePath = "compatibility/evidence/state-replay.json"
	)
	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility", "evidence"), 0700))
	evidence, err := json.Marshal(map[string]any{
		"schema":       1,
		"gate":         gate,
		"status":       "passed",
		"testedCommit": testedCommit,
		"source":       "https://github.com/example/repository/actions/runs/123456789",
	})
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(evidencePath)), evidence, 0600))
	digest := fmt.Sprintf("%x", sha256.Sum256(evidence))

	lock := map[string]any{
		"status":        "approved",
		"requiredGates": []string{gate},
		"gateResults": map[string]any{
			gate: map[string]any{
				"status":   "passed",
				"evidence": evidencePath,
				"sha256":   digest,
			},
		},
		"components": []map[string]any{
			{
				"name":              "idena-go",
				"commit":            testedCommit,
				"runtimeCodeCommit": testedCommit,
			},
		},
		"consumerPins": map[string]any{
			"idena-desktop": map[string]any{"idena-go": testedCommit},
		},
	}
	lockPath := filepath.Join(root, "compatibility", "stack-lock.json")
	writeTestJSON(t, lockPath, lock)

	output, err := exec.Command(python, "check_release_compatibility.py", lockPath, root).CombinedOutput()
	if err != nil {
		t.Fatalf("valid evidence was rejected: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "evidence passed") {
		t.Fatalf("unexpected validator output: %s", output)
	}

	lock["gateResults"].(map[string]any)[gate].(map[string]any)["sha256"] = strings.Repeat("0", 64)
	writeTestJSON(t, lockPath, lock)
	output, err = exec.Command(python, "check_release_compatibility.py", lockPath, root).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "digest does not match") {
		t.Fatalf("mismatched digest was not rejected: err=%v output=%s", err, output)
	}

	lock["gateResults"].(map[string]any)[gate].(map[string]any)["sha256"] = digest
	lock["consumerPins"].(map[string]any)["idena-desktop"].(map[string]any)["idena-go"] = strings.Repeat("f", 40)
	writeTestJSON(t, lockPath, lock)
	output, err = exec.Command(python, "check_release_compatibility.py", lockPath, root).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "idena-go pin") {
		t.Fatalf("mismatched consumer pin was not rejected: err=%v output=%s", err, output)
	}

	lock["consumerPins"].(map[string]any)["idena-desktop"].(map[string]any)["idena-go"] = testedCommit
	lock["components"].([]map[string]any)[0]["commit"] = strings.Repeat("f", 40)
	writeTestJSON(t, lockPath, lock)
	output, err = exec.Command(python, "check_release_compatibility.py", lockPath, root).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "component commit") {
		t.Fatalf("mismatched component commit was not rejected: err=%v output=%s", err, output)
	}
}

func TestCompatibilityRuntimeGuardUsesRuntimeCodeCommit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compatibility runtime validation runs in bash")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility"), 0700))
	script, err := os.ReadFile("check-compatibility-runtime.sh")
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "scripts", "check-compatibility-runtime.sh"), script, 0700))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package runtime\n"), 0600))
	runTestCommand(t, root, git, "init", "-q")
	runTestCommand(t, root, git, "config", "user.email", "test@example.com")
	runTestCommand(t, root, git, "config", "user.name", "Compatibility Test")
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "runtime baseline")
	runtimeCommit := strings.TrimSpace(runTestCommand(t, root, git, "rev-parse", "HEAD"))

	requireTestNoError(t, os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package runtime\n\nconst changed = true\n"), 0600))
	lock := map[string]any{
		"components": []map[string]any{{
			"name":              "idena-go",
			"commit":            strings.Repeat("f", 40),
			"runtimeCodeCommit": runtimeCommit,
		}},
	}
	writeTestJSON(t, filepath.Join(root, "compatibility", "stack-lock.json"), lock)
	dirtyCommand := exec.Command(bash, filepath.Join(root, "scripts", "check-compatibility-runtime.sh"))
	dirtyCommand.Dir = root
	dirtyOutput, dirtyErr := dirtyCommand.CombinedOutput()
	if dirtyErr == nil || !strings.Contains(string(dirtyOutput), "requires a clean worktree") {
		t.Fatalf("dirty runtime change was not rejected: err=%v output=%s", dirtyErr, dirtyOutput)
	}
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "change runtime")

	command := exec.Command(bash, filepath.Join(root, "scripts", "check-compatibility-runtime.sh"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "Runtime-affecting path changed") {
		t.Fatalf("runtime change after runtimeCodeCommit was not rejected: err=%v output=%s", err, output)
	}
}

func TestCompatibilityRuntimeGuardAllowsReleaseVerifierChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compatibility runtime validation runs in bash")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility"), 0700))
	script, err := os.ReadFile("check-compatibility-runtime.sh")
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "scripts", "check-compatibility-runtime.sh"), script, 0700))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package runtime\n"), 0600))
	verifierPath := filepath.Join(root, "scripts", "verify_release_artifacts.py")
	requireTestNoError(t, os.WriteFile(verifierPath, []byte("print('initial')\n"), 0600))
	runTestCommand(t, root, git, "init", "-q")
	runTestCommand(t, root, git, "config", "user.email", "test@example.com")
	runTestCommand(t, root, git, "config", "user.name", "Compatibility Test")
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "runtime baseline")
	runtimeCommit := strings.TrimSpace(runTestCommand(t, root, git, "rev-parse", "HEAD"))

	writeTestJSON(t, filepath.Join(root, "compatibility", "stack-lock.json"), map[string]any{
		"components": []map[string]any{{
			"name":              "idena-go",
			"runtimeCodeCommit": runtimeCommit,
		}},
	})
	requireTestNoError(t, os.WriteFile(verifierPath, []byte("print('hardened')\n"), 0600))
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "harden release verifier")

	command := exec.Command(bash, filepath.Join(root, "scripts", "check-compatibility-runtime.sh"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "runtime boundary passed") {
		t.Fatalf("release verifier change was rejected: err=%v output=%s", err, output)
	}
}

func TestCompatibilityRuntimeGuardAllowsGoTestsAndTestdata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compatibility runtime validation runs in bash")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "api", "testdata"), 0700))
	script, err := os.ReadFile("check-compatibility-runtime.sh")
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "scripts", "check-compatibility-runtime.sh"), script, 0700))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package runtime\n"), 0600))
	runTestCommand(t, root, git, "init", "-q")
	runTestCommand(t, root, git, "config", "user.email", "test@example.com")
	runTestCommand(t, root, git, "config", "user.name", "Compatibility Test")
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "runtime baseline")
	runtimeCommit := strings.TrimSpace(runTestCommand(t, root, git, "rev-parse", "HEAD"))

	writeTestJSON(t, filepath.Join(root, "compatibility", "stack-lock.json"), map[string]any{
		"components": []map[string]any{{
			"name":              "idena-go",
			"runtimeCodeCommit": runtimeCommit,
		}},
	})
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "api", "dna_api_test.go"), []byte("package api\n"), 0600))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "api", "testdata", "vector.json"), []byte("{}\n"), 0600))
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "add compatibility tests")

	command := exec.Command(bash, filepath.Join(root, "scripts", "check-compatibility-runtime.sh"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "runtime boundary passed") {
		t.Fatalf("test-only changes were rejected: err=%v output=%s", err, output)
	}
}

func TestCompatibilityRuntimeGuardAllowsGitHubConfigChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("compatibility runtime validation runs in bash")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}

	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "scripts"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility"), 0700))
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, ".github", "workflows"), 0700))
	script, err := os.ReadFile("check-compatibility-runtime.sh")
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "scripts", "check-compatibility-runtime.sh"), script, 0700))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, "runtime.go"), []byte("package runtime\n"), 0600))
	workflowPath := filepath.Join(root, ".github", "workflows", "main.yml")
	requireTestNoError(t, os.WriteFile(workflowPath, []byte("on:\n  push:\n"), 0600))
	runTestCommand(t, root, git, "init", "-q")
	runTestCommand(t, root, git, "config", "user.email", "test@example.com")
	runTestCommand(t, root, git, "config", "user.name", "Compatibility Test")
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "runtime baseline")
	runtimeCommit := strings.TrimSpace(runTestCommand(t, root, git, "rev-parse", "HEAD"))

	writeTestJSON(t, filepath.Join(root, "compatibility", "stack-lock.json"), map[string]any{
		"components": []map[string]any{{
			"name":              "idena-go",
			"runtimeCodeCommit": runtimeCommit,
		}},
	})
	requireTestNoError(t, os.WriteFile(workflowPath, []byte("on:\n  push:\n    branches: [master]\n"), 0600))
	requireTestNoError(t, os.WriteFile(filepath.Join(root, ".github", "dependabot.yml"), []byte("version: 2\n"), 0600))
	runTestCommand(t, root, git, "add", ".")
	runTestCommand(t, root, git, "commit", "-qm", "change CI triggers")

	command := exec.Command(bash, filepath.Join(root, "scripts", "check-compatibility-runtime.sh"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "runtime boundary passed") {
		t.Fatalf("GitHub config changes were rejected: err=%v output=%s", err, output)
	}
}

func TestReleaseArtifactVerifierBindsPublishedBinariesToEvidence(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release validation runs on Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	const (
		gate         = "independent-rebuild-digest-match"
		testedCommit = "0123456789abcdef0123456789abcdef01234567"
		releaseTag   = "v1.2.3-rc.1"
		evidencePath = "compatibility/evidence/independent-rebuild.json"
	)
	root := t.TempDir()
	buildsDir := filepath.Join(root, "builds")
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility", "evidence"), 0700))
	requireTestNoError(t, os.MkdirAll(buildsDir, 0700))

	assetNames := map[string]string{
		"linux-x64":   "idena-node-linux-1.2.3-rc.1",
		"linux-arm64": "idena-node-linux-aarch64-1.2.3-rc.1",
		"windows-x64": "idena-node-win-1.2.3-rc.1.exe",
		"macos-x64":   "idena-node-mac-1.2.3-rc.1",
		"macos-arm64": "idena-node-mac-arm64-1.2.3-rc.1",
	}
	artifacts := make([]map[string]any, 0, len(assetNames))
	for platform, assetName := range assetNames {
		content := []byte("independently rebuilt " + platform)
		digest := fmt.Sprintf("%x", sha256.Sum256(content))
		requireTestNoError(t, os.WriteFile(filepath.Join(buildsDir, assetName), content, 0700))
		requireTestNoError(t, os.WriteFile(
			filepath.Join(buildsDir, assetName+".sha256"),
			[]byte(fmt.Sprintf("%s  %s\n", digest, assetName)),
			0600,
		))
		artifacts = append(artifacts, map[string]any{"platform": platform, "sha256": digest})
	}
	evidence, err := json.Marshal(map[string]any{
		"schema":           1,
		"gate":             gate,
		"status":           "passed",
		"testedCommit":     testedCommit,
		"source":           "https://github.com/example/repository/actions/runs/123456789",
		"releaseTag":       releaseTag,
		"releaseArtifacts": artifacts,
	})
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(filepath.Join(root, filepath.FromSlash(evidencePath)), evidence, 0600))
	lock := map[string]any{
		"status":        "approved",
		"requiredGates": []string{gate},
		"gateResults": map[string]any{
			gate: map[string]any{
				"status":   "passed",
				"evidence": evidencePath,
				"sha256":   fmt.Sprintf("%x", sha256.Sum256(evidence)),
			},
		},
		"components": []map[string]any{{
			"name":              "idena-go",
			"commit":            testedCommit,
			"runtimeCodeCommit": testedCommit,
		}},
		"consumerPins": map[string]any{
			"idena-desktop": map[string]any{"idena-go": testedCommit},
		},
	}
	lockPath := filepath.Join(root, "compatibility", "stack-lock.json")
	writeTestJSON(t, lockPath, lock)

	output, err := exec.Command(
		python,
		"verify_release_artifacts.py",
		lockPath,
		root,
		buildsDir,
		releaseTag,
	).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "match independent rebuild evidence") {
		t.Fatalf("matching release artifacts were rejected: err=%v output=%s", err, output)
	}

	linuxAsset := filepath.Join(buildsDir, assetNames["linux-x64"])
	requireTestNoError(t, os.WriteFile(linuxAsset, []byte("substituted binary"), 0700))
	output, err = exec.Command(
		python,
		"verify_release_artifacts.py",
		lockPath,
		root,
		buildsDir,
		releaseTag,
	).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "digest does not match") {
		t.Fatalf("substituted release artifact was not rejected: err=%v output=%s", err, output)
	}

	linuxContent := []byte("independently rebuilt linux-x64")
	requireTestNoError(t, os.WriteFile(linuxAsset, linuxContent, 0700))
	requireTestNoError(t, os.Mkdir(filepath.Join(buildsDir, "unexpected-directory"), 0700))
	output, err = exec.Command(
		python,
		"verify_release_artifacts.py",
		lockPath,
		root,
		buildsDir,
		releaseTag,
	).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "unexpected release file set") {
		t.Fatalf("unexpected release directory was not rejected: err=%v output=%s", err, output)
	}
	requireTestNoError(t, os.Remove(filepath.Join(buildsDir, "unexpected-directory")))

	linuxDigest := fmt.Sprintf("%x", sha256.Sum256(linuxContent))
	linuxChecksum := linuxAsset + ".sha256"
	requireTestNoError(t, os.WriteFile(
		linuxChecksum,
		[]byte(fmt.Sprintf("%s  **%s\n", linuxDigest, assetNames["linux-x64"])),
		0600,
	))
	output, err = exec.Command(
		python,
		"verify_release_artifacts.py",
		lockPath,
		root,
		buildsDir,
		releaseTag,
	).CombinedOutput()
	if err == nil || !strings.Contains(string(output), "checksum file does not match") {
		t.Fatalf("malformed checksum filename was not rejected: err=%v output=%s", err, output)
	}
}

// A waiver lets the owner release without one gate's evidence: it names the reason, who approved it, the runtime
// commit and the one release tag it covers, and the quick gates can never be waived.
func TestReleaseCompatibilityValidatorAcceptsOnlyBoundWaivers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release validation runs on Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	const (
		gate         = "legacy-state-replay-differential"
		testedCommit = "0123456789abcdef0123456789abcdef01234567"
		releaseTag   = "v1.2.0"
	)
	root := t.TempDir()
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility", "evidence"), 0700))
	waiver := func() map[string]any {
		return map[string]any{
			"status":       "waived",
			"reason":       "consensus v13 freeze released before its voting window",
			"approvedBy":   "owner",
			"date":         "2026-11-02",
			"waivedCommit": testedCommit,
			"releaseTag":   releaseTag,
		}
	}
	lockFor := func(gateName string, result map[string]any) map[string]any {
		return map[string]any{
			"status":        "approved",
			"requiredGates": []string{gateName},
			"gateResults":   map[string]any{gateName: result},
			"components": []map[string]any{{
				"name":              "idena-go",
				"commit":            testedCommit,
				"runtimeCodeCommit": testedCommit,
			}},
			"consumerPins": map[string]any{
				"idena-desktop": map[string]any{"idena-go": testedCommit},
			},
		}
	}
	lockPath := filepath.Join(root, "compatibility", "stack-lock.json")
	check := func(lock map[string]any, args ...string) (string, error) {
		writeTestJSON(t, lockPath, lock)
		output, err := exec.Command(python, append([]string{"check_release_compatibility.py", lockPath, root}, args...)...).CombinedOutput()
		return string(output), err
	}

	output, err := check(lockFor(gate, waiver()), releaseTag)
	if err != nil || !strings.Contains(output, "waived by the owner: "+gate) || !strings.Contains(output, "evidence passed") {
		t.Fatalf("a bound waiver was rejected: err=%v output=%s", err, output)
	}

	refused := []struct {
		name   string
		gate   string
		change func(map[string]any)
		args   []string
		want   string
	}{
		{"no tag given", gate, func(map[string]any) {}, nil, "run the check with the tag"},
		{"another tag", gate, func(map[string]any) {}, []string{"v1.2.1"}, "not release tag 'v1.2.1'"},
		{"another commit", gate, func(r map[string]any) { r["waivedCommit"] = strings.Repeat("f", 40) }, []string{releaseTag}, "different node commit"},
		{"no reason", gate, func(r map[string]any) { r["reason"] = " " }, []string{releaseTag}, "non-empty reason"},
		{"no approver", gate, func(r map[string]any) { delete(r, "approvedBy") }, []string{releaseTag}, "non-empty approvedBy"},
		{"bad date", gate, func(r map[string]any) { r["date"] = "11/02/2026" }, []string{releaseTag}, "needs a date"},
		{"unknown field", gate, func(r map[string]any) { r["evidence"] = "compatibility/evidence/x.json" }, []string{releaseTag}, "unknown fields: evidence"},
		{"stack lock gate", "stack-lock-validation", func(map[string]any) {}, []string{releaseTag}, "cannot be waived"},
		{"secret scan gate", "secret-scan", func(map[string]any) {}, []string{releaseTag}, "cannot be waived"},
		{"dependency gate", "dependency-policy", func(map[string]any) {}, []string{releaseTag}, "cannot be waived"},
	}
	for _, tc := range refused {
		result := waiver()
		tc.change(result)
		output, err := check(lockFor(tc.gate, result), tc.args...)
		if err == nil || !strings.Contains(output, tc.want) {
			t.Fatalf("%s: waiver was not rejected with %q: err=%v output=%s", tc.name, tc.want, err, output)
		}
	}
}

// With the independent rebuild waived, the release job still refuses a missing, extra or mismatched release file.
func TestReleaseArtifactVerifierWithWaivedRebuildChecksChecksums(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("release validation runs on Linux")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}

	const (
		gate         = "independent-rebuild-digest-match"
		testedCommit = "0123456789abcdef0123456789abcdef01234567"
		releaseTag   = "v1.2.0"
	)
	root := t.TempDir()
	buildsDir := filepath.Join(root, "builds")
	requireTestNoError(t, os.MkdirAll(filepath.Join(root, "compatibility", "evidence"), 0700))
	requireTestNoError(t, os.MkdirAll(buildsDir, 0700))
	assetNames := []string{
		"idena-node-linux-1.2.0",
		"idena-node-linux-aarch64-1.2.0",
		"idena-node-win-1.2.0.exe",
		"idena-node-mac-1.2.0",
		"idena-node-mac-arm64-1.2.0",
	}
	for _, assetName := range assetNames {
		content := []byte("release build " + assetName)
		requireTestNoError(t, os.WriteFile(filepath.Join(buildsDir, assetName), content, 0700))
		requireTestNoError(t, os.WriteFile(
			filepath.Join(buildsDir, assetName+".sha256"),
			[]byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(content), assetName)),
			0600,
		))
	}
	lockPath := filepath.Join(root, "compatibility", "stack-lock.json")
	writeTestJSON(t, lockPath, map[string]any{
		"status":        "approved",
		"requiredGates": []string{gate},
		"gateResults": map[string]any{gate: map[string]any{
			"status":       "waived",
			"reason":       "consensus v13 freeze released before its voting window",
			"approvedBy":   "owner",
			"date":         "2026-11-02",
			"waivedCommit": testedCommit,
			"releaseTag":   releaseTag,
		}},
		"components": []map[string]any{{
			"name":              "idena-go",
			"commit":            testedCommit,
			"runtimeCodeCommit": testedCommit,
		}},
		"consumerPins": map[string]any{
			"idena-desktop": map[string]any{"idena-go": testedCommit},
		},
	})
	verify := func() (string, error) {
		output, err := exec.Command(python, "verify_release_artifacts.py", lockPath, root, buildsDir, releaseTag).CombinedOutput()
		return string(output), err
	}

	output, err := verify()
	if err != nil || !strings.Contains(output, "independent rebuild waived") {
		t.Fatalf("release files with a waived rebuild were rejected: err=%v output=%s", err, output)
	}

	linuxAsset := filepath.Join(buildsDir, assetNames[0])
	requireTestNoError(t, os.WriteFile(linuxAsset, []byte("substituted binary"), 0700))
	output, err = verify()
	if err == nil || !strings.Contains(output, "checksum file does not match") {
		t.Fatalf("a binary that does not match its checksum file was not rejected: err=%v output=%s", err, output)
	}
	requireTestNoError(t, os.WriteFile(linuxAsset, []byte("release build "+assetNames[0]), 0700))

	requireTestNoError(t, os.Remove(linuxAsset+".sha256"))
	output, err = verify()
	if err == nil || !strings.Contains(output, "missing release files") {
		t.Fatalf("a missing checksum file was not rejected: err=%v output=%s", err, output)
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	requireTestNoError(t, err)
	requireTestNoError(t, os.WriteFile(path, raw, 0600))
}

func requireTestNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func runTestCommand(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, output)
	}
	return string(output)
}
