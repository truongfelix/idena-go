package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type govulncheckMessage struct {
	Finding *govulncheckFinding `json:"finding"`
}

type govulncheckFinding struct {
	OSV   string             `json:"osv"`
	Trace []govulncheckFrame `json:"trace"`
}

type govulncheckFrame struct {
	Module   string `json:"module"`
	Package  string `json:"package"`
	Function string `json:"function"`
}

type allowPolicy struct {
	allowAll bool
	modules  map[string]bool
}

type findingKey struct {
	osv    string
	module string
}

func main() {
	allowReachableFlag := flag.String("allow-reachable", "", "comma-separated reachable govulncheck OSV IDs allowed by policy; use OSV@module to scope an allowance")
	ignoreUnreachableFlag := flag.String("ignore-unreachable", "", "comma-separated module/package-only govulncheck OSV IDs ignored by policy; use OSV@module to scope an allowance")
	replacedModulesFlag := flag.Bool("replaced-modules", false, "read go mod edit -json output and check the replaced modules' advisories instead of govulncheck JSON")
	allowReplacedFlag := flag.String("allow-replaced", "", "comma-separated OSV@module advisories of replaced modules allowed by policy")
	vulnDBFlag := flag.String("vulndb", "https://vuln.go.dev", "vulnerability database read by -replaced-modules")
	flag.Parse()

	if *replacedModulesFlag {
		index, err := fetchVulnIndex(*vulnDBFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to fetch the vulnerability index: %v\n", err)
			os.Exit(2)
		}
		code := runReplacedCheck(os.Stdin, index, os.Stderr, *allowReplacedFlag)
		index.Close()
		os.Exit(code)
	}
	os.Exit(runFilter(os.Stdin, os.Stderr, *allowReachableFlag, *ignoreUnreachableFlag))
}

func runFilter(input io.Reader, stderr io.Writer, allowReachableList, ignoreUnreachableList string) int {
	allowedReachable := parseAllowList(allowReachableList)
	ignoredUnreachable := parseAllowList(ignoreUnreachableList)

	reachableCounts := map[findingKey]int{}
	unreachableCounts := map[findingKey]int{}
	decoder := json.NewDecoder(input)
	for {
		var msg govulncheckMessage
		err := decoder.Decode(&msg)
		if err == io.EOF {
			break
		}
		if err != nil {
			fmt.Fprintf(stderr, "failed to parse govulncheck JSON: %v\n", err)
			return 2
		}
		if msg.Finding != nil && msg.Finding.OSV != "" {
			key := findingKey{
				osv:    msg.Finding.OSV,
				module: findingModule(msg.Finding),
			}
			if findingIsReachable(msg.Finding) {
				reachableCounts[key]++
			} else {
				unreachableCounts[key]++
			}
		}
	}

	if len(reachableCounts) == 0 && len(unreachableCounts) == 0 {
		fmt.Fprintln(stderr, "govulncheck: no reachable vulnerabilities found")
		return 0
	}

	var blocked []string
	for _, key := range sortedFindingKeys(reachableCounts) {
		if allowedReachable[key.osv].allowsModule(key.module) {
			fmt.Fprintf(stderr, "govulncheck: allowed reachable %s in %s (%d trace(s))\n", key.osv, displayModule(key.module), reachableCounts[key])
			continue
		}
		blocked = append(blocked, displayFinding(key))
		fmt.Fprintf(stderr, "govulncheck: blocked reachable %s in %s (%d trace(s))\n", key.osv, displayModule(key.module), reachableCounts[key])
	}

	for _, key := range sortedFindingKeys(unreachableCounts) {
		if reachableCounts[key] > 0 {
			continue
		}
		if ignoredUnreachable[key.osv].allowsModule(key.module) {
			fmt.Fprintf(stderr, "govulncheck: ignored module/package-only %s in %s (%d finding(s))\n", key.osv, displayModule(key.module), unreachableCounts[key])
			continue
		}
		blocked = append(blocked, displayFinding(key))
		fmt.Fprintf(stderr, "govulncheck: blocked module/package-only %s in %s (%d finding(s))\n", key.osv, displayModule(key.module), unreachableCounts[key])
	}

	if len(blocked) > 0 {
		fmt.Fprintf(stderr, "govulncheck: refusing %d unallowed vulnerability finding(s): %s\n", len(blocked), strings.Join(blocked, ", "))
		return 1
	}
	return 0
}

func sortedFindingKeys(counts map[findingKey]int) []findingKey {
	keys := make([]findingKey, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].osv == keys[j].osv {
			return keys[i].module < keys[j].module
		}
		return keys[i].osv < keys[j].osv
	})
	return keys
}

func parseAllowList(raw string) map[string]allowPolicy {
	allowed := map[string]allowPolicy{}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, module, hasModule := strings.Cut(entry, "@")
		id = strings.TrimSpace(id)
		module = strings.TrimSpace(module)
		if id == "" {
			continue
		}
		policy := allowed[id]
		if policy.modules == nil {
			policy.modules = map[string]bool{}
		}
		if !hasModule || module == "" {
			policy.allowAll = true
		} else {
			policy.modules[module] = true
		}
		allowed[id] = policy
	}
	return allowed
}

func (p allowPolicy) allowsModule(module string) bool {
	if p.allowAll {
		return true
	}
	return p.modules[module]
}

func findingModule(finding *govulncheckFinding) string {
	for _, frame := range finding.Trace {
		if frame.Module != "" {
			return frame.Module
		}
	}
	for _, frame := range finding.Trace {
		if frame.Package != "" {
			return frame.Package
		}
	}
	return ""
}

func findingIsReachable(finding *govulncheckFinding) bool {
	for _, frame := range finding.Trace {
		if frame.Function != "" {
			return true
		}
	}
	return false
}

func displayFinding(key findingKey) string {
	if key.module == "" {
		return key.osv
	}
	return key.osv + "@" + key.module
}

func displayModule(module string) string {
	if module == "" {
		return "<unknown module>"
	}
	return module
}
