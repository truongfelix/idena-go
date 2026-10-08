package ipfs

import "github.com/ipfs/kubo/core"

// DiagNode gives the reachability probe (cmd/reachprobe, diag branch only) the Kubo node behind a proxy.
func DiagNode(p Proxy) *core.IpfsNode {
	if ip, ok := p.(*ipfsProxy); ok {
		return ip.node
	}
	return nil
}
