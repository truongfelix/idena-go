// Copyright 2016 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Contains the NTP time drift detection via the SNTP protocol:
//   https://tools.ietf.org/html/rfc4330

package protocol

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/idena-network/idena-go/log"
	"net"
	"slices"
	"time"
)

const (
	ntpPool        = "pool.ntp.org" // ntpPool is the NTP server to query for the current time
	ntpChecks      = 3              // Number of measurements to do against the NTP server
	driftThreshold = 10 * time.Second
	// wrongTimeChecks is how many checks in a row must find the clock off before
	// the node reports a wrong time: one bad server or reply is not enough.
	wrongTimeChecks = 3
	ntpPacketSize   = 48
)

var (
	ntpServer = ntpPool + ":123"
	// ntpPacketGap spaces out the requests of one measurement: servers answer
	// packets that come less than ~2 s apart (ntpd's default "discard minimum")
	// with a Kiss-o'-Death packet or not at all.
	ntpPacketGap   = 2 * time.Second
	ntpReadTimeout = 5 * time.Second
)

// clockCheck turns NTP measurements into the node's "wrong time" flag.
type clockCheck struct {
	offInARow int
	wrong     bool
}

// observe records one measurement and returns the flag: set once the clock is
// off by more than driftThreshold in wrongTimeChecks measurements in a row,
// cleared by a measurement within it. A failed measurement says nothing about
// the clock and changes nothing.
func (c *clockCheck) observe(drift time.Duration, err error) bool {
	switch {
	case err != nil:
		log.Debug("NTP sanity check failed", "err", err)
	case drift < -driftThreshold || drift > driftThreshold:
		log.Warn(fmt.Sprintf("System clock seems off by %v, which can prevent network connectivity", drift))
		log.Warn("Please enable network time synchronisation in system settings.")
		c.offInARow++
		c.wrong = c.offInARow >= wrongTimeChecks
	default:
		log.Debug("NTP sanity check done", "drift", drift)
		c.offInARow = 0
		c.wrong = false
	}
	return c.wrong
}

// SntpDrift does a naive time resolution against an NTP server and returns the
// measured drift. This method uses the simple version of NTP. It's not precise
// but should be fine for these purposes.
//
// Note, it sends two extra requests compared to the number of requested
// measurements, drops the replies that carry no valid time (see checkReply),
// fails with fewer than the requested number left, and returns their median.
func SntpDrift(measurements int) (time.Duration, error) {
	// Resolve the address of the NTP server
	addr, err := net.ResolveUDPAddr("udp", ntpServer)
	if err != nil {
		return 0, err
	}
	// Execute each of the measurements
	drifts := []time.Duration{}
	var lastErr error
	for i := 0; i < measurements+2; i++ {
		if i > 0 {
			time.Sleep(ntpPacketGap)
		}
		drift, err := sntpQuery(addr)
		if err != nil {
			lastErr = err
			continue
		}
		drifts = append(drifts, drift)
	}
	if len(drifts) < measurements {
		return 0, fmt.Errorf("%d of %d NTP replies usable, last error: %w", len(drifts), measurements+2, lastErr)
	}
	slices.Sort(drifts)
	return drifts[len(drifts)/2], nil
}

// sntpQuery sends one request and returns the drift it measures.
func sntpQuery(addr *net.UDPAddr) (time.Duration, error) {
	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	// Construct the time request (empty package with only 3 fields set):
	//   Bits 3-5: Protocol version, 3
	//   Bits 6-8: Mode of operation, client, 3
	//   Transmit timestamp: random, the server returns it as the origin timestamp
	request := make([]byte, ntpPacketSize)
	request[0] = 3<<3 | 3
	if _, err := rand.Read(request[40:48]); err != nil {
		return 0, err
	}
	conn.SetDeadline(time.Now().Add(ntpReadTimeout))
	sent := time.Now()
	if _, err = conn.Write(request); err != nil {
		return 0, err
	}
	// Retrieve the reply and calculate the elapsed time
	reply := make([]byte, ntpPacketSize)
	n, err := conn.Read(reply)
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(sent)
	if err := checkReply(request, reply[:n]); err != nil {
		return 0, err
	}

	// Reconstruct the time from the reply data
	sec := uint64(binary.BigEndian.Uint32(reply[40:44]))
	frac := uint64(binary.BigEndian.Uint32(reply[44:48]))
	nanosec := sec*1e9 + (frac*1e9)>>32
	t := time.Date(1900, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(nanosec))

	// Calculate the drift based on an assumed answer time of RRT/2
	return sent.Sub(t) + elapsed/2, nil
}

// checkReply rejects a reply that carries no valid time: a Kiss-o'-Death packet
// (stratum 0, a server telling the client to slow down; ntpd echoes the
// request's transmit timestamp in it), a server that is not synchronised (leap
// indicator 3, stratum 16), a packet that is not a server reply, or one that
// does not answer this request.
func checkReply(request, reply []byte) error {
	if len(reply) < ntpPacketSize {
		return fmt.Errorf("NTP reply of %d bytes", len(reply))
	}
	if mode := reply[0] & 0x07; mode != 4 {
		return fmt.Errorf("NTP reply in mode %d, not a server reply", mode)
	}
	if stratum := reply[1]; stratum == 0 {
		return fmt.Errorf("NTP Kiss-o'-Death %q", reply[12:16])
	} else if stratum >= 16 {
		return fmt.Errorf("NTP server not synchronised (stratum %d)", stratum)
	}
	if reply[0]>>6 == 3 {
		return errors.New("NTP server not synchronised (leap indicator 3)")
	}
	if !bytes.Equal(reply[24:32], request[40:48]) {
		return errors.New("NTP reply does not answer the request")
	}
	if binary.BigEndian.Uint64(reply[40:48]) == 0 {
		return errors.New("NTP reply without a transmit timestamp")
	}
	return nil
}
