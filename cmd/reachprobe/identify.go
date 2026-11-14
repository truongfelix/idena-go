package main

// identify mode (B-247): what another node learns about a target. First the DHT's answer for its peer id (what a
// node that never met it finds), then a direct connection to -addr and what the target announces in identify: its
// listen addresses, the addresses of its signed peer record, the address it sees for us, its agent.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/record"
	"github.com/multiformats/go-multiaddr"
)

func identifyTarget(ctx context.Context, h host.Host, find findFunc, target peer.ID, addr string, warmup time.Duration) {
	sub, err := h.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		logf("FATAL", "subscribe: %v", err)
		return
	}
	defer sub.Close()

	logf("WARMUP", "%s", warmup)
	select {
	case <-ctx.Done():
		return
	case <-time.After(warmup):
	}
	logf("WARMUP", "done, peers=%d", len(h.Network().Peers()))

	lookup, cancel := context.WithTimeout(ctx, 90*time.Second)
	info, err := find(lookup, target)
	cancel()
	if err != nil {
		logf("DHT", "%s not found: %v", target, err)
	} else {
		logf("DHT", "%s: %d addresses", target, len(info.Addrs))
		for _, a := range info.Addrs {
			logf("DHT", "  %s", a)
		}
	}

	if addr == "" {
		return
	}
	ma, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		logf("FATAL", "addr %q: %v", addr, err)
		return
	}
	connect, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	err = h.Connect(connect, peer.AddrInfo{ID: target, Addrs: []multiaddr.Multiaddr{ma}})
	cancel2()
	if err != nil {
		logf("CONNECT", "%s via %s: %v", target, ma, err)
		return
	}
	logf("CONNECT", "%s via %s", target, ma)

	deadline := time.After(60 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			logf("IDENTIFY", "no identify result for %s in 60 s", target)
			return
		case e := <-sub.Out():
			ev := e.(event.EvtPeerIdentificationCompleted)
			if ev.Peer != target {
				continue
			}
			logf("IDENTIFY", "agent=%q protocolVersion=%q", ev.AgentVersion, ev.ProtocolVersion)
			logf("IDENTIFY", "observed (our address as the target sees it): %v", ev.ObservedAddr)
			logf("IDENTIFY", "listen addresses: %d", len(ev.ListenAddrs))
			for _, a := range ev.ListenAddrs {
				logf("IDENTIFY", "  %s", a)
			}
			if ev.SignedPeerRecord == nil {
				logf("IDENTIFY", "signed peer record: none")
			} else if _, rec, err := record.ConsumeEnvelope(mustMarshal(ev.SignedPeerRecord), peer.PeerRecordEnvelopeDomain); err != nil {
				logf("IDENTIFY", "signed peer record: unreadable: %v", err)
			} else if pr, ok := rec.(*peer.PeerRecord); ok {
				logf("IDENTIFY", "signed peer record: %d addresses", len(pr.Addrs))
				for _, a := range pr.Addrs {
					logf("IDENTIFY", "  %s", a)
				}
			}
			return
		}
	}
}

func mustMarshal(e *record.Envelope) []byte {
	b, err := e.Marshal()
	if err != nil {
		return nil
	}
	return b
}

// dhtWatch (B-247 experiment): every interval, the DHT's answer for target (the probe's own memory of it wiped first,
// never dialed on purpose), whether the probe is connected to it anyway, and whether a bare TCP connect to tcpAddr
// gets through the router (closed at once).
func dhtWatch(ctx context.Context, h host.Host, find findFunc, target peer.ID, tcpAddr string, interval time.Duration) {
	logf("WARMUP", "60s")
	select {
	case <-ctx.Done():
		return
	case <-time.After(60 * time.Second):
	}
	for {
		local := len(h.Network().ConnsToPeer(target))
		// A connection found on the home network (mDNS) would make the lookup answer from it: drop it first.
		_ = h.Network().ClosePeer(target)
		h.Peerstore().ClearAddrs(target)
		lookup, cancel := context.WithTimeout(ctx, 60*time.Second)
		info, err := find(lookup, target)
		cancel()
		dht := "not found"
		if err == nil {
			var as []string
			for _, a := range info.Addrs {
				as = append(as, a.String())
			}
			dht = fmt.Sprintf("%d: %s", len(as), strings.Join(as, " "))
		}
		tcp := "-"
		if tcpAddr != "" {
			c, err := net.DialTimeout("tcp", tcpAddr, 5*time.Second)
			if err != nil {
				tcp = "closed (" + firstLine(err) + ")"
			} else {
				tcp = "open"
				c.Close()
			}
		}
		logf("WATCH", "dht=%s | connected before=%d after=%d | tcp %s %s | probe peers=%d", dht, local, len(h.Network().ConnsToPeer(target)),
			tcpAddr, tcp, len(h.Network().Peers()))
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}
