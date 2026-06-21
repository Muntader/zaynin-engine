package drm

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const axinomKeyServiceURL = "https://key-server-management.axprod.net/api/WidevineProtectionInfo"

type AxinomConfig struct {
	ProviderName string `json:"name"`
	SigningKey   string `json:"signing_key"`
	SigningIV    string `json:"signing_iv"`
}

type AxinomKeyService struct {
	config AxinomConfig
	client *http.Client
}

func NewAxinomKeyService(config AxinomConfig) (*AxinomKeyService, error) {
	if config.ProviderName == "" || config.SigningKey == "" || config.SigningIV == "" {
		return nil, errors.New("axinom config requires 'name', 'signing_key', and 'signing_iv' fields")
	}

	return &AxinomKeyService{
		config: config,
		client: &http.Client{},
	}, nil
}

func (s *AxinomKeyService) FetchKeys(ctx context.Context, contentID string, drmTypes []string) (*DRMKeys, error) {
	keyRequestData := axinomKeyRequest{
		ContentID:        base64.StdEncoding.EncodeToString([]byte("CID:" + contentID)),
		DRMTypes:         drmTypes,
		Tracks:           []axinomTrack{{Type: "HD"}},
		ProtectionScheme: "CENC",
	}
	requestText, err := json.Marshal(keyRequestData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal axinom request: %w", err)
	}

	sha1Hash := sha1.Sum(requestText)
	signingKeyBin, _ := hex.DecodeString(s.config.SigningKey)
	signingIVBin, _ := hex.DecodeString(s.config.SigningIV)
	encryptedSignature, err := encryptAES256CBC(sha1Hash[:], signingKeyBin, signingIVBin)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt signature for axinom: %w", err)
	}
	signatureBase64 := base64.StdEncoding.EncodeToString(encryptedSignature)

	requestEnvelope := axinomRequestEnvelope{
		Request:   base64.StdEncoding.EncodeToString(requestText),
		Signature: signatureBase64,
		Signer:    s.config.ProviderName,
	}
	envelopeBytes, err := json.Marshal(requestEnvelope)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal axinom envelope: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", axinomKeyServiceURL, bytes.NewBuffer(envelopeBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create axinom http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("axinom api request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("axinom api request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var responseEnvelope axinomResponseEnvelope
	if err := json.Unmarshal(body, &responseEnvelope); err != nil {
		return nil, fmt.Errorf("failed to unmarshal axinom response envelope: %w", err)
	}
	decodedResponseText, err := base64.StdEncoding.DecodeString(responseEnvelope.Response)
	if err != nil {
		return nil, fmt.Errorf("failed to decode inner axinom response: %w", err)
	}

	var responseData axinomResponse
	if err := json.Unmarshal(decodedResponseText, &responseData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal final axinom response data: %w", err)
	}

	if responseData.Status != "OK" {
		return nil, fmt.Errorf("axinom API returned an error status: %s", responseData.Status)
	}
	if len(responseData.Tracks) == 0 {
		return nil, errors.New("axinom API response contained no tracks, cannot retrieve keys")
	}
	trackInfo := responseData.Tracks[0]

	keyIDBytes, err := base64.StdEncoding.DecodeString(trackInfo.KeyID)
	if err != nil {
		return nil, fmt.Errorf("failed to decode axinom key_id: %w", err)
	}
	keyBytes, err := base64.StdEncoding.DecodeString(trackInfo.Key)
	if err != nil {
		return nil, fmt.Errorf("failed to decode axinom key: %w", err)
	}
	result := &DRMKeys{
		KID:        binaryUUIDToString(keyIDBytes),
		ContentKey: hex.EncodeToString(keyBytes),
		PSSH:       make(map[string]string),
		IsFairPlay: false,
	}

	for _, pssh := range trackInfo.PSSH {
		if pssh.Data != "" {
			result.PSSH[strings.ToUpper(pssh.DRMType)] = pssh.Data
		}
	}

	if stringInSlice("FAIRPLAY", drmTypes) {
		if trackInfo.IV != "" {
			ivBytes, err := base64.StdEncoding.DecodeString(trackInfo.IV)
			if err != nil {
				return nil, fmt.Errorf("failed to decode axinom iv: %w", err)
			}
			result.IV = hex.EncodeToString(ivBytes)
		}
		result.SkdURI = trackInfo.SkdURI
		if result.SkdURI != "" && result.IV != "" {
			result.IsFairPlay = true
		}
	}

	return result, nil
}

type axinomKeyRequest struct {
	ContentID        string        `json:"content_id"`
	DRMTypes         []string      `json:"drm_types"`
	Tracks           []axinomTrack `json:"tracks"`
	ProtectionScheme string        `json:"protection_scheme"`
}
type axinomTrack struct {
	Type string `json:"type"`
}
type axinomRequestEnvelope struct {
	Request   string `json:"request"`
	Signature string `json:"signature"`
	Signer    string `json:"signer"`
}
type axinomResponseEnvelope struct {
	Response string `json:"response"`
}
type axinomResponse struct {
	Status    string                `json:"status"`
	ContentID string                `json:"content_id"`
	Tracks    []axinomResponseTrack `json:"tracks"`
}
type axinomResponseTrack struct {
	KeyID  string       `json:"key_id"`
	Key    string       `json:"key"`
	IV     string       `json:"iv"`
	SkdURI string       `json:"skd_uri"`
	PSSH   []axinomPSSH `json:"pssh"`
}
type axinomPSSH struct {
	DRMType string `json:"drm_type"`
	Data    string `json:"data"`
}

func encryptAES256CBC(plaintext, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padding := block.BlockSize() - len(plaintext)%block.BlockSize()
	paddedText := append(plaintext, bytes.Repeat([]byte{byte(padding)}, padding)...)
	ciphertext := make([]byte, len(paddedText))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, paddedText)
	return ciphertext, nil
}
func binaryUUIDToString(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
func stringInSlice(a string, list []string) bool {
	for _, b := range list {
		if strings.ToUpper(b) == strings.ToUpper(a) {
			return true
		}
	}
	return false
}
