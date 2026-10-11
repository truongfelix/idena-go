package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// govulncheck looks a replaced module up in the vulnerability database under its replacement's path, so the
// advisories published for the module it replaces never reach its report. The replaced-modules check reads the
// go.mod replacements (`go mod edit -json`) and blocks every advisory the database lists for a replaced path,
// whatever its affected versions: a replacement's version cannot be compared with the replaced module's ranges.

type goModEdit struct {
	Replace []struct {
		Old struct {
			Path string
		}
		New struct {
			Path    string
			Version string
		}
	}
}

type vulnIndexModule struct {
	Path  string `json:"path"`
	Vulns []struct {
		ID string `json:"id"`
	} `json:"vulns"`
}

const vulnIndexTimeout = time.Minute

func fetchVulnIndex(db string) (io.ReadCloser, error) {
	client := &http.Client{Timeout: vulnIndexTimeout}
	url := strings.TrimSuffix(db, "/") + "/index/modules.json"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

func runReplacedCheck(goMod io.Reader, index io.Reader, stderr io.Writer, allowList string) int {
	var mod goModEdit
	if err := json.NewDecoder(goMod).Decode(&mod); err != nil {
		fmt.Fprintf(stderr, "failed to parse go mod edit -json output: %v\n", err)
		return 2
	}
	var modules []vulnIndexModule
	if err := json.NewDecoder(index).Decode(&modules); err != nil {
		fmt.Fprintf(stderr, "failed to parse the vulnerability index: %v\n", err)
		return 2
	}
	if len(modules) == 0 {
		fmt.Fprintln(stderr, "govulncheck: the vulnerability index lists no modules")
		return 2
	}
	vulnsByPath := map[string][]string{}
	for _, m := range modules {
		for _, v := range m.Vulns {
			vulnsByPath[m.Path] = append(vulnsByPath[m.Path], v.ID)
		}
	}

	allowed := parseAllowList(allowList)
	for id, policy := range allowed {
		if policy.allowAll {
			fmt.Fprintf(stderr, "govulncheck: replaced-module allowance %s must name its module (%s@<module>)\n", id, id)
			return 2
		}
	}
	used := map[findingKey]bool{}
	var blocked []string
	for _, r := range mod.Replace {
		ids := vulnsByPath[r.Old.Path]
		sort.Strings(ids)
		for _, id := range ids {
			key := findingKey{osv: id, module: r.Old.Path}
			if allowed[id].allowsModule(r.Old.Path) {
				used[key] = true
				fmt.Fprintf(stderr, "govulncheck: allowed %s in replaced %s (built from %s)\n", id, r.Old.Path, strings.TrimSpace(r.New.Path+" "+r.New.Version))
				continue
			}
			blocked = append(blocked, displayFinding(key))
			fmt.Fprintf(stderr, "govulncheck: blocked %s in replaced %s (built from %s)\n", id, r.Old.Path, strings.TrimSpace(r.New.Path+" "+r.New.Version))
		}
	}
	fmt.Fprintf(stderr, "govulncheck: checked %d replaced module(s) against %d indexed module(s)\n", len(mod.Replace), len(modules))

	for _, key := range allowedKeys(allowed) {
		if !used[key] {
			blocked = append(blocked, displayFinding(key))
			fmt.Fprintf(stderr, "govulncheck: allowance %s matches no replaced module; remove it\n", displayFinding(key))
		}
	}

	if len(blocked) > 0 {
		fmt.Fprintf(stderr, "govulncheck: refusing %d replaced-module finding(s): %s\n", len(blocked), strings.Join(blocked, ", "))
		return 1
	}
	return 0
}

func allowedKeys(allowed map[string]allowPolicy) []findingKey {
	counts := map[findingKey]int{}
	for id, policy := range allowed {
		for module := range policy.modules {
			counts[findingKey{osv: id, module: module}] = 1
		}
	}
	return sortedFindingKeys(counts)
}
