package protocol

import (
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type ntpReply int

const (
	ntpOK        ntpReply = iota
	ntpKoD                // ntpd's "slow down" reply to a client sending too fast
	ntpDrop               // no reply
	ntpBadOrigin          // a reply to another request, an hour old
	ntpUnsynced           // stratum 16, an hour off
)

// startFakeNtp points SntpDrift at a local server whose clock runs offset ahead
// of this one; it answers the n-th request as script[n] says (ntpOK past its end).
func startFakeNtp(t *testing.T, offset time.Duration, script ...ntpReply) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	oldServer, oldGap, oldTimeout := ntpServer, ntpPacketGap, ntpReadTimeout
	ntpServer, ntpPacketGap, ntpReadTimeout = conn.LocalAddr().String(), 0, 200*time.Millisecond
	t.Cleanup(func() {
		conn.Close()
		ntpServer, ntpPacketGap, ntpReadTimeout = oldServer, oldGap, oldTimeout
	})
	go func() {
		buf := make([]byte, 512)
		for n := 0; ; n++ {
			size, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			kind := ntpOK
			if n < len(script) {
				kind = script[n]
			}
			if kind != ntpDrop {
				conn.WriteToUDP(fakeNtpReply(kind, buf[:size], time.Now().Add(offset)), from)
			}
		}
	}()
}

func ntpTimestamp(t time.Time) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b, uint32(t.Unix()+2208988800))
	binary.BigEndian.PutUint32(b[4:], uint32(uint64(t.Nanosecond())<<32/1e9))
	return b
}

func fakeNtpReply(kind ntpReply, request []byte, now time.Time) []byte {
	reply := make([]byte, ntpPacketSize)
	reply[0] = 3<<3 | 4 // no leap warning, version 3, server
	reply[1] = 2
	copy(reply[24:32], request[40:48])
	copy(reply[32:40], ntpTimestamp(now))
	copy(reply[40:48], ntpTimestamp(now))
	switch kind {
	case ntpKoD:
		// As ntpd builds it: leap indicator 3, stratum 0, code "RATE", and the
		// request's transmit timestamp as all three timestamps (zero in the
		// requests of the old SntpDrift, hence a time of 1900-01-01).
		reply[0] = 3<<6 | 3<<3 | 4
		reply[1] = 0
		copy(reply[12:16], "RATE")
		copy(reply[32:40], request[40:48])
		copy(reply[40:48], request[40:48])
	case ntpBadOrigin:
		reply[24] ^= 0xff
		copy(reply[40:48], ntpTimestamp(now.Add(-time.Hour)))
	case ntpUnsynced:
		reply[1] = 16
		copy(reply[40:48], ntpTimestamp(now.Add(time.Hour)))
	}
	return reply
}

func TestSntpDrift(t *testing.T) {
	startFakeNtp(t, 30*time.Second)
	drift, err := SntpDrift(ntpChecks)
	require.NoError(t, err)
	require.InDelta(t, -30*time.Second, drift, float64(time.Second))
}

// The false "clock is wrong" of 2026-10-04: two of the five replies were
// Kiss-o'-Death packets, read as times of 1900-01-01, and one of them stayed in
// the average: a drift of (now - 1900) / 3, about 42 years.
func TestSntpDriftSkipsKissOfDeath(t *testing.T) {
	startFakeNtp(t, 0, ntpOK, ntpKoD, ntpOK, ntpKoD, ntpOK)
	drift, err := SntpDrift(ntpChecks)
	require.NoError(t, err)
	require.InDelta(t, 0, drift, float64(time.Second))
}

// With bad replies in the majority the median would be one of them: no
// measurement then, rather than a wrong one.
func TestSntpDriftNeedsEnoughValidReplies(t *testing.T) {
	for name, script := range map[string][]ntpReply{
		"kiss-o'-death": {ntpKoD, ntpOK, ntpKoD, ntpOK, ntpKoD},
		"bad replies":   {ntpBadOrigin, ntpOK, ntpUnsynced, ntpOK, ntpKoD},
		"no replies":    {ntpKoD, ntpDrop, ntpDrop, ntpOK, ntpOK},
	} {
		t.Run(name, func(t *testing.T) {
			startFakeNtp(t, 0, script...)
			_, err := SntpDrift(ntpChecks)
			require.ErrorContains(t, err, "2 of 5 NTP replies usable")
		})
	}
}

func TestCheckReply(t *testing.T) {
	request := make([]byte, ntpPacketSize)
	request[0] = 3<<3 | 3
	copy(request[40:48], "nonce123")
	require.NoError(t, checkReply(request, fakeNtpReply(ntpOK, request, time.Now())))

	for name, tc := range map[string]struct {
		reply func(r []byte) []byte
		err   string
	}{
		"short":          {func(r []byte) []byte { return r[:47] }, "NTP reply of 47 bytes"},
		"client mode":    {func(r []byte) []byte { r[0] = 3<<3 | 3; return r }, "mode 3"},
		"kiss-o'-death":  {func([]byte) []byte { return fakeNtpReply(ntpKoD, request, time.Now()) }, `Kiss-o'-Death "RATE"`},
		"stratum 16":     {func(r []byte) []byte { r[1] = 16; return r }, "stratum 16"},
		"leap 3":         {func(r []byte) []byte { r[0] |= 3 << 6; return r }, "leap indicator 3"},
		"other request":  {func(r []byte) []byte { r[24] ^= 0xff; return r }, "does not answer the request"},
		"zero timestamp": {func(r []byte) []byte { clear(r[40:48]); return r }, "without a transmit timestamp"},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorContains(t, checkReply(request, tc.reply(fakeNtpReply(ntpOK, request, time.Now()))), tc.err)
		})
	}
}

func TestClockCheck(t *testing.T) {
	var c clockCheck
	off, failed := time.Hour, errors.New("i/o timeout")
	require.False(t, c.observe(off, nil))
	require.False(t, c.observe(-off, nil))
	require.False(t, c.observe(0, failed), "a failed check changes nothing")
	require.True(t, c.observe(off, nil), "third check in a row off")
	require.True(t, c.observe(0, failed), "a failed check changes nothing")
	require.False(t, c.observe(time.Second, nil))
	require.False(t, c.observe(off, nil), "the count starts over")
}
