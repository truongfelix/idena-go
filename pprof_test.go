package main

import (
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPprofServerServesHeapProfileOnLoopbackOnly(t *testing.T) {
	addr, err := startPprofServer(0)
	require.NoError(t, err)

	host, _, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)

	resp, err := http.Get("http://" + addr + "/debug/pprof/heap?debug=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(body), "heap profile:"), string(body[:min(len(body), 80)]))
}

func TestPprofServerServesNothingElse(t *testing.T) {
	addr, err := startPprofServer(0)
	require.NoError(t, err)

	resp, err := http.Get("http://" + addr + "/")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestPprofServerReportsAPortInUse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	_, err = startPprofServer(listener.Addr().(*net.TCPAddr).Port)

	require.ErrorContains(t, err, "cannot serve the Go runtime profiles")
}
