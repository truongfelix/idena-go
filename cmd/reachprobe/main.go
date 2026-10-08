// Command reachprobe measures whether a node behind NAT (no open port) can be reached on the Idena IPFS network.
// It starts the node's own IPFS stack (ipfs.NewIpfsProxy: swarm key, "server" profile, relay client, hole
// punching) with a throwaway key, then either listens (reachability, relay addresses, incoming connections) or
// dials a peer id (DHT lookup, connect, relayed or direct, then a stream that needs a direct connection).
//
// Diag branch only; never merged.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/idena-network/idena-go/common/eventbus"
	"github.com/idena-network/idena-go/config"
	"github.com/idena-network/idena-go/ipfs"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	pingproto "github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

const probeProtocol = "/reachprobe/1.0.0"

var start = time.Now()

func logf(tag, format string, args ...interface{}) {
	fmt.Printf("%s +%5.0fs %-7s %s\n", time.Now().Format("15:04:05"), time.Since(start).Seconds(), tag,
		fmt.Sprintf(format, args...))
}

func main() {
	datadir := flag.String("datadir", "", "throwaway IPFS datadir (a new key on first use)")
	port := flag.Int("port", 40499, "IPFS TCP port")
	mode := flag.String("mode", "listen", "listen or dial")
	target := flag.String("target", "", "dial: peer ids to reach, comma-separated")
	proto := flag.String("proto", "probe", "dial: probe (the target runs reachprobe) or ping (libp2p ping, any node)")
	addr := flag.String("addr", "", "dial: optional full multiaddr of the target (relay circuit or direct)")
	duration := flag.Duration("duration", 20*time.Minute, "how long to run")
	warmup := flag.Duration("warmup", 60*time.Second, "dial: time to fill the DHT before the lookup")
	flag.Parse()
	if *datadir == "" {
		fmt.Fprintln(os.Stderr, "-datadir is required")
		os.Exit(2)
	}

	cfg := config.GetDefaultIpfsConfig()
	cfg.DataDir = *datadir
	cfg.IpfsPort = *port
	cfg.BootNodes = config.DefaultIpfsBootstrapNodes
	cfg.SwarmKey = config.DefaultSwarmKey
	cfg.Routing = config.DefaultIpfsRouting
	cfg.LowWater = 30
	cfg.HighWater = 50
	cfg.GracePeriod = "40s"
	cfg.ReproviderInterval = "12h"

	proxy, err := ipfs.NewIpfsProxy(cfg, eventbus.New())
	if err != nil {
		logf("FATAL", "start: %v", err)
		os.Exit(1)
	}
	node := ipfs.DiagNode(proxy)
	h := proxy.Host()
	logf("START", "mode=%s peer=%s port=%d", *mode, h.ID(), *port)

	h.SetStreamHandler(probeProtocol, func(s network.Stream) {
		c := s.Conn()
		logf("STREAM<", "from %s via %s limited=%v", short(c.RemotePeer()), c.RemoteMultiaddr(), c.Stat().Limited)
		line, _ := bufio.NewReader(s).ReadString('\n')
		fmt.Fprintf(s, "pong %s limited=%v %s", h.ID(), c.Stat().Limited, line)
		s.Close()
	})

	watchEvents(h, *datadir)
	targets := map[peer.ID]bool{}
	var targetIDs []peer.ID
	if *mode == "dial" {
		for _, t := range strings.Split(*target, ",") {
			id, err := peer.Decode(strings.TrimSpace(t))
			if err != nil {
				logf("FATAL", "target %q: %v", t, err)
				os.Exit(2)
			}
			targets[id] = true
			targetIDs = append(targetIDs, id)
		}
	}
	watchConns(h, *mode, targets)

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	switch *mode {
	case "listen":
		listen(ctx, h)
	case "dial":
		dial(ctx, h, node.Routing.FindPeer, targetIDs, *addr, *proto, *warmup)
	default:
		logf("FATAL", "unknown mode %q", *mode)
		os.Exit(2)
	}
	logf("END", "peers=%d", len(h.Network().Peers()))
}

func short(p peer.ID) string {
	s := p.String()
	if len(s) > 12 {
		return s[len(s)-8:]
	}
	return s
}

