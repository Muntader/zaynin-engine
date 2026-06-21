package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/muntader/zaynin-engine/internal/api"
	"github.com/muntader/zaynin-engine/internal/common/central_api_client"
	"github.com/muntader/zaynin-engine/internal/common/logging"
	"github.com/muntader/zaynin-engine/internal/common/notifier"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/ingress/rtmp"
	"github.com/muntader/zaynin-engine/internal/live/ingress/srt"
	"github.com/muntader/zaynin-engine/internal/vod/pipeline"
	"github.com/muntader/zaynin-engine/internal/vod/queue"
	"github.com/muntader/zaynin-engine/internal/vod/service"
	"github.com/muntader/zaynin-engine/internal/vod/store"
	"github.com/muntader/zaynin-engine/pkg/toolpath"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"google.golang.org/grpc"

	_ "net/http/pprof"

	_ "github.com/muntader/zaynin-engine/internal/live/egress/forward"
	_ "github.com/muntader/zaynin-engine/internal/live/pipeline/packager"
	_ "github.com/muntader/zaynin-engine/internal/live/pipeline/recoder"
	_ "github.com/muntader/zaynin-engine/internal/live/pipeline/transcoder"
)

const (
	secretsFile         = ".worker-secrets"
	shutdownTimeout     = 30 * time.Second
	defaultSecretLength = 24
	jwtSecretLength     = 32
	defaultPprofPort    = 6060
)

// ServiceManager holds everything we start in main and tear down on exit.
type ServiceManager struct {
	httpAPIServer   *http.Server
	grpcAPIServer   *grpc.Server
	grpcListener    net.Listener
	grpcClientApi   *central_api_client.Client
	rtmpServer      *rtmp.Server
	srtServer       *srt.Server
	cpuWorkerServer *asynq.Server
	ioWorkerServer  *asynq.Server
	cpuWorkerMux    *asynq.ServeMux
	ioWorkerMux     *asynq.ServeMux
	redisClient     *redis.Client
	credStore       *store.BoltCredentialsStore
}

func main() {
	initConfig()

	appConfig := loadAndValidateConfig()
	redisClient := initializeRedis(context.Background(), appConfig.Redis)
	setupLogger(appConfig.Logging, redisClient)

	if appConfig.Server.PprofEnabled {
		startPprofServer(appConfig.Server.PprofPort)
	}

	resourceManager := setupEnvironment(&appConfig)

	serviceMgr := initializeServices(redisClient, appConfig, resourceManager)
	defer serviceMgr.cleanup()

	serviceMgr.startServices(appConfig)
	serviceMgr.waitForShutdown()
}

func startPprofServer(port int) {
	if port == 0 {
		port = defaultPprofPort
	}
	addr := fmt.Sprintf(":%d", port)
	slog.Info("pprof enabled", "address", "http://localhost"+addr+"/debug/pprof/")
	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			slog.Error("pprof server stopped", "error", err)
		}
	}()
}

func initConfig() {
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.SetEnvPrefix("ZAYNIN")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			slog.Error("config.yaml not found   copy config.example.yaml and edit it")
		} else {
			slog.Error("failed to read config", "error", err)
		}
	}
	slog.Info("using config file", "file", viper.ConfigFileUsed())
}

func loadAndValidateConfig() configTypes.Config {
	if err := managePersistentSecrets(); err != nil {
		slog.Error("could not load worker secrets", "error", err)
	}

	var cfg configTypes.Config
	if err := viper.Unmarshal(&cfg); err != nil {
		slog.Error("config decode failed", "error", err)
	}

	// .worker-secrets uses api.apiKey in viper; mapstructure expects api.api_key.
	if cfg.Api.APIKey == "" {
		cfg.Api.APIKey = viper.GetString("api.apiKey")
	}
	if cfg.Api.JWTSecret == "" {
		cfg.Api.JWTSecret = viper.GetString("api.jwtSecret")
	}
	if !viper.IsSet("server.grpc_reflection_enabled") {
		cfg.Server.GRPCReflectionEnabled = true
	}

	if err := cfg.Validate(); err != nil {
		slog.Error("invalid config", "error", err)
	}

	if cfg.Api.APIKey == "" {
		slog.Warn("API key not configured; HTTP and gRPC APIs will accept unauthenticated requests")
	}

	return cfg
}

func setupLogger(cfg configTypes.LoggingConfig, redisClient *redis.Client) {
	logging.Init(cfg.Level, cfg.Format, redisClient)
}

func setupEnvironment(appConfig *configTypes.Config) *hardware.ResourceManager {
	appConfig.Tools.BinDir = resolveAndValidateBinDir(appConfig.Tools.BinDir)
	slog.Info("external tools directory", "path", appConfig.Tools.BinDir)
	toolpath.Init(appConfig.Tools.BinDir)
	checkExternalTools()

	resManager, err := hardware.NewResourceManager(hardware.Config{
		LoadThreshold: 0.90,
	})
	if err != nil {
		slog.Error("hardware monitor failed to start", "error", err)
		os.Exit(1)
	}
	return resManager
}

