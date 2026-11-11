package protocol

import (
	"os"
	"testing"

	"github.com/idena-network/idena-go/config"
)

// The tests here build automine chains with no online identity: while a consensus version's voting window is
// open, 80% of zero voters passes and the chain upgrades at its first block. Close every window so the tests do
// not depend on the date.
func TestMain(m *testing.M) {
	for _, cfg := range config.ConsensusVersions {
		cfg.StartActivationDate = 0
		cfg.EndActivationDate = 0
	}
	os.Exit(m.Run())
}
