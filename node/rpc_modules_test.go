package node

import (
	"testing"

	"github.com/idena-network/idena-go/rpc"
	"github.com/stretchr/testify/require"
)

// The HTTP RPC serves only the namespaces of rpc.Config.HTTPModules: a namespace the node registers but the default
// list leaves out answers "method not found" (social_encrypt, 2026-10-15).
func TestDefaultHTTPModulesServeEveryNamespace(t *testing.T) {
	modules := map[string]bool{}
	for _, m := range rpc.GetDefaultRPCConfig("localhost", 9009).HTTPModules {
		modules[m] = true
	}
	apis := (&Node{}).apis()
	require.NotEmpty(t, apis)
	for _, a := range apis {
		require.True(t, modules[a.Namespace], "namespace %q is not in the default HTTP modules", a.Namespace)
	}
}