var externalTools = []struct {
	name    string
	purpose string
}{
	{"ffmpeg", "transcoding"},
	{"ffprobe", "media analysis"},
	{"shaka-packager", "live HLS/DASH packaging"},
	{"mp4fragment", "VOD fragmentation"},
	{"mp4dash", "VOD DASH/CMAF packaging"},
	{"mp4hls", "VOD HLS packaging (AES-128)"},
}

func checkExternalTools() {
	for _, tool := range externalTools {
		path, err := toolpath.Resolve(tool.name)
		if err != nil {
			slog.Warn("external tool not found",
				"tool", tool.name,
				"purpose", tool.purpose,
				"bin_dir", toolpath.BinDir(),
				"error", err,
			)
			continue
		}
		slog.Info("external tool resolved", "tool", tool.name, "path", path)
	}
}

func initializeServices(
	redisClient *redis.Client,
	appConfig configTypes.Config,
	resourceManager *hardware.ResourceManager,
) *ServiceManager {
	if err := os.MkdirAll(appConfig.Storage.Paths.VODWorkspace, 0755); err != nil {
		slog.Error("could not create VOD workspace", "error", err)
	}
	slog.Info("VOD workspace", "path", appConfig.Storage.Paths.VODWorkspace)

	commonNotifier := notifier.New(appConfig.Webhooks.NotificationURL)

	queueClient := asynq.NewClient(asynq.RedisClientOpt{Addr: appConfig.Redis.Address})
	cpuWorkerServer := queue.NewCPUQueueServer(appConfig.Redis.Address, 1)
	cpuWorkerMux := asynq.NewServeMux()
	ioWorkerServer := queue.NewIOQueueServer(appConfig.Redis.Address)
	ioWorkerMux := asynq.NewServeMux()
	credStore, _ := store.NewBoltCredentialsStore("zaynin-credentials.db")
	storageService := service.NewStorageService(*credStore)

	streamManager, err := core.NewManager(appConfig, redisClient, commonNotifier, resourceManager, queueClient)
	if err != nil {
		slog.Error("stream manager init failed", "error", err)
		os.Exit(1)
	}

	var centralClient *central_api_client.Client
	if appConfig.Node.ID != "" && appConfig.ControlPlane.Address != "" {
		interval := time.Duration(appConfig.ControlPlane.HeartbeatInterval) * time.Second
		centralClient, err = central_api_client.New(
			appConfig.ControlPlane.Address,
			appConfig.Node.ID,
			interval,
			resourceManager,
			streamManager,
			&appConfig,
		)
		if err != nil {
			slog.Error("control plane client failed   worker wont register", "error", err)
			os.Exit(1)
		}
	} else {
		slog.Warn("node id or control plane address missing; running without central registration")
	}

	rtmpServer, _ := rtmp.NewRTMPServer(appConfig, streamManager)
	srtServer, _ := srt.NewSRTServer(appConfig, streamManager)
	grpcAPIServer, grpcListener, _ := api.NewGRPCServer(appConfig, streamManager, redisClient, queueClient, credStore, resourceManager)

	pipeline.RegisterVODHandlers(
		cpuWorkerMux,
		ioWorkerMux,
		queueClient,
		redisClient,
		centralClient,
		appConfig.Storage.Paths.VODWorkspace,
		storageService,
		commonNotifier,
	)

	return &ServiceManager{
		httpAPIServer:   api.NewAPIServer(appConfig, streamManager, redisClient, queueClient, credStore, resourceManager),
		grpcAPIServer:   grpcAPIServer,
		grpcListener:    grpcListener,
		grpcClientApi:   centralClient,
		rtmpServer:      rtmpServer,
		srtServer:       srtServer,
		cpuWorkerServer: cpuWorkerServer,
		ioWorkerServer:  ioWorkerServer,
		cpuWorkerMux:    cpuWorkerMux,
		ioWorkerMux:     ioWorkerMux,
		redisClient:     redisClient,
		credStore:       credStore,
	}
}

func initializeRedis(ctx context.Context, config configTypes.RedisConfig) *redis.Client {
	redisClient := redis.NewClient(&redis.Options{Addr: config.Address, Password: config.Password, DB: config.DB})
	if _, err := redisClient.Ping(ctx).Result(); err != nil {
		slog.Error("redis ping failed", "error", err)
	}
	return redisClient
}

func (sm *ServiceManager) startServices(appConfig configTypes.Config) {
	if sm.grpcClientApi != nil {
		sm.grpcClientApi.StartHeartbeat()
	}
	go sm.startRTMPServer()
	go sm.startSRTServer()
	go sm.startHTTPAPIServer(appConfig)
	go sm.startGRPCAPIServer(appConfig)
	go sm.startEncoderWorker()
	go sm.startGeneralWorker()
}

func (sm *ServiceManager) startRTMPServer() {
	if err := sm.rtmpServer.ListenAndServe(); err != nil {
		slog.Error("RTMP server error", "error", err)
	}
}

func (sm *ServiceManager) startSRTServer() {
	if err := sm.srtServer.ListenAndServe(); err != nil {
		slog.Error("SRT server error", "error", err)
	}
}

