package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-playground/validator/v10"
	"github.com/gorilla/mux"
	"github.com/muntader/zaynin-engine/internal/vod/store"
	"github.com/muntader/zaynin-engine/internal/vod/types"
)

type CredentialsHandler struct {
	store    *store.BoltCredentialsStore
	validate *validator.Validate
}

func NewCredentialsHandler(store *store.BoltCredentialsStore) *CredentialsHandler {
	return &CredentialsHandler{
		store:    store,
		validate: validator.New(),
	}
}

func (h *CredentialsHandler) SaveCredentials(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	provider, ok := vars["provider"]
	if !ok {
		http.Error(w, "Provider is missing in URL", http.StatusBadRequest)
		return
	}

	var credsToSave map[string]interface{}
	var validationErr error

	switch provider {
	case "s3", "r2":
		var body types.AWSCredentialsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON body for AWS/R2 credentials", http.StatusBadRequest)
			return
		}
		validationErr = h.validate.Struct(body)
		credsToSave = map[string]interface{}{
			"access_key_id":     body.AccessKeyID,
			"secret_access_key": body.SecretAccessKey,
		}

	case "gcs":
		var body types.GCSCredentialsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON body for GCS credentials", http.StatusBadRequest)
			return
		}
		validationErr = h.validate.Struct(body)
		credsToSave = map[string]interface{}{
			"service_account_json": body.ServiceAccountJSON,
		}

	case "azure":
		var body types.AzureCredentialsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON body for Azure credentials", http.StatusBadRequest)
			return
		}
		validationErr = h.validate.Struct(body)
		credsToSave = map[string]interface{}{
			"sas_token": body.SASToken,
		}
	case "sftp":
		var body types.SFTPCredentialsBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON body for SFTP credentials", http.StatusBadRequest)
			return
		}
		validationErr = h.validate.Struct(body)
		credsToSave = map[string]interface{}{
			"user":        body.User,
			"host":        body.Host,
			"port":        strconv.Itoa(body.Port),
			"password":    body.Password,
			"private_key": body.PrivateKey,
		}

	default:
		http.Error(w, fmt.Sprintf("Unsupported provider for credentials: %s", provider), http.StatusBadRequest)
		return
	}

	if validationErr != nil {
		http.Error(w, "Validation failed: "+validationErr.Error(), http.StatusBadRequest)
		return
	}

	if err := h.store.Save(r.Context(), provider, credsToSave); err != nil {
		http.Error(w, "Failed to save credentials: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