// watchEvents logs reachability and own-address changes, and keeps <datadir>/addrs.txt = the shareable addresses.
func watchEvents(h host.Host, datadir string) {
	sub, err := h.EventBus().Subscribe([]interface{}{
		new(event.EvtLocalReachabilityChanged),
		new(event.EvtLocalAddressesUpdated),
	})
	if err != nil {
		logf("WARN", "event subscribe: %v", err)
		return
	}
	go func() {
		for e := range sub.Out() {
			switch ev := e.(type) {
			case event.EvtLocalReachabilityChanged:
				logf("REACH", "%s", ev.Reachability)
			case event.EvtLocalAddressesUpdated:
				addrs := h.Addrs()
				var list []string
				relays := map[string]bool{}
				for _, a := range addrs {
					list = append(list, a.String())
					if relay, ok := relayOf(a); ok {
						relays[relay] = true
					}
				}
				sort.Strings(list)
				logf("ADDRS", "%d addrs, %d relays: %s", len(list), len(relays), strings.Join(list, " "))
				var full []string
				for _, a := range list {
					full = append(full, a+"/p2p/"+h.ID().String())
				}
				_ = os.WriteFile(filepath.Join(datadir, "addrs.txt"), []byte(strings.Join(full, "\n")+"\n"), 0o644)
			}
		}
	}()
}

func relayOf(a multiaddr.Multiaddr) (string, bool) {
	if _, err := a.ValueForProtocol(multiaddr.P_CIRCUIT); err != nil {
		return "", false
	}
	v, err := a.ValueForProtocol(multiaddr.P_P2P)
	return v, err == nil
}

// watchConns logs connections: in listen mode the incoming ones and every relayed one, in dial mode the target's.
func watchConns(h host.Host, mode string, targets map[peer.ID]bool) {
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			st := c.Stat()
			_, circuit := relayOf(c.RemoteMultiaddr())
			if (mode == "dial" && targets[c.RemotePeer()]) ||
				(mode == "listen" && (st.Direction == network.DirInbound || circuit || st.Limited)) {
				logf("CONN+", "%s %s %s limited=%v", c.RemotePeer(), st.Direction, c.RemoteMultiaddr(), st.Limited)
				go logAgent(h, c.RemotePeer())
			}
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			st := c.Stat()
			_, circuit := relayOf(c.RemoteMultiaddr())
			if (mode == "dial" && targets[c.RemotePeer()]) || (mode == "listen" && (circuit || st.Limited)) {
				logf("CONN-", "%s %s %s limited=%v lasted=%s", short(c.RemotePeer()), st.Direction,
					c.RemoteMultiaddr(), st.Limited, time.Since(st.Opened).Round(time.Second))
			}
		},
	})
}

var agentsLogged sync.Map

// logAgent logs a peer's software (identify's agent version) and whether it advertises a relay address, once.
func logAgent(h host.Host, p peer.ID) {
	if _, done := agentsLogged.LoadOrStore(p, true); done {
		return
	}
	time.Sleep(5 * time.Second)
	agent, _ := h.Peerstore().Get(p, "AgentVersion")
	circuits := 0
	var public []string
	for _, a := range h.Peerstore().Addrs(p) {
		if _, ok := relayOf(a); ok {
			circuits++
		} else if manet.IsPublicAddr(a) {
			public = append(public, a.String())
		}
	}
	logf("AGENT", "%s %v circuitAddrs=%d public=%s", p, agent, circuits, strings.Join(public, " "))
}

func listen(ctx context.Context, h host.Host) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			in, out, limited := 0, 0, 0
			for _, c := range h.Network().Conns() {
				if c.Stat().Direction == network.DirInbound {
					in++
				} else {
					out++
				}
				if c.Stat().Limited {
					limited++
				}
			}
			relays := 0
			for _, a := range h.Addrs() {
				if _, ok := relayOf(a); ok {
					relays++
				}
			}
			logf("STATS", "peers=%d conns in=%d out=%d limited=%d circuitAddrs=%d", len(h.Network().Peers()), in,
				out, limited, relays)
		}
	}
}

type findFunc func(context.Context, peer.ID) (peer.AddrInfo, error)

func dial(ctx context.Context, h host.Host, find findFunc, targets []peer.ID, addr, proto string, warmup time.Duration) {
	logf("WARMUP", "%s", warmup)
	select {
	case <-ctx.Done():
		return
	case <-time.After(warmup):
	}
	logf("WARMUP", "done, peers=%d", len(h.Network().Peers()))

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t peer.ID) {
			defer wg.Done()
			dialOne(ctx, h, find, t, addr, proto)
		}(t)
	}
	wg.Wait()
	logf("RESULT", "first pass done")

	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			for _, t := range targets {
				printConns(h, t)
				if len(h.Network().ConnsToPeer(t)) > 0 {
					ping(ctx, h, t, proto, true)
				}
			}
		}
	}
}