func (sm *ServiceManager) startHTTPAPIServer(apiCfg configTypes.Config) {
	addr := fmt.Sprintf("%s:%d", apiCfg.Server.BindAddress, apiCfg.Server.HTTPPort)
	slog.Info("HTTP API starting (legacy)", "address", addr)
	if err := sm.httpAPIServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("HTTP API fatal error", "error", err)
	}
}

func (sm *ServiceManager) startGRPCAPIServer(apiCfg configTypes.Config) {
	addr := fmt.Sprintf("%s:%d", apiCfg.Server.BindAddress, apiCfg.Server.GRPCPort)
	slog.Info("gRPC API starting", "address", addr)
	if err := sm.grpcAPIServer.Serve(sm.grpcListener); err != nil {
		slog.Error("gRPC API fatal error", "error", err)
	}
}

func (sm *ServiceManager) startEncoderWorker() {
	if err := sm.cpuWorkerServer.Run(sm.cpuWorkerMux); err != nil {
		slog.Error("CPU worker stopped", "error", err)
	}
}

func (sm *ServiceManager) startGeneralWorker() {
	slog.Info("I/O worker starting", "queue", queue.QueueNameEncoder)
	if err := sm.ioWorkerServer.Run(sm.ioWorkerMux); err != nil {
		slog.Error("I/O worker stopped", "error", err)
	}
}

func (sm *ServiceManager) waitForShutdown() {
	slog.Info("running   Ctrl+C to stop")
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down...")
	sm.shutdown()
	slog.Info("bye")
}

func (sm *ServiceManager) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	slog.Info("stopping ingest and APIs")
	sm.grpcAPIServer.GracefulStop()
	if err := sm.httpAPIServer.Shutdown(ctx); err != nil {
		slog.Error("HTTP shutdown error", "error", err)
	}
	sm.rtmpServer.Shutdown(ctx)
	sm.srtServer.Shutdown(ctx)

	// blocks until in-flight asynq tasks finish   avoids redis connection races on exit
	slog.Info("waiting for workers")
	sm.cpuWorkerServer.Shutdown()
	sm.ioWorkerServer.Shutdown()

	if sm.grpcClientApi != nil {
		sm.grpcClientApi.Close()
	}

	sm.cleanup()
}

func (sm *ServiceManager) cleanup() {
	if sm.credStore != nil {
		if err := sm.credStore.Close(); err != nil {
			slog.Error("credentials db close failed", "error", err)
		}
	}
	if sm.redisClient != nil {
		if err := sm.redisClient.Close(); err != nil {
			slog.Error("redis close failed", "error", err)
		}
	}
}

func resolveAndValidateBinDir(binDir string) string {
	absPath, err := filepath.Abs(binDir)
	if err != nil {
		slog.Error("bad bin_dir path", "directory", binDir, "error", err)
	}
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		slog.Error("bin_dir does not exist   check config", "directory", absPath)
	}
	return absPath
}

func generateRandomSecret(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func managePersistentSecrets() error {
	if viper.IsSet("api.apiKey") && viper.IsSet("api.jwtSecret") {
		slog.Info("API key and JWT secret loaded from environment")
		return nil
	}
	return loadOrCreateSecretsFile()
}

func loadOrCreateSecretsFile() error {
	file, err := os.Open(secretsFile)
	if err != nil {
		if os.IsNotExist(err) {
			return generateAndSaveSecrets()
		}
		return fmt.Errorf("open %s: %w", secretsFile, err)
	}
	defer file.Close()
	slog.Info("loading worker secrets", "file", secretsFile)
	return parseSecretsFile(file)
}

func generateAndSaveSecrets() error {
	slog.Warn("no secrets file   generating one", "file", secretsFile)
	apiKey, err := generateRandomSecret(defaultSecretLength)
	if err != nil {
		return fmt.Errorf("api key: %w", err)
	}
	jwtSecret, err := generateRandomSecret(jwtSecretLength)
	if err != nil {
		return fmt.Errorf("jwt secret: %w", err)
	}

	content := fmt.Sprintf("API_KEY=%s\nJWT_SECRET=%s\n", apiKey, jwtSecret)
	if err := os.WriteFile(secretsFile, []byte(content), 0600); err != nil {
		return fmt.Errorf("write %s: %w", secretsFile, err)
	}

	slog.Info("wrote new secrets file", "file", secretsFile)
	fmt.Println("--- created .worker-secrets   back it up somewhere safe ---")

	viper.Set("api.apiKey", apiKey)
	viper.Set("api.jwtSecret", jwtSecret)
	return nil
}

func parseSecretsFile(file *os.File) error {
	scanner := bufio.NewScanner(file)
	secrets := make(map[string]string)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			secrets[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}

	if !viper.IsSet("api.apiKey") {
		viper.Set("api.apiKey", secrets["API_KEY"])
	}
	if !viper.IsSet("api.jwtSecret") {
		viper.Set("api.jwtSecret", secrets["JWT_SECRET"])
	}
	return scanner.Err()
}
