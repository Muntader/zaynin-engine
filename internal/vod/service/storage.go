package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"google.golang.org/api/option"

	"github.com/muntader/zaynin-engine/internal/vod/store"
	"github.com/muntader/zaynin-engine/internal/vod/types"
)

// StorageService talks to cloud/object stores directly   no shelling out to awscli etc.
type StorageService struct {
	credStore store.BoltCredentialsStore
}

// NewStorageService wires up credential lookup for per-job or stored creds.
func NewStorageService(credStore store.BoltCredentialsStore) *StorageService {
	return &StorageService{
		credStore: credStore,
	}
}

// DownloadFile pulls the input object down to destPath based on provider config.
func (s *StorageService) DownloadFile(ctx context.Context, input types.InputStorage, destPath string) error {

	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return fmt.Errorf("failed to create destination directory for download: %w", err)
	}

	switch input.Provider {
	case "s3":
		return s.downloadS3(ctx, input.S3, destPath)
	case "gcs":
		return s.downloadGCS(ctx, input.GCS, destPath)
	case "azure":
		return s.downloadAzure(ctx, input.Azure, destPath)
	case "r2":
		return s.downloadR2(ctx, input.R2, destPath)
	case "http":
		return s.downloadHTTP(ctx, input.HTTP, destPath)
	case "sftp":
		return s.downloadSFTP(ctx, input.SFTP, destPath)
	default:
		return fmt.Errorf("unsupported download provider: %s", input.Provider)
	}
}

// UploadDirectory walks sourceDir and pushes everything to the configured destination.
func (s *StorageService) UploadDirectory(ctx context.Context, output types.OutputStorage, sourceDir string) error {
	slog.Info("Preparing to upload with pure Go SDK", "provider", output.Provider, "output_id", output.OutputID)

	switch output.Provider {
	case "s3":
		return s.uploadS3(ctx, output.S3, sourceDir)
	case "gcs":
		return s.uploadGCS(ctx, output.GCS, sourceDir)
	case "azure":
		return s.uploadAzure(ctx, output.Azure, sourceDir)
	case "r2":
		return s.uploadR2(ctx, output.R2, sourceDir)
	case "sftp":
		return s.uploadSFTP(ctx, output.SFTP, sourceDir)
	case "http":
		return s.uploadHTTP(ctx, output.HTTP, sourceDir)
	case "local":
		return s.uploadLocal(ctx, output.Local, sourceDir)
	default:
		return fmt.Errorf("unsupported upload provider: %s", output.Provider)
	}
}

func (s *StorageService) downloadS3(ctx context.Context, cfg *types.InputS3, destPath string) error {
	awsCfg, err := s.getAWSCfg(ctx, "s3", cfg.Region, cfg.Credentials)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(awsCfg)
	downloader := manager.NewDownloader(client)

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer file.Close()

	numBytes, err := downloader.Download(ctx, file, &s3.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Key),
	})
	if err != nil {
		return fmt.Errorf("failed to download from s3: %w", err)
	}
	slog.Info("S3 download complete", "bytes_downloaded", numBytes, "destination", destPath)
	return nil
}

func (s *StorageService) downloadGCS(ctx context.Context, cfg *types.InputGCS, destPath string) error {
	opts, err := s.getGCSOpts(ctx, cfg.Credentials)
	if err != nil {
		return err
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()

	rc, err := client.Bucket(cfg.Bucket).Object(cfg.Key).NewReader(ctx)
	if err != nil {
		return fmt.Errorf("failed to create GCS object reader: %w", err)
	}
	defer rc.Close()

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer file.Close()

	numBytes, err := io.Copy(file, rc)
	if err != nil {
		return fmt.Errorf("failed to copy GCS object to file: %w", err)
	}
	slog.Info("GCS download complete", "bytes_downloaded", numBytes, "destination", destPath)
	return nil
}

func (s *StorageService) downloadAzure(ctx context.Context, cfg *types.InputAzure, destPath string) error {
	client, err := s.getAzureClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create Azure client: %w", err)
	}
	// stream straight to disk   no temp file dance
	downloadResponse, err := client.DownloadStream(ctx, cfg.Container, cfg.Key, nil)
	if err != nil {
		return fmt.Errorf("failed to download from azure blob: %w", err)
	}
	defer downloadResponse.Body.Close()

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer file.Close()

	numBytes, err := io.Copy(file, downloadResponse.Body)
	if err != nil {
		return fmt.Errorf("failed to copy Azure blob content to file: %w", err)
	}

	slog.Info("Azure download complete", "bytes_downloaded", numBytes, "destination", destPath)
	return nil
}

