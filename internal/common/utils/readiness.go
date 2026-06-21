package utils

import (
	"fmt"
	"net"
	"time"
)

// WaitForPorts blocks until each UDP port accepts a probe   used before ffmpeg connects to packager.
func WaitForPorts(ports []int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for _, port := range ports {
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("udp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				goto nextPort
			}
			time.Sleep(50 * time.Millisecond)
		}

		return fmt.Errorf("timed out waiting for port %d to open", port)

	nextPort:
	}

	return nil
}
