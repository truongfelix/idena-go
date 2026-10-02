//go:build windows
// +build windows

package rpc

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Closing the listener while Accept waits must not close any handle twice: Windows hands freed
// handle values out again, so a second close hits whatever handle took the value (in the Go
// runtime, a thread handle: "runtime.preemptM: duplicatehandle failed; errno=6").
func TestIPCListenerCloseDuringAcceptKeepsOtherHandles(t *testing.T) {
	for i := 0; i < 100; i++ {
		endpoint := fmt.Sprintf(`\\.\pipe\idena-rpc-close-during-accept-%d-%d`, os.Getpid(), i)
		listener, err := ipcListen(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		accepted := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if conn != nil {
				conn.Close()
			}
			accepted <- err
		}()
		time.Sleep(time.Millisecond)
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}

		// Take the handle values the listener has just freed.
		var events [4]windows.Handle
		for j := range events {
			if events[j], err = windows.CreateEvent(nil, 0, 0, nil); err != nil {
				t.Fatal(err)
			}
		}
		select {
		case err := <-accepted:
			if err == nil {
				t.Fatalf("iteration %d: Accept succeeded on a closed listener", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Accept did not return after Close", i)
		}
		for j, event := range events {
			if err := windows.SetEvent(event); err != nil {
				t.Fatalf("iteration %d: event %d was closed by the listener: %v", i, j, err)
			}
			windows.CloseHandle(event)
		}
	}
}

// A connection closed twice, as when a test listener kills it and the server then closes its codec,
// must not close the handle value a second time.
func TestIPCConnCloseTwiceKeepsOtherHandles(t *testing.T) {
	endpoint := fmt.Sprintf(`\\.\pipe\idena-rpc-close-twice-%d`, os.Getpid())
	listener, err := ipcListen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	for i := 0; i < 100; i++ {
		accepted := make(chan net.Conn, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				conn = nil
			}
			accepted <- conn
		}()
		client, err := newIPCConnection(context.Background(), endpoint)
		if err != nil {
			t.Fatal(err)
		}
		server := <-accepted
		if server == nil {
			t.Fatalf("iteration %d: Accept failed", i)
		}
		server.Close()

		// Take the handle value the first Close freed, then close again.
		var events [4]windows.Handle
		for j := range events {
			if events[j], err = windows.CreateEvent(nil, 0, 0, nil); err != nil {
				t.Fatal(err)
			}
		}
		server.Close()
		for j, event := range events {
			if err := windows.SetEvent(event); err != nil {
				t.Fatalf("iteration %d: event %d was closed by the second Close: %v", i, j, err)
			}
			windows.CloseHandle(event)
		}
		client.Close()
	}
}