func (s *StorageService) downloadR2(ctx context.Context, cfg *types.InputR2, destPath string) error {
	awsCfg, err := s.getR2Cfg(ctx, cfg.EndpointURL, cfg.Credentials)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(awsCfg)
	downloader := manager.NewDownloader(client)

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer file.Close()

	numBytes, err := downloader.Download(ctx, file, &s3.GetObjectInput{
		Bucket: aws.String(cfg.Bucket),
		Key:    aws.String(cfg.Key),
	})
	if err != nil {
		return fmt.Errorf("failed to download from r2: %w", err)
	}
	slog.Info("R2 download complete", "bytes_downloaded", numBytes, "destination", destPath)
	return nil
}

func (s *StorageService) downloadHTTP(ctx context.Context, cfg *types.InputHTTP, destPath string) error {
	client := &http.Client{Timeout: 30 * time.Minute}
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.URL, nil)
	if err != nil {
		return fmt.Errorf("failed to create HTTP request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download failed: server returned status %d for URL %s", resp.StatusCode, cfg.URL)
	}
	outFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file %s: %w", destPath, err)
	}
	defer outFile.Close()
	bytesCopied, err := io.Copy(outFile, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to write downloaded content to file: %w", err)
	}
	slog.Info("HTTP download complete", "bytes_downloaded", bytesCopied, "destination", destPath)
	return nil
}

func (s *StorageService) downloadSFTP(ctx context.Context, cfg *types.InputSFTP, destPath string) error {
	client, err := s.getSFTPClient(ctx, cfg.Credentials)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer client.Close()

	srcFile, err := client.Open(cfg.Path)
	if err != nil {
		return fmt.Errorf("failed to open remote file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	bytesCopied, err := io.Copy(dstFile, srcFile)
	if err != nil {
		return fmt.Errorf("failed to copy SFTP file: %w", err)
	}

	slog.Info("SFTP download complete", "bytes_downloaded", bytesCopied, "destination", destPath)
	return nil
}

func (s *StorageService) uploadS3(ctx context.Context, cfg *types.OutputS3, sourceDir string) error {
	awsCfg, err := s.getAWSCfg(ctx, "s3", cfg.Region, cfg.Credentials)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(awsCfg)
	uploader := manager.NewUploader(client)
	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		key := filepath.Join(cfg.Key, strings.TrimPrefix(filePath, sourceDir))
		_, err := uploader.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(key),
			Body:   file,
		})
		return err
	})
}

func (s *StorageService) uploadGCS(ctx context.Context, cfg *types.OutputGCS, sourceDir string) error {
	opts, err := s.getGCSOpts(ctx, cfg.Credentials)
	if err != nil {
		return err
	}
	client, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return fmt.Errorf("failed to create GCS client: %w", err)
	}
	defer client.Close()
	bucket := client.Bucket(cfg.Bucket)
	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		key := filepath.ToSlash(filepath.Join(cfg.Key, strings.TrimPrefix(filePath, sourceDir)))
		wc := bucket.Object(key).NewWriter(ctx)
		if _, err := io.Copy(wc, file); err != nil {
			return err
		}
		return wc.Close()
	})
}

func (s *StorageService) uploadAzure(ctx context.Context, cfg *types.OutputAzure, sourceDir string) error {

	client, err := s.getAzureClient(ctx)
	if err != nil {
		return fmt.Errorf("failed to create Azure client: %w", err)
	}

	// defensive   shouldnt happen but Azure SDK panics are worse than a clear error
	if client == nil {
		return fmt.Errorf("Azure client is nil")
	}

	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		if file == nil {
			return fmt.Errorf("file is nil for path: %s", filePath)
		}

		key := filepath.ToSlash(filepath.Join(cfg.Key, strings.TrimPrefix(filePath, sourceDir)))

		// UploadStream reads from current offset   rewind after the walk opened us
		if _, err := file.Seek(0, 0); err != nil {
			return fmt.Errorf("failed to seek to beginning of file: %w", err)
		}

		_, err = client.UploadStream(ctx, cfg.Container, key, file, nil)
		if err != nil {
			return fmt.Errorf("failed to upload to Azure blob %s: %w", key, err)
		}

		slog.Info("Azure file uploaded", "container", cfg.Container, "key", key, "file", filePath)
		return nil
	})
}

