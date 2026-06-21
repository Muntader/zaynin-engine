package types

import (
	"errors"
	"time"
)

// Config mirrors config.yaml via mapstructure tags.
type Config struct {
	Node         NodeConfig         `mapstructure:"node"`
	ControlPlane ControlPlaneConfig `mapstructure:"control_plane"`
	Api          ApiConfig          `mapstructure:"api"`
	Server       ServerConfig       `mapstructure:"server"`
	Logging      LoggingConfig      `mapstructure:"logging"`
	Resources    ResourcesConfig    `mapstructure:"resources"`
	Storage      StorageConfig      `mapstructure:"storage"`
	Tools        ToolsConfig        `mapstructure:"tools"`
	Redis        RedisConfig        `mapstructure:"redis"`
	Webhooks     WebhooksConfig     `mapstructure:"webhooks"`
}

type NodeConfig struct {
	ID     string `mapstructure:"id"`
	Type   string `mapstructure:"type"`
	Region string `mapstructure:"region"`
}

type ControlPlaneConfig struct {
	Address           string        `mapstructure:"address"`
	HeartbeatInterval time.Duration `mapstructure:"heartbeat_interval"`
}

type ApiConfig struct {
	APIKey    string `mapstructure:"api_key"`
	JWTSecret string `mapstructure:"jwt_secret"`
}

type ServerConfig struct {
	BindAddress            string      `mapstructure:"bind_address"`
	GRPCPort               int         `mapstructure:"grpc_port"`
	HTTPPort               int         `mapstructure:"http_port"`
	GRPCReflectionEnabled  bool        `mapstructure:"grpc_reflection_enabled"`
	PprofEnabled           bool        `mapstructure:"pprof_enabled"`
	PprofPort              int         `mapstructure:"pprof_port"`
	Media                  MediaConfig `mapstructure:"media"`
}

type MediaConfig struct {
	RTMPPort int `mapstructure:"rtmp_port"`
	SRTPort  int `mapstructure:"srt_port"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type ResourcesConfig struct {
	CPUCoresPerJob            int             `mapstructure:"cpu_cores_per_job"`
	EstimatedVRAMGBPerSession int             `mapstructure:"estimated_vram_gb_per_session"`
	DynamicPortRange          PortRangeConfig `mapstructure:"dynamic_port_range"`
}

type PortRangeConfig struct {
	Start int `mapstructure:"start"`
	End   int `mapstructure:"end"`
}

type StorageConfig struct {
	Paths StoragePathsConfig `mapstructure:"paths"`
}

type StoragePathsConfig struct {
	LiveMedia    string `mapstructure:"live_media"`
	LiveArchive  string `mapstructure:"live_archive"`
	VODWorkspace string `mapstructure:"vod_workspace"`
}

type ToolsConfig struct {
	BinDir string `mapstructure:"bin_dir"`
}

type RedisConfig struct {
	Address  string `mapstructure:"address"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type WebhooksConfig struct {
	NotificationURL string `mapstructure:"notification_url"`
}

// Validate fails fast at boot if required fields are missing.
func (c *Config) Validate() error {
	if c.Node.ID == "" {
		return errors.New("node.id must be set in the configuration file")
	}
	if c.ControlPlane.Address == "" {
		return errors.New("control_plane.address must be set in the configuration file")
	}
	return nil
}
