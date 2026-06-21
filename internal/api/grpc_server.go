package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/muntader/zaynin-engine/gen/workerpb"
	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/vod/queue"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/reflection"
	"log/slog"
	"net"
	"strconv"

	"github.com/go-playground/validator/v10"
	"github.com/hibiken/asynq"
	"github.com/muntader/zaynin-engine/internal/common/logging"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/core/service"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
	vodStore "github.com/muntader/zaynin-engine/internal/vod/store"
	videoTypes "github.com/muntader/zaynin-engine/internal/vod/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/structpb"
)

type grpcServer struct {
	workerpb.UnimplementedWorkerServiceServer

	streamSvc       *service.StreamService
	egressSvc       *service.EgressService
	queueClient     *asynq.Client
	credStore       *vodStore.BoltCredentialsStore
	logStore        *logging.LogStore
	resourceManager *hardware.ResourceManager
	inspector       *asynq.Inspector
	validate        *validator.Validate
}

func NewGRPCServer(
	cfg configTypes.Config,
	coreManager *core.Manager,
	redisClient *redis.Client,
	queueClient *asynq.Client,
	credStore *vodStore.BoltCredentialsStore,
	resourceManager *hardware.ResourceManager,
) (*grpc.Server, net.Listener, error) {
	inspector := asynq.NewInspector(asynq.RedisClientOpt{Addr: cfg.Redis.Address})
	egressStore := store.NewEgressStore(redisClient)
	streamStore := store.NewStreamStore(redisClient, coreManager.NodeID())
	streamSvc := service.NewStreamService(coreManager, streamStore)
	egressSvc := service.NewEgressService(coreManager, egressStore, streamStore)
	logStore := logging.NewLogStore(redisClient)

	s := &grpcServer{
		streamSvc:       streamSvc,
		egressSvc:       egressSvc,
		queueClient:     queueClient,
		credStore:       credStore,
		logStore:        logStore,
		resourceManager: resourceManager,
		inspector:       inspector,
		validate:        validator.New(),
	}

	grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(apiKeyUnaryInterceptor(cfg.Api.APIKey)))
	workerpb.RegisterWorkerServiceServer(grpcSrv, s)
	if cfg.Server.GRPCReflectionEnabled {
		reflection.Register(grpcSrv)
	}

	addr := fmt.Sprintf(":%d", cfg.Server.GRPCPort)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	slog.Info("gRPC server configured to listen", "address", addr)

	return grpcSrv, lis, nil
}

func (s *grpcServer) PrepareStream(ctx context.Context, req *workerpb.PrepareStreamRequest) (*workerpb.PrepareStreamResponse, error) {
	configJSON := req.GetConfigJson()
	if configJSON == "" {
		return nil, status.Error(codes.InvalidArgument, "config_json is required")
	}

	streamConfig, err := s.streamSvc.PrepareStream(ctx, configJSON)
	if err != nil {
		slog.Error("StreamService failed to prepare stream", "error", err)
		return nil, status.Error(codes.Internal, "failed to process stream configuration")
	}

	return &workerpb.PrepareStreamResponse{
		Success: true,
		Message: fmt.Sprintf("Configuration for stream %s accepted.", streamConfig.ID),
	}, nil
}

func (s *grpcServer) GetSystemStatus(ctx context.Context, _ *emptypb.Empty) (*workerpb.SystemStatusResponse, error) {
	resp := &workerpb.SystemStatusResponse{
		CpuUsage: &workerpb.CPUStatus{},
		GpuUsage: &workerpb.GPUStatus{},
	}

	if s.resourceManager != nil {
		status, err := s.resourceManager.GetStatus()
		if err != nil {
			slog.Warn("GetSystemStatus: failed to read hardware status", "error", err)
		} else if status != nil {
			resp.CpuUsage.UsagePercent = status.CPUUsagePercent
			if len(status.GPUs) > 0 {
				gpu := status.GPUs[0]
				resp.GpuUsage.VramUsagePercent = gpu.VRAMUsagePercent
			}
		}
	}

	for _, queueName := range []string{queue.QueueNameEncoder, queue.QueueNameGeneral} {
		info, err := s.inspector.GetQueueInfo(queueName)
		if err != nil {
			slog.Warn("GetSystemStatus: failed to read queue info", "queue", queueName, "error", err)
			continue
		}
		resp.Queues = append(resp.Queues, &workerpb.QueueInfo{
			Name:    queueName,
			Active:  int32(info.Active),
			Pending: int32(info.Pending),
			Total:   int32(info.Size),
		})
	}

	return resp, nil
}