func (s *StorageService) uploadR2(ctx context.Context, cfg *types.OutputR2, sourceDir string) error {
	awsCfg, err := s.getR2Cfg(ctx, cfg.EndpointURL, cfg.Credentials)
	if err != nil {
		return err
	}
	client := s3.NewFromConfig(awsCfg)
	uploader := manager.NewUploader(client)
	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		key := filepath.Join(cfg.Key, strings.TrimPrefix(filePath, sourceDir))
		_, err := uploader.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String(cfg.Bucket),
			Key:    aws.String(key),
			Body:   file,
		})
		return err
	})
}

func (s *StorageService) uploadSFTP(ctx context.Context, cfg *types.OutputSFTP, sourceDir string) error {
	client, err := s.getSFTPClient(ctx, cfg.Credentials)
	if err != nil {
		return fmt.Errorf("failed to create SFTP client: %w", err)
	}
	defer client.Close()

	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		remotePath := filepath.Join(cfg.Path, strings.TrimPrefix(filePath, sourceDir))

		if err := client.MkdirAll(filepath.Dir(remotePath)); err != nil {
			return fmt.Errorf("failed to create remote directory: %w", err)
		}

		dstFile, err := client.Create(remotePath)
		if err != nil {
			return fmt.Errorf("failed to create remote file: %w", err)
		}
		defer dstFile.Close()

		if _, err := io.Copy(dstFile, file); err != nil {
			return fmt.Errorf("failed to copy to remote file: %w", err)
		}

		slog.Info("SFTP file uploaded", "remote_path", remotePath, "file", filePath)
		return nil
	})
}

func (s *StorageService) uploadHTTP(ctx context.Context, cfg *types.OutputHTTP, sourceDir string) error {
	client := &http.Client{Timeout: 5 * time.Minute} // big dirs over slow links need headroom

	return s.uploadDirectory(ctx, sourceDir, func(filePath string, file *os.File) error {
		relativePath := strings.TrimPrefix(filePath, sourceDir)
		relativePath = filepath.ToSlash(relativePath)

		// base URL + relative path → per-segment PUT target
		targetURL := strings.TrimSuffix(cfg.URL, "/") + relativePath

		var buf bytes.Buffer
		if _, err := io.Copy(&buf, file); err != nil {
			return fmt.Errorf("failed to read file into buffer: %w", err)
		}
		content := buf.Bytes()

		req, err := http.NewRequestWithContext(ctx, "PUT", targetURL, bytes.NewReader(content))
		if err != nil {
			return fmt.Errorf("failed to create HTTP request for %s: %w", targetURL, err)
		}

		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		for key, value := range cfg.Headers {
			req.Header.Set(key, value)
		}

		// some origins are picky about Content-Type on segments
		ext := strings.ToLower(filepath.Ext(filePath))
		switch ext {
		case ".m4s", ".mp4":
			req.Header.Set("Content-Type", "video/mp4")
		case ".ts":
			req.Header.Set("Content-Type", "video/mp2t")
		case ".mpd":
			req.Header.Set("Content-Type", "application/dash+xml")
		case ".m3u8":
			req.Header.Set("Content-Type", "application/vnd.apple.mpegurl")
		case ".jpg", ".jpeg":
			req.Header.Set("Content-Type", "image/jpeg")
		case ".png":
			req.Header.Set("Content-Type", "image/png")
		case ".gif":
			req.Header.Set("Content-Type", "image/gif")
		case ".svg":
			req.Header.Set("Content-Type", "image/svg+xml")
		case ".webp":
			req.Header.Set("Content-Type", "image/webp")
		case ".vtt":
			req.Header.Set("Content-Type", "text/vtt")
		case ".txt", ".text":
			req.Header.Set("Content-Type", "text/plain")
		case ".json":
			req.Header.Set("Content-Type", "application/json")
		case ".xml":
			req.Header.Set("Content-Type", "application/xml")
		case ".wav":
			req.Header.Set("Content-Type", "audio/wav")
		case ".mp3":
			req.Header.Set("Content-Type", "audio/mpeg")
		case ".aac":
			req.Header.Set("Content-Type", "audio/aac")
		case ".ogg":
			req.Header.Set("Content-Type", "audio/ogg")
		default:
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(content)))

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("HTTP PUT request failed for %s: %w", targetURL, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("HTTP upload for %s failed with status %d: %s", targetURL, resp.StatusCode, string(respBody))
		}

		slog.Debug("HTTP file upload successful", "url", targetURL)
		return nil
	})
}

