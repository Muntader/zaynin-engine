package central_api_client

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/muntader/zaynin-engine/gen/centralpb"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/util"

	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Client is how this worker talks back to the central control plane.
type Client struct {
	nodeID          string
	hostname        string
	interval        time.Duration
	grpcClient      centralpb.CentralAPIClient
	conn            *grpc.ClientConn
	stopChan        chan struct{}
	resourceManager *hardware.ResourceManager
	streamManager   *core.Manager
	config          configTypes.Config
}

func New(centralApiAddress, nodeID string, heartbeatInterval time.Duration, resManager *hardware.ResourceManager, streamMgr *core.Manager, appConfig *configTypes.Config) (*Client, error) {
	conn, err := grpc.Dial(centralApiAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	return &Client{
		nodeID:          nodeID,
		hostname:        hostname,
		interval:        heartbeatInterval,
		grpcClient:      centralpb.NewCentralAPIClient(conn),
		conn:            conn,
		stopChan:        make(chan struct{}),
		resourceManager: resManager,
		streamManager:   streamMgr,
		config:          *appConfig,
	}, nil
}

func (c *Client) Close() {
	close(c.stopChan)
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) StartHeartbeat() {
	ticker := time.NewTicker(c.interval)
	go func() {
		defer ticker.Stop()
		c.sendHeartbeat()

		for {
			select {
			case <-ticker.C:
				c.sendHeartbeat()
			case <-c.stopChan:
				return
			}
		}
	}()
}

func (c *Client) sendHeartbeat() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	privateIP, publicIP, err := util.DiscoverNetworkInfo()
	if err != nil {
		slog.Error("Failed to discover network info for heartbeat", "error", err)
		return
	}

	perfStatus, err := c.resourceManager.GetStatus()
	if err != nil {
		slog.Warn("Failed to get hardware status for heartbeat", "error", err)
		perfStatus = &hardware.WorkerStatus{} // empty snapshot beats crashing the ticker
	}
	activeConnections := c.streamManager.GetActiveStreamCount()

	// media ports vary per deploy   metadata struct keeps the proto stable
	metadataMap := map[string]interface{}{
		"rtmp_port": c.config.Server.Media.RTMPPort,
		"srt_port":  c.config.Server.Media.SRTPort,
	}
	protoMetadata, err := structpb.NewStruct(metadataMap)
	if err != nil {
		slog.Error("Failed to create metadata struct for heartbeat", "error", err)
		return
	}

	gpuStatuses := make([]*centralpb.GpuLoadStatus, 0, len(perfStatus.GPUs))
	for _, gpu := range perfStatus.GPUs {
		gpuStatuses = append(gpuStatuses, &centralpb.GpuLoadStatus{
			DeviceId:         int32(gpu.DeviceID),
			ModelName:        gpu.ModelName,
			GpuUsagePercent:  float32(gpu.GPUUsagePercent),
			VramUsagePercent: float32(gpu.VRAMUsagePercent),
		})
	}
	nodeStatus := &centralpb.NodeLoadStatus{
		CpuUsagePercent:   float32(perfStatus.CPUUsagePercent),
		RamUsagePercent:   float32(perfStatus.RAMUsagePercent),
		ActiveConnections: int32(activeConnections),
		Gpus:              gpuStatuses,
	}

	req := &centralpb.NodeHeartbeatRequest{
		NodeId:   c.nodeID,
		Hostname: c.hostname,
		NodeType: centralpb.NodeType_NODE_TYPE_WORKER,
		Region:   c.config.Node.Region,

		PublicIp:  publicIP,
		PrivateIp: privateIP,
		GrpcPort:  int32(c.config.Server.GRPCPort),
		HttpPort:  int32(c.config.Server.HTTPPort),

		Metadata: protoMetadata,
		Status:   nodeStatus,
	}

	_, err = c.grpcClient.Heartbeat(ctx, req)
	if err != nil {
		slog.Warn("Failed to send heartbeat", "node_id", c.nodeID, "error", err)
	} else {

	}
}

func (c *Client) ReportStatus(ctx context.Context, jobID string, phase centralpb.VODJobPhase, message string) {
	req := &centralpb.ReportVODStatusRequest{
		JobId:     jobID,
		Phase:     phase,
		Timestamp: timestamppb.Now(),
		Details:   &centralpb.ReportVODStatusRequest_Message{Message: message},
	}
	c.sendStatus(ctx, req)
}

func (c *Client) ReportFailure(ctx context.Context, jobID string, failedAtPhase centralpb.VODJobPhase, err error) {
	req := &centralpb.ReportVODStatusRequest{
		JobId:     jobID,
		Phase:     centralpb.VODJobPhase_PHASE_FAILED,
		Timestamp: timestamppb.Now(),
		Details:   &centralpb.ReportVODStatusRequest_ErrorMessage{ErrorMessage: err.Error()},
	}
	c.sendStatus(ctx, req)
}

func (c *Client) sendStatus(ctx context.Context, req *centralpb.ReportVODStatusRequest) {
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := c.grpcClient.ReportVODStatus(rpcCtx, req)
	if err != nil {
		slog.Error("Failed to report job status", "job_id", req.JobId, "phase", req.Phase.String(), "error", err)
	}
}
