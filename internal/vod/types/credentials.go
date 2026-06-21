package types

// AWSCredentialsBody is the POST body shape for S3/R2 credential storage.
type AWSCredentialsBody struct {
	AccessKeyID     string `json:"access_key_id" validate:"required"`
	SecretAccessKey string `json:"secret_access_key" validate:"required"`
}

// GCSCredentialsBody is the POST body shape for GCS credential storage.
type GCSCredentialsBody struct {
	// we only check its valid JSON   parsing the SA doc fully would be overkill here
	ServiceAccountJSON string `json:"service_account_json" validate:"required,json"`
}

// AzureCredentialsBody is the POST body shape for Azure credential storage.
type AzureCredentialsBody struct {
	// SAS validation is painful; presence check is enough for now
	SASToken string `json:"sas_token" validate:"required"`
}

type SFTPCredentialsBody struct {
	User       string `json:"user" validate:"required"`
	Host       string `json:"host" validate:"required"`
	Port       int    `json:"port" validate:"required,gt=0,lte=65535"`
	Password   string `json:"password,omitempty" validate:"required_without=PrivateKey"`
	PrivateKey string `json:"private_key,omitempty" validate:"required_without=Password"`
}
