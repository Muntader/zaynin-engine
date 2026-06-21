package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

const (
	s3CredentialsKey    = "credentials:s3"
	gcsCredentialsKey   = "credentials:gcs"
	azureCredentialsKey = "credentials:azure"
	r2CredentialsKey    = "credentials:r2"
)

// CredentialsStore persists cloud creds in Redis (older path   Bolt is preferred now).
type CredentialsStore struct {
	redisClient *redis.Client
}

func NewCredentialsStore(client *redis.Client) *CredentialsStore {
	return &CredentialsStore{redisClient: client}
}

// Save overwrites stored creds for a provider hash.
func (s *CredentialsStore) Save(ctx context.Context, provider string, creds map[string]interface{}) error {
	key, err := s.getKeyForProvider(provider)
	if err != nil {
		return err
	}
	return s.redisClient.HSet(ctx, key, creds).Err()
}

func (s *CredentialsStore) Get(ctx context.Context, provider string) (map[string]string, error) {
	key, err := s.getKeyForProvider(provider)
	if err != nil {
		return nil, err
	}
	return s.redisClient.HGetAll(ctx, key).Result()
}

func (s *CredentialsStore) getKeyForProvider(provider string) (string, error) {
	switch provider {
	case "s3":
		return s3CredentialsKey, nil
	case "gcs":
		return gcsCredentialsKey, nil
	case "azure":
		return azureCredentialsKey, nil
	case "r2":
		return r2CredentialsKey, nil
	default:
		return "", fmt.Errorf("unsupported credentials provider: %s", provider)
	}
}
