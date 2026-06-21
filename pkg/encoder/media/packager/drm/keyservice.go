// DRM key providers   axinom, simple aes, etc.
package drm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

type KeyServiceProvider interface {
	FetchKeys(ctx context.Context, contentID string, drmTypes []string) (*DRMKeys, error)
}

type DRMKeys struct {
	KID        string
	PSSH       map[string]string
	SkdURI     string
	IsFairPlay bool

	ContentKey string
	IV         string

	AESKeyURI   string
	IsSimpleAES bool
}

type providerConfigShell struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

func NewKeyServiceProvider(providerJSON json.RawMessage) (KeyServiceProvider, error) {
	var shell providerConfigShell
	if err := json.Unmarshal(providerJSON, &shell); err != nil {
		return nil, fmt.Errorf("failed to parse provider type: %w", err)
	}

	if shell.Config == nil {
		return nil, fmt.Errorf("provider 'config' object is missing for type '%s'", shell.Type)
	}

	switch shell.Type {
	case "axinom":
		var axinomCfg AxinomConfig
		if err := json.Unmarshal(shell.Config, &axinomCfg); err != nil {
			return nil, fmt.Errorf("failed to parse 'axinom' provider config: %w", err)
		}
		return NewAxinomKeyService(axinomCfg)

	case "simple_aes":
		var simpleAesCfg SimpleAESConfig
		if err := json.Unmarshal(shell.Config, &simpleAesCfg); err != nil {
			return nil, fmt.Errorf("failed to parse 'simple_aes' provider config: %w", err)
		}
		return NewSimpleAESKeyService(simpleAesCfg)

	default:
		return nil, fmt.Errorf("unsupported DRM provider type: %s", shell.Type)
	}
}

type SimpleAESConfig struct {
	Kid    string `json:"kid"`
	Key    string `json:"key"`
	KeyURI string `json:"key_uri"`
	IV     string `json:"iv,omitempty"`
}

type SimpleAESKeyService struct {
	config SimpleAESConfig
}

func NewSimpleAESKeyService(config SimpleAESConfig) (*SimpleAESKeyService, error) {
	if config.Key == "" || config.KeyURI == "" {
		return nil, fmt.Errorf("simple_aes config requires 'key' and 'key_uri'")
	}
	hexRegex := regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	if !hexRegex.MatchString(config.Key) {
		return nil, fmt.Errorf("simple_aes 'key' must be a 32-character hex string")
	}
	if config.IV != "" && !hexRegex.MatchString(config.IV) {
		return nil, fmt.Errorf("simple_aes 'iv' must be a 32-character hex string if provided")
	}

	return &SimpleAESKeyService{config: config}, nil
}

// static keys from config   contentID/drmTypes ignored
func (s *SimpleAESKeyService) FetchKeys(ctx context.Context, contentID string, drmTypes []string) (*DRMKeys, error) {
	return &DRMKeys{
		IsSimpleAES: true,
		ContentKey:  s.config.Key,
		AESKeyURI:   s.config.KeyURI,
		KID:         s.config.Kid,
		IV:          s.config.IV,
	}, nil
}
