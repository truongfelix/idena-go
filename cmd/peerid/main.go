// Command peerid prints a throwaway ed25519 peer id and checks how peer.Decode treats sample strings (diag only).
package main

import (
	"crypto/rand"
	"fmt"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func main() {
	_, pub, _ := crypto.GenerateEd25519Key(rand.Reader)
	id, _ := peer.IDFromPublicKey(pub)
	fmt.Println("ed25519:", id.String(), len(id.String()), "bytes", len([]byte(id)))
	for _, s := range []string{
		"QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xp",
		"QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7x",
		"QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR7xpp",
		"12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
		"QmNqkSwad5HTShxVzFcYLQkRCRjrs9ZhQykqrRTQcdR70p",
	} {
		_, err := peer.Decode(s)
		fmt.Printf("%s -> %v\n", s, err)
	}
}
