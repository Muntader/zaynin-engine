package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"go.etcd.io/bbolt"
)

// BoltCredentialsStore keeps provider creds on disk so jobs dont need them inline.
type BoltCredentialsStore struct {
	db *bbolt.DB
}

func NewBoltCredentialsStore(dbPath string) (*BoltCredentialsStore, error) {
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open bolt db at %s: %w", dbPath, err)
	}

	err = db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("credentials"))
		return err
	})
	if err != nil {
		return nil, err
	}

	return &BoltCredentialsStore{db: db}, nil
}

func (s *BoltCredentialsStore) Close() error {
	return s.db.Close()
}

func (s *BoltCredentialsStore) Save(ctx context.Context, provider string, creds map[string]interface{}) error {
	return s.db.Update(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("credentials"))
		if b == nil {
			slog.Error("CRITICAL: 'credentials' bucket not found!")
			return fmt.Errorf("credentials bucket not found")
		}

		data, err := json.Marshal(creds)
		if err != nil {
			slog.Error("Failed to marshal credentials JSON", "error", err)
			return err
		}

		err = b.Put([]byte(provider), data)
		if err != nil {
			slog.Error("Failed to b.Put data into bucket", "error", err)
		} else {
		}
		return err
	})
}

func (s *BoltCredentialsStore) Get(ctx context.Context, provider string) (map[string]string, error) {
	var creds map[string]string
	err := s.db.View(func(tx *bbolt.Tx) error {
		b := tx.Bucket([]byte("credentials"))
		data := b.Get([]byte(provider))
		if data == nil {
			return nil // missing creds isnt an error   caller falls back to ADC etc
		}
		return json.Unmarshal(data, &creds)
	})
	return creds, err
}
