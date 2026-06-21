package api

import (
	"fmt"
	"github.com/hibiken/asynq"
	"github.com/muntader/zaynin-engine/internal/api/handlers/api"
	"github.com/muntader/zaynin-engine/internal/api/middleware"
	"github.com/muntader/zaynin-engine/internal/common/logging"
	configTypes "github.com/muntader/zaynin-engine/internal/common/types"
	"github.com/muntader/zaynin-engine/internal/hardware"
	"github.com/muntader/zaynin-engine/internal/live/core"
	"github.com/muntader/zaynin-engine/internal/live/core/service"
	"github.com/muntader/zaynin-engine/internal/live/core/store"
	vodStore "github.com/muntader/zaynin-engine/internal/vod/store"
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/redis/go-redis/v9"
)

// NewAPIServer wires HTTP routes for live, VOD, creds, and logs.
func NewAPIServer(
	cfg configTypes.Config,
	coreManager *core.Manager,
	redisClient *redis.Client,
	queueClient *asynq.Client,
	credStore *vodStore.BoltCredentialsStore,
	resourceManager *hardware.ResourceManager,
) *http.Server {

	router := mux.NewRouter()
	router.Use(middleware.PanicRecovery)

	egressStore := store.NewEgressStore(redisClient)
	streamStore := store.NewStreamStore(redisClient, coreManager.NodeID())

	streamSvc := service.NewStreamService(coreManager, streamStore)
	egressSvc := service.NewEgressService(coreManager, egressStore, streamStore)

	streamHandler := api.NewStreamHandler(streamSvc, egressSvc)
	egressHandler := api.NewEgressHandler(egressSvc)

	credHandler := api.NewCredentialsHandler(credStore)

	logStore := logging.NewLogStore(redisClient)
	logHandler := api.NewLogHandler(logStore)

	apiV1 := router.PathPrefix("/api/v1").Subrouter()
	if cfg.Api.APIKey != "" {
		apiV1.Use(middleware.APIKeyAuth(cfg.Api.APIKey))
	}

	jobHandler := api.NewJobHandler(queueClient)

	apiV1.HandleFunc("/logs", logHandler.GetLogs).Methods("GET")

	jobAPI := apiV1.PathPrefix("/jobs").Subrouter()
	jobAPI.HandleFunc("", jobHandler.CreateJob).Methods("POST")

	clusterAPI := apiV1.PathPrefix("/cluster").Subrouter()
	clusterAPI.HandleFunc("/streams/active", streamHandler.ListAllActiveStreams).Methods("GET")
	clusterAPI.HandleFunc("/streams/closed", streamHandler.ListRecentlyClosedStreams).Methods("GET")

	streamsAPI := apiV1.PathPrefix("/streams/{streamID}").Subrouter()
	streamsAPI.HandleFunc("", streamHandler.GetStreamDetails).Methods("GET")
	streamsAPI.HandleFunc("/sinks", egressHandler.GetConfiguredSinksForStream).Methods("GET")
	streamsAPI.HandleFunc("/sinks/rtmp_push", egressHandler.AddRtmpPushSink).Methods("POST")
	streamsAPI.HandleFunc("/sinks/{egressID}", egressHandler.StopSink).Methods("DELETE")

	nodeAPI := apiV1.PathPrefix("/node").Subrouter()
	nodeAPI.HandleFunc("/streams/{streamID}", streamHandler.GetNodeStreamDetails).Methods("GET")
	nodeAPI.HandleFunc("/streams/{streamID}", streamHandler.ForceStopStream).Methods("DELETE")

	credAPI := apiV1.PathPrefix("/credentials").Subrouter()
	credAPI.HandleFunc("/{provider}", credHandler.SaveCredentials).Methods("POST")

	return &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Server.HTTPPort),
		Handler:      router,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
}
