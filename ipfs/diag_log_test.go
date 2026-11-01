package ipfs

import (
	"os"
	"testing"

	"github.com/idena-network/idena-go/log"
)

// Diagnostic only: shows the node's log in the ipfs tests.
func TestMain(m *testing.M) {
	log.Root().SetHandler(log.StreamHandler(os.Stderr, log.TerminalFormat(false)))
	os.Exit(m.Run())
}
