package utils

import (
	"fmt"
	"sync"
)

// PortManager hands out UDP ports from a configured range for live pipeline wiring.
type PortManager struct {
	mutex      sync.Mutex
	startPort  int
	endPort    int
	portsInUse map[int]bool
}

func NewPortManager(start, end int) *PortManager {
	return &PortManager{
		startPort:  start,
		endPort:    end,
		portsInUse: make(map[int]bool),
	}
}

func (pm *PortManager) Allocate(count int) ([]int, error) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	var allocatedPorts []int
	for port := pm.startPort; port <= pm.endPort && len(allocatedPorts) < count; port++ {
		if !pm.portsInUse[port] {
			allocatedPorts = append(allocatedPorts, port)
		}
	}
	if len(allocatedPorts) < count {
		return nil, fmt.Errorf("not enough free ports in range [%d-%d] to allocate %d", pm.startPort, pm.endPort, count)
	}
	for _, port := range allocatedPorts {
		pm.portsInUse[port] = true
	}
	return allocatedPorts, nil
}

func (pm *PortManager) Release(ports []int) {
	pm.mutex.Lock()
	defer pm.mutex.Unlock()
	for _, port := range ports {
		delete(pm.portsInUse, port)
	}
}
