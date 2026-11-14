package config

// splitPublicAddrs reads --ipfspublicaddr: comma-separated addresses, blanks dropped. The IPFS proxy checks each one.
func splitPublicAddrs(value string) []string {
	return splitDirectPeers(value)
}
