package api

import (
	"context"
	"crypto/subtle"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const apiKeyMetadataKey = "x-api-key"

func apiKeyUnaryInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	if apiKey == "" {
		return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
			return handler(ctx, req)
		}
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := validateAPIKey(ctx, apiKey); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func validateAPIKey(ctx context.Context, apiKey string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	values := md.Get(apiKeyMetadataKey)
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "no API key provided")
	}

	provided := strings.TrimSpace(values[0])
	if subtle.ConstantTimeCompare([]byte(provided), []byte(apiKey)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid API key")
	}

	return nil
}
