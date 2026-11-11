package config

import (
	"reflect"
	"sort"
	"testing"
)

// The rules v13 changes are read through the rules of each block's height (AppState.Consensus, set by the fork check
// for a fork's blocks), never through the running config alone: a new v13 rule, or a value v13 changes, must be read
// the same way, and then added here.
func TestV13ChangesOnlyTheRulesReadPerHeight(t *testing.T) {
	v12, v13 := reflect.ValueOf(*ConsensusVersions[ConsensusV12]), reflect.ValueOf(*ConsensusVersions[ConsensusV13])
	var changed []string
	for i := 0; i < v12.NumField(); i++ {
		if !reflect.DeepEqual(v12.Field(i).Interface(), v13.Field(i).Interface()) {
			changed = append(changed, v12.Type().Field(i).Name)
		}
	}
	sort.Strings(changed)
	want := []string{"EnableUpgrade13", "EndActivationDate", "FrozenAddresses", "StartActivationDate", "Version"}
	if !reflect.DeepEqual(changed, want) {
		t.Fatalf("v13 changes %v, want %v: read a new rule through the rules of the block's height", changed, want)
	}
}

// UpgradedRules applies the versions above base's up to the one asked, on a copy.
func TestUpgradedRules(t *testing.T) {
	base := *ConsensusVersions[ConsensusV12]
	rules := UpgradedRules(&base, ConsensusV13)
	if rules.Version != ConsensusV13 || !rules.EnableUpgrade13 || base.EnableUpgrade13 {
		t.Fatalf("UpgradedRules: version %d, v13 %v, base changed %v", rules.Version, rules.EnableUpgrade13, base.EnableUpgrade13)
	}
	same := UpgradedRules(rules, ConsensusV12)
	if same == rules || same.Version != ConsensusV13 {
		t.Fatalf("UpgradedRules below the base's version: %+v", same)
	}
}