func (s *grpcServer) GetLogs(ctx context.Context, req *workerpb.GetLogsRequest) (*workerpb.GetLogsResponse, error) {
	logs, err := s.logStore.GetLogs(ctx, req.GetLevel(), req.Offset, req.Limit)
	if err != nil {
		slog.Error("gRPC GetLogs failed to retrieve logs from store", "error", err)
		return nil, status.Errorf(codes.Internal, "failed to retrieve logs: %v", err)
	}

	pbLogs := make([]*workerpb.LogEntry, 0, len(logs))
	for _, log := range logs {
		ts, _ := log["time"].(string)
		level, _ := log["level"].(string)
		msg, _ := log["msg"].(string)

		customFields := make(map[string]interface{})
		for k, v := range log {
			if k != "time" && k != "level" && k != "msg" {
				customFields[k] = v
			}
		}

		fieldsStruct, err := structpb.NewStruct(customFields)
		if err != nil {
			slog.Warn("Failed to convert log fields to proto struct", "error", err)
			continue
		}

		pbLogs = append(pbLogs, &workerpb.LogEntry{
			Timestamp: ts,
			Level:     level,
			Message:   msg,
			Fields:    fieldsStruct,
		})
	}

	return &workerpb.GetLogsResponse{Logs: pbLogs}, nil
}

func (s *grpcServer) CreateJob(ctx context.Context, req *workerpb.CreateJobRequest) (*workerpb.CreateJobResponse, error) {
	if req.GetConfig() == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	var jobConfig videoTypes.Config
	jsonBytes, err := req.GetConfig().MarshalJSON()
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to process config: %v", err)
	}
	if err := json.Unmarshal(jsonBytes, &jobConfig); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "failed to decode config: %v", err)
	}
	if err := s.validate.Struct(jobConfig); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validation failed: %v", err)
	}
	startPayload := queue.StartWorkflowPayload{Config: jobConfig}
	task, err := queue.NewTask(queue.TypeDownloadSource, startPayload, queue.QueueNameGeneral)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create job task: %v", err)
	}
	info, err := s.queueClient.EnqueueContext(ctx, task, asynq.MaxRetry(0))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to enqueue job: %v", err)
	}
	return &workerpb.CreateJobResponse{
		Message: "Job workflow accepted for processing.",
		JobId:   jobConfig.JobID,
		QueueId: info.ID,
		Queue:   info.Queue,
	}, nil
}

func mapToProtoStruct(m map[string]string) (*structpb.Struct, error) {
	fields := make(map[string]interface{}, len(m))
	for k, v := range m {
		fields[k] = v
	}
	return structpb.NewStruct(fields)
}

func (s *grpcServer) ListAllActiveStreams(ctx context.Context, _ *emptypb.Empty) (*workerpb.ListStreamsResponse, error) {
	streamInfoMaps, err := s.streamSvc.ListAllActiveStreams(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list active streams: %v", err)
	}

	pbStreams := make([]*workerpb.StreamInfo, len(streamInfoMaps))

	for i, streamMap := range streamInfoMaps {
		detailsStruct, err := mapToProtoStruct(streamMap)
		if err != nil {
			streamID := streamMap["id"]
			slog.Warn("Failed to convert stream details to proto struct", "streamID", streamID, "error", err)
			continue
		}

		pbStreams[i] = &workerpb.StreamInfo{
			StreamId: streamMap["id"],
			NodeId:   streamMap["nodeId"],
			Details:  detailsStruct,
		}
	}

	return &workerpb.ListStreamsResponse{Streams: pbStreams}, nil
}

func (s *grpcServer) ListRecentlyClosedStreams(ctx context.Context, req *workerpb.ListRecentlyClosedStreamsRequest) (*workerpb.ListStreamsResponse, error) {
	count := req.GetCount()
	if count <= 0 {
		count = 20
	}

	streamInfoMaps, err := s.streamSvc.ListRecentlyClosedStreams(ctx, count)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list closed streams: %v", err)
	}

	pbStreams := make([]*workerpb.StreamInfo, len(streamInfoMaps))

	for i, streamMap := range streamInfoMaps {
		detailsStruct, err := mapToProtoStruct(streamMap)
		if err != nil {
			streamID := streamMap["id"]
			slog.Warn("Failed to convert stream details to proto struct", "streamID", streamID, "error", err)
			continue
		}

		pbStreams[i] = &workerpb.StreamInfo{
			StreamId: streamMap["id"],
			NodeId:   streamMap["nodeId"],
			Details:  detailsStruct,
		}
	}

	return &workerpb.ListStreamsResponse{Streams: pbStreams}, nil
}

