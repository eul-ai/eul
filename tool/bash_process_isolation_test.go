//go:build linux || darwin

package tool

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBashWithoutSandboxCanReachHostListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listeners are unavailable: %v", err)
	}
	defer listener.Close()

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	result := executeJSON(t, NewBashWithoutSandbox(t.TempDir()), map[string]any{
		"command": `printf connected > /dev/tcp/127.0.0.1/` + port,
		"network": false,
	})
	if result.IsError {
		t.Fatalf("result = %+v", result)
	}

	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.Accept()
	if err != nil {
		t.Fatalf("command did not reach listener: %v", err)
	}
	connection.Close()
}

func TestBashWithoutNetworkCannotReachHostListener(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local listeners are unavailable: %v", err)
	}
	defer listener.Close()

	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	command := `if bash -c 'printf connected > /dev/tcp/127.0.0.1/` + port + `'; then printf connected; else printf blocked; fi`
	result := executeJSON(t, NewBash(t.TempDir()), map[string]any{"command": command, "network": false})
	if result.IsError {
		if strings.Contains(result.Output, "failed to start network-isolated shell") {
			t.Fatalf("network isolation is unavailable: %s", result.Output)
		}
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Output, "blocked") || strings.Contains(result.Output, "connected") {
		t.Fatalf("network-isolated output = %q", result.Output)
	}

	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	connection, err := listener.Accept()
	if err == nil {
		connection.Close()
		t.Fatal("network-isolated command reached host listener")
	}
	if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("accept error = %v", err)
	}

	approved := executeJSON(t, newTestBash(t.TempDir()), map[string]any{
		"command": `printf connected > /dev/tcp/127.0.0.1/` + port,
		"network": true,
	})
	if approved.IsError {
		t.Fatalf("approved result = %+v", approved)
	}
	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	connection, err = listener.Accept()
	if err != nil {
		t.Fatalf("approved command did not reach listener: %v", err)
	}
	connection.Close()
}