func dialOne(ctx context.Context, h host.Host, find findFunc, target peer.ID, addr, proto string) {
	tag := short(target)
	if conns := h.Network().ConnsToPeer(target); len(conns) > 0 {
		logf("BEFORE", "%s already connected (%d conns): closing them", tag, len(conns))
		printConns(h, target)
		_ = h.Network().ClosePeer(target)
	}
	// Only the DHT may answer the lookup: forget what identify or a past connection taught this node.
	h.Peerstore().ClearAddrs(target)
	h.Peerstore().RemovePeer(target)
	var addrs []multiaddr.Multiaddr
	t0 := time.Now()
	fctx, fcancel := context.WithTimeout(ctx, 60*time.Second)
	info, err := find(fctx, target)
	fcancel()
	if err != nil {
		logf("FIND", "%s failed after %s: %v", tag, time.Since(t0).Round(time.Millisecond), err)
	} else {
		var list []string
		circuits := 0
		for _, a := range info.Addrs {
			list = append(list, a.String())
			if _, ok := relayOf(a); ok {
				circuits++
			}
		}
		logf("FIND", "%s ok after %s: %d addrs (%d relayed): %s", tag, time.Since(t0).Round(time.Millisecond),
			len(list), circuits, strings.Join(list, " "))
		addrs = append(addrs, info.Addrs...)
	}
	if addr != "" {
		ma, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			logf("FATAL", "addr: %v", err)
			return
		}
		if transport, id := peer.SplitAddr(ma); id == target {
			addrs = append(addrs, transport)
		}
	}

	t0 = time.Now()
	cctx, ccancel := context.WithTimeout(ctx, 60*time.Second)
	err = h.Connect(cctx, peer.AddrInfo{ID: target, Addrs: addrs})
	ccancel()
	if err != nil {
		logf("CONNECT", "%s failed after %s: %v", tag, time.Since(t0).Round(time.Millisecond), firstLine(err))
		return
	}
	logf("CONNECT", "%s ok after %s", tag, time.Since(t0).Round(time.Millisecond))
	printConns(h, target)
	// A stream with no limited-connection allowance makes the swarm wait for a direct connection (hole punch
	// or connection reversal) when the only one is relayed: this is what Idena's own protocol streams do.
	if !ping(ctx, h, target, proto, false) {
		ping(ctx, h, target, proto, true)
	}
	printConns(h, target)
}

func firstLine(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " ..."
	}
	return s
}

// ping opens a stream on the probe's protocol (both ends run reachprobe) or libp2p's ping (any Kubo node) and
// checks the echo. Without allowLimited, a relayed connection only serves once it is upgraded to a direct one.
func ping(ctx context.Context, h host.Host, target peer.ID, proto string, allowLimited bool) bool {
	tag := short(target)
	sctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if allowLimited {
		sctx = network.WithAllowLimitedConn(sctx, "reachprobe")
	}
	protoID := protocol.ID(probeProtocol)
	if proto == "ping" {
		protoID = pingproto.ID
	}
	t0 := time.Now()
	s, err := h.NewStream(sctx, target, protoID)
	if err != nil {
		logf("STREAM>", "%s allowLimited=%v failed after %s: %v", tag, allowLimited,
			time.Since(t0).Round(time.Millisecond), firstLine(err))
		return false
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(30 * time.Second))
	via := fmt.Sprintf("via %s limited=%v", s.Conn().RemoteMultiaddr(), s.Conn().Stat().Limited)
	var reply string
	if proto == "ping" {
		buf := make([]byte, pingproto.PingSize)
		_, _ = rand.Read(buf)
		back := make([]byte, len(buf))
		if _, err = s.Write(buf); err == nil {
			_, err = io.ReadFull(s, back)
		}
		if err == nil && !bytes.Equal(buf, back) {
			err = errors.New("echo differs")
		}
		reply = "echo ok"
	} else {
		fmt.Fprintf(s, "ping %s\n", time.Now().Format(time.RFC3339))
		reply, err = bufio.NewReader(s).ReadString('\n')
	}
	if err != nil {
		logf("STREAM>", "%s allowLimited=%v opened after %s %s, read failed: %v", tag, allowLimited,
			time.Since(t0).Round(time.Millisecond), via, err)
		return false
	}
	logf("STREAM>", "%s allowLimited=%v ok after %s %s: %s", tag, allowLimited,
		time.Since(t0).Round(time.Millisecond), via, strings.TrimSpace(reply))
	return true
}

func printConns(h host.Host, target peer.ID) {
	conns := h.Network().ConnsToPeer(target)
	if len(conns) == 0 {
		logf("CONNS", "%s none", short(target))
		return
	}
	for _, c := range conns {
		logf("CONNS", "%s %s %s limited=%v age=%s", short(target), c.Stat().Direction, c.RemoteMultiaddr(), c.Stat().Limited,
			time.Since(c.Stat().Opened).Round(time.Second))
	}
}