func (s *grpcServer) GetStreamDetails(ctx context.Context, req *workerpb.GetStreamDetailsRequest) (*workerpb.StreamDetailsResponse, error) {
	details, err := s.egressSvc.GetStreamDetailsWithSinks(ctx, req.GetStreamId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to get stream details: %v", err)
	}

	detailsStruct, err := toProtoStruct(details)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to convert details to response format: %v", err)
	}

	return &workerpb.StreamDetailsResponse{Details: detailsStruct}, nil
}

func (s *grpcServer) ForceStopStream(ctx context.Context, req *workerpb.ForceStopStreamRequest) (*workerpb.ForceStopStreamResponse, error) {
	if err := s.streamSvc.StopStream(req.GetStreamId()); err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to stop stream: %v", err)
	}
	return &workerpb.ForceStopStreamResponse{
		Message: "Stream termination initiated on this node.",
	}, nil
}

func (s *grpcServer) AddRtmpPushSink(ctx context.Context, req *workerpb.AddRtmpPushSinkRequest) (*workerpb.AddRtmpPushSinkResponse, error) {
	addReq := service.AddRtmpPushSinkRequest{
		Platform:  req.GetPlatform(),
		RemoteURL: req.GetRemoteUrl(),
		APIKey:    req.GetApiKey(),
	}
	if err := s.validate.Struct(addReq); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validation failed: %v", err)
	}
	egressID, err := s.egressSvc.AddRtmpPushSink(ctx, req.GetStreamId(), addReq)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to add sink: %v", err)
	}
	return &workerpb.AddRtmpPushSinkResponse{
		Message:  "Command to start sink accepted.",
		EgressId: egressID,
	}, nil
}

func (s *grpcServer) StopSink(ctx context.Context, req *workerpb.StopSinkRequest) (*emptypb.Empty, error) {
	err := s.egressSvc.StopSink(ctx, req.GetStreamId(), req.GetEgressId())
	if err != nil {
		if errors.Is(err, store.ErrEgressNotFound) {
			return nil, status.Errorf(codes.NotFound, "egress not found: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to stop sink: %v", err)
	}
	return &emptypb.Empty{}, nil
}

func (s *grpcServer) GetConfiguredSinksForStream(ctx context.Context, req *workerpb.GetSinksRequest) (*workerpb.GetSinksResponse, error) {
	sinks, err := s.egressSvc.GetConfiguredSinksForStream(ctx, req.GetStreamId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to retrieve sinks: %v", err)
	}

	pbSinks := make([]*structpb.Struct, len(sinks))
	for i, sink := range sinks {
		sinkStruct, err := toProtoStruct(sink)
		if err != nil {
			slog.Warn("Failed to convert sink to proto struct", "error", err)
			continue
		}
		pbSinks[i] = sinkStruct
	}

	return &workerpb.GetSinksResponse{Sinks: pbSinks}, nil
}

func (s *grpcServer) SaveCredentials(ctx context.Context, req *workerpb.SaveCredentialsRequest) (*emptypb.Empty, error) {
	provider := req.GetProvider()
	var credsToSave map[string]interface{}
	var validationErr error

	switch creds := req.GetCredentials().(type) {
	case *workerpb.SaveCredentialsRequest_Aws:
		body := videoTypes.AWSCredentialsBody{
			AccessKeyID:     creds.Aws.AccessKeyId,
			SecretAccessKey: creds.Aws.SecretAccessKey,
		}
		validationErr = s.validate.Struct(body)
		credsToSave = map[string]interface{}{"access_key_id": body.AccessKeyID, "secret_access_key": body.SecretAccessKey}
	case *workerpb.SaveCredentialsRequest_Sftp:
		body := videoTypes.SFTPCredentialsBody{
			User:       creds.Sftp.User,
			Host:       creds.Sftp.Host,
			Port:       int(creds.Sftp.Port),
			Password:   creds.Sftp.Password,
			PrivateKey: creds.Sftp.PrivateKey,
		}
		validationErr = s.validate.Struct(body)
		credsToSave = map[string]interface{}{
			"user":        body.User,
			"host":        body.Host,
			"port":        strconv.Itoa(body.Port),
			"password":    body.Password,
			"private_key": body.PrivateKey,
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported provider or credentials type for: %s", provider)
	}
	if validationErr != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validation failed: %v", validationErr)
	}
	if err := s.credStore.Save(ctx, provider, credsToSave); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to save credentials: %v", err)
	}
	return &emptypb.Empty{}, nil
}

func toProtoStruct(v interface{}) (*structpb.Struct, error) {
	bytes, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	s := &structpb.Struct{}
	if err := s.UnmarshalJSON(bytes); err != nil {
		return nil, err
	}
	return s, nil
}
