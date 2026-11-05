package main

import "testing"

func TestUnauthenticatedListenerRequiresLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080"} {
		if err := loopbackAddress(address); err != nil {
			t.Errorf("rejected local address %q: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "[::]:8080", "192.168.1.1:8080", "example.com:8080", "invalid"} {
		if err := loopbackAddress(address); err == nil {
			t.Errorf("accepted nonlocal address %q", address)
		}
	}
}