func (s *StorageService) uploadLocal(ctx context.Context, cfg *types.OutputLocal, sourceDir string) error {
	destPath := cfg.Path
	slog.Info("Moving directory to local destination", "source", sourceDir, "destination", destPath)

	// Ensure the parent directory of the destination exists.
	parentDir := filepath.Dir(destPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create parent directory for local output %s: %w", parentDir, err)
	}

	// Before moving, remove the destination if it already exists to prevent 'destination not empty' errors.
	if _, err := os.Stat(destPath); err == nil {
		slog.Warn("Destination path already exists, removing it before move.", "path", destPath)
		if err := os.RemoveAll(destPath); err != nil {
			return fmt.Errorf("failed to remove existing local destination %s: %w", destPath, err)
		}
	}

	// Rename is atomic on the same filesystem   faster than copying a whole workspace
	if err := os.Rename(sourceDir, destPath); err != nil {
		return fmt.Errorf("failed to move workspace output %s to local destination %s: %w", sourceDir, destPath, err)
	}

	slog.Info("Local output move complete", "destination", destPath)
	return nil
}

func (s *StorageService) getAWSCfg(ctx context.Context, provider, region string, creds *types.AWSCredentials) (aws.Config, error) {
	if creds != nil {
		staticCreds := credentials.NewStaticCredentialsProvider(creds.AccessKeyID, creds.SecretAccessKey, "")
		return config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithCredentialsProvider(staticCreds))
	}
	storedCreds, _ := s.credStore.Get(ctx, provider)
	if storedCreds["access_key_id"] != "" {
		staticCreds := credentials.NewStaticCredentialsProvider(storedCreds["access_key_id"], storedCreds["secret_access_key"], "")
		return config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithCredentialsProvider(staticCreds))
	}
	return config.LoadDefaultConfig(ctx, config.WithRegion(region))
}

func (s *StorageService) getR2Cfg(ctx context.Context, endpointURL string, creds *types.R2Credentials) (aws.Config, error) {
	var awsCreds *types.AWSCredentials
	if creds != nil {
		awsCreds = (*types.AWSCredentials)(creds)
	}
	cfg, err := s.getAWSCfg(ctx, "r2", "auto", awsCreds)
	if err != nil {
		return cfg, err
	}
	// R2 isnt real S3   need the custom endpoint on the config
	cfg.BaseEndpoint = aws.String(endpointURL)
	return cfg, nil
}

func (s *StorageService) getGCSOpts(ctx context.Context, creds *types.GCSCredentials) ([]option.ClientOption, error) {
	if creds != nil && creds.ServiceAccountJSON != "" {
		return []option.ClientOption{option.WithCredentialsJSON([]byte(creds.ServiceAccountJSON))}, nil
	}
	storedCreds, _ := s.credStore.Get(ctx, "gcs")
	if storedCreds["service_account_json"] != "" {
		return []option.ClientOption{option.WithCredentialsJSON([]byte(storedCreds["service_account_json"]))}, nil
	}
	return nil, nil // fall back to ADC when nothing is stored
}

func (s *StorageService) getAzureClient(ctx context.Context) (*azblob.Client, error) {
	storedCreds, _ := s.credStore.Get(ctx, "azure")
	if storedCreds["sas_token"] != "" {
		return azblob.NewClientWithNoCredential(storedCreds["sas_token"], nil)
	}
	return nil, fmt.Errorf("no valid Azure credentials provided (SAS token required)")
}

func (s *StorageService) getSFTPClient(ctx context.Context, creds *types.SFTPCredentials) (*sftp.Client, error) {
	var auth ssh.AuthMethod

	if creds.Password != "" {
		auth = ssh.Password(creds.Password)
	} else if creds.PrivateKey != "" {
		signer, err := ssh.ParsePrivateKey([]byte(creds.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("failed to parse private key: %w", err)
		}
		auth = ssh.PublicKeys(signer)
	} else {
		return nil, fmt.Errorf("no password or private key provided for SFTP")
	}

	config := &ssh.ClientConfig{
		User: creds.User,
		Auth: []ssh.AuthMethod{
			auth,
		},
		HostKeyCallback: func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			// TODO: proper host key pinning   right now we skip verification for customer SFTP endpoints
			return nil
		},
		Timeout: 30 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", creds.Host, creds.Port)
	conn, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("failed to dial: %w", err)
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		return nil, fmt.Errorf("failed to create new SFTP client: %w", err)
	}

	return client, nil
}

// uploadDirectory walks root and calls uploadFunc for every file (dirs are skipped).
func (s *StorageService) uploadDirectory(ctx context.Context, root string, uploadFunc func(filePath string, file *os.File) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			slog.Info("Uploading file", "path", path)
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if err := uploadFunc(path, file); err != nil {
				return fmt.Errorf("failed to upload %s: %w", path, err)
			}
		}
		return nil
	})
}
