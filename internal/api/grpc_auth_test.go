package api

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestValidateAPIKeyMissingMetadata(t *testing.T) {
	err := validateAPIKey(context.Background(), "secret")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestValidateAPIKeyMissingKey(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("other", "value"))
	err := validateAPIKey(ctx, "secret")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestValidateAPIKeyInvalidKey(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(apiKeyMetadataKey, "wrong"))
	err := validateAPIKey(ctx, "secret")
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
}

func TestValidateAPIKeyValidKey(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(apiKeyMetadataKey, "secret"))
	if err := validateAPIKey(ctx, "secret"); err != nil {
		t.Fatalf("validateAPIKey: %v", err)
	}
}

func TestAPIKeyUnaryInterceptorRejectsInvalidKey(t *testing.T) {
	interceptor := apiKeyUnaryInterceptor("secret")
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(apiKeyMetadataKey, "wrong"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated", status.Code(err))
	}
	if called {
		t.Fatal("handler should not run with invalid key")
	}
}

func TestAPIKeyUnaryInterceptorAllowsValidKey(t *testing.T) {
	interceptor := apiKeyUnaryInterceptor("secret")
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(apiKeyMetadataKey, "secret"))
	if _, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !called {
		t.Fatal("handler should run with valid key")
	}
}

func TestAPIKeyUnaryInterceptorSkipsWhenKeyNotConfigured(t *testing.T) {
	interceptor := apiKeyUnaryInterceptor("")
	called := false
	handler := func(ctx context.Context, req any) (any, error) {
		called = true
		return "ok", nil
	}

	if _, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if !called {
		t.Fatal("handler should run when API key is not configured")
	}
}
