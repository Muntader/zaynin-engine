package hardware

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/mem"
)

var (
	ErrSystemOverloaded = fmt.Errorf("system is overloaded")
)

type Config struct {
	// fraction of CPU/GPU/VRAM use before we stop accepting new work (0.9 = 90%)
	LoadThreshold float64
}

type GPUProfile struct {
	DeviceID  int
	ModelName string
}

// ResourceManager polls real hardware load   we dont guess capacity anymore.
type ResourceManager struct {
	config Config
	gpus   []GPUProfile
	mu     sync.Mutex
}

func NewResourceManager(config Config) (*ResourceManager, error) {
	if config.LoadThreshold <= 0 || config.LoadThreshold > 1.0 {
		config.LoadThreshold = 0.90
	}

	rm := &ResourceManager{
		config: config,
	}

	logicalCores, _ := cpu.Counts(true)
	slog.Info("Hardware Manager: CPU detected.", "logical_cores", logicalCores)

	if err := rm.profileGPUs(); err != nil {
		slog.Warn("Hardware Manager: Could not profile GPUs. GPU monitoring will be disabled.", "error", err)
	}

	slog.Info("Hardware Manager: Monitoring ready.", "load_threshold_percent", rm.config.LoadThreshold*100)
	return rm, nil
}

func (rm *ResourceManager) profileGPUs() error {
	if ret := nvml.Init(); ret != nvml.SUCCESS {
		return fmt.Errorf("NVML initialization failed with code %d", ret)
	}
	// NVML stays up for polling   Shutdown() runs on process exit

	deviceCount, ret := nvml.DeviceGetCount()
	if ret != nvml.SUCCESS {
		nvml.Shutdown()
		return fmt.Errorf("failed to get GPU device count: %d", ret)
	}
	if deviceCount == 0 {
		slog.Info("Hardware Manager: No NVIDIA GPUs detected.")
		return nil
	}

	slog.Info("Hardware Manager: Found NVIDIA GPU(s).", "count", deviceCount)
	rm.gpus = make([]GPUProfile, 0, deviceCount)

	for i := 0; i < int(deviceCount); i++ {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			slog.Warn("Could not get handle for GPU", "index", i)
			continue
		}
		name, _ := device.GetName()
		rm.gpus = append(rm.gpus, GPUProfile{DeviceID: i, ModelName: name})
		slog.Info("  -> Monitoring GPU", "id", i, "model", name)
	}
	return nil
}

func (rm *ResourceManager) Shutdown() {
	if len(rm.gpus) > 0 {
		nvml.Shutdown()
		slog.Info("Hardware Manager: NVML shut down.")
	}
}

type WorkerStatus struct {
	CPUUsagePercent float64
	RAMUsagePercent float64
	GPUs            []GPUStatus
}

type GPUStatus struct {
	DeviceID         int
	ModelName        string
	GPUUsagePercent  float64
	VRAMUsagePercent float64
}

func (rm *ResourceManager) GetStatus() (*WorkerStatus, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	status := &WorkerStatus{
		GPUs: make([]GPUStatus, len(rm.gpus)),
	}

	cpuPerc, err := cpu.Percent(200*time.Millisecond, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU usage: %w", err)
	}
	status.CPUUsagePercent = cpuPerc[0]

	ram, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("failed to get RAM usage: %w", err)
	}
	status.RAMUsagePercent = ram.UsedPercent

	for i, gpuProfile := range rm.gpus {
		device, ret := nvml.DeviceGetHandleByIndex(i)
		if ret != nvml.SUCCESS {
			slog.Warn("Failed to get handle for GPU during status poll", "id", i)
			continue
		}

		util, ret := device.GetUtilizationRates()
		if ret != nvml.SUCCESS {
			slog.Warn("Failed to get utilization for GPU", "id", i)
			continue
		}

		memInfo, ret := device.GetMemoryInfo()
		if ret != nvml.SUCCESS {
			slog.Warn("Failed to get VRAM info for GPU", "id", i)
			continue
		}

		status.GPUs[i] = GPUStatus{
			DeviceID:         gpuProfile.DeviceID,
			ModelName:        gpuProfile.ModelName,
			GPUUsagePercent:  float64(util.Gpu),
			VRAMUsagePercent: float64(memInfo.Used) * 100.0 / float64(memInfo.Total),
		}
	}

	return status, nil
}

// CheckCapacity returns ErrSystemOverloaded when we're too hot to take another job.
func (rm *ResourceManager) CheckCapacity() (*WorkerStatus, error) {
	status, err := rm.GetStatus()
	if err != nil {
		return nil, fmt.Errorf("cannot check capacity due to monitoring error: %w", err)
	}

	if status.CPUUsagePercent >= rm.config.LoadThreshold*100 {
		return status, fmt.Errorf("%w: CPU load is %.1f%%, exceeding threshold of %.1f%%",
			ErrSystemOverloaded, status.CPUUsagePercent, rm.config.LoadThreshold*100)
	}

	if len(status.GPUs) > 0 {
		allGpusOverloaded := true
		for _, gpu := range status.GPUs {
			if gpu.GPUUsagePercent < rm.config.LoadThreshold*100 && gpu.VRAMUsagePercent < rm.config.LoadThreshold*100 {
				allGpusOverloaded = false
				break
			}
		}
		if allGpusOverloaded {
			return status, fmt.Errorf("%w: all available GPUs are exceeding the load threshold of %.1f%%",
				ErrSystemOverloaded, rm.config.LoadThreshold*100)
		}
	}

	return status, nil
}

// FindLeastLoadedGPU picks the GPU with the lowest max(util, vram)   returns -1 if none.
func (status *WorkerStatus) FindLeastLoadedGPU() int {
	if len(status.GPUs) == 0 {
		return -1
	}

	bestGPUID := -1
	lowestScore := 101.0

	for _, gpu := range status.GPUs {
		score := gpu.GPUUsagePercent
		if gpu.VRAMUsagePercent > score {
			score = gpu.VRAMUsagePercent
		}

		if score < lowestScore {
			lowestScore = score
			bestGPUID = gpu.DeviceID
		}
	}

	return bestGPUID
}
