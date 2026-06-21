package util

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// DiscoverNetworkInfo returns private + public IPs for heartbeats and node registration.
func DiscoverNetworkInfo() (privateIP, publicIP string, err error) {
	privateIP, err = getPrivateIP()
	if err != nil {
		fmt.Printf("Warning: could not determine private IP: %v\n", err)
		privateIP = ""
		err = nil
	}

	publicIP, err = getPublicIP()
	if err != nil {
		return "", "", fmt.Errorf("could not determine public IP: %w", err)
	}

	return privateIP, publicIP, nil
}

// getPrivateIP uses the outbound UDP dial trick to find the primary private address.
func getPrivateIP() (string, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "", err
	}
	defer conn.Close()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("could not determine local address type")
	}

	return localAddr.IP.String(), nil
}

func getPublicIP() (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("https://api.ipify.org")
	if err != nil {
		return "", fmt.Errorf("request to ipify.org failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ipify.org returned non-200 status: %d", resp.StatusCode)
	}

	ipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("could not read response from ipify.org: %w", err)
	}

	ip := string(ipBytes)
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("invalid IP address from ipify.org: %s", ip)
	}

	return ip, nil
}
