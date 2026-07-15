package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/to-alan/vaultmesh/internal/domain"
)

var allowedRepositoryEnvironment = map[string]struct{}{
	"AWS_ACCESS_KEY_ID":                {},
	"AWS_SECRET_ACCESS_KEY":            {},
	"AWS_SESSION_TOKEN":                {},
	"AWS_DEFAULT_REGION":               {},
	"AWS_REGION":                       {},
	"AWS_CA_BUNDLE":                    {},
	"RESTIC_REST_USERNAME":             {},
	"RESTIC_REST_PASSWORD":             {},
	"B2_ACCOUNT_ID":                    {},
	"B2_ACCOUNT_KEY":                   {},
	"AZURE_ACCOUNT_NAME":               {},
	"AZURE_ACCOUNT_KEY":                {},
	"AZURE_ACCOUNT_SAS":                {},
	"AZURE_ENDPOINT_SUFFIX":            {},
	"AZURE_FORCE_CLI_CREDENTIAL":       {},
	"GOOGLE_PROJECT_ID":                {},
	"GOOGLE_APPLICATION_CREDENTIALS":   {},
	"GOOGLE_ACCESS_TOKEN":              {},
	"ST_AUTH":                          {},
	"ST_USER":                          {},
	"ST_KEY":                           {},
	"OS_AUTH_URL":                      {},
	"OS_REGION_NAME":                   {},
	"OS_USERNAME":                      {},
	"OS_PASSWORD":                      {},
	"OS_TENANT_ID":                     {},
	"OS_TENANT_NAME":                   {},
	"OS_USER_ID":                       {},
	"OS_USER_DOMAIN_NAME":              {},
	"OS_USER_DOMAIN_ID":                {},
	"OS_PROJECT_NAME":                  {},
	"OS_PROJECT_DOMAIN_NAME":           {},
	"OS_PROJECT_DOMAIN_ID":             {},
	"OS_TRUST_ID":                      {},
	"OS_APPLICATION_CREDENTIAL_ID":     {},
	"OS_APPLICATION_CREDENTIAL_NAME":   {},
	"OS_APPLICATION_CREDENTIAL_SECRET": {},
	"OS_STORAGE_URL":                   {},
	"OS_AUTH_TOKEN":                    {},
	"SWIFT_DEFAULT_CONTAINER_POLICY":   {},
}

var allowedRepositoryProviders = map[string]struct{}{
	"local": {}, "sftp": {}, "rest_server": {},
	"amazon_s3": {}, "cloudflare_r2": {}, "minio": {}, "wasabi": {},
	"alibaba_oss": {}, "tencent_cos": {}, "huawei_obs": {}, "qiniu_kodo": {},
	"backblaze_b2_s3": {}, "s3_compatible": {},
	"openstack_swift": {}, "backblaze_b2": {}, "azure_blob": {}, "google_cloud_storage": {},
	"rclone": {}, "webdav_rclone": {}, "onedrive_rclone": {}, "google_drive_rclone": {}, "dropbox_rclone": {},
}

var s3RepositoryProviders = map[string]struct{}{
	"amazon_s3": {}, "cloudflare_r2": {}, "minio": {}, "wasabi": {},
	"alibaba_oss": {}, "tencent_cos": {}, "huawei_obs": {}, "qiniu_kodo": {},
	"backblaze_b2_s3": {}, "s3_compatible": {},
}

var rcloneRepositoryProviders = map[string]struct{}{
	"rclone": {}, "webdav_rclone": {}, "onedrive_rclone": {}, "google_drive_rclone": {}, "dropbox_rclone": {},
}

var allowedRepositoryOptions = map[string]map[string]struct{}{
	"s3.bucket-lookup":  {"auto": {}, "dns": {}, "path": {}},
	"azure.access-tier": {"Hot": {}, "Cool": {}, "Cold": {}},
}

func (s *Service) CreateRepository(ctx context.Context, input domain.Repository) (domain.Repository, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	input.Provider = strings.TrimSpace(input.Provider)
	if input.Name == "" || len(input.Name) > 100 {
		return domain.Repository{}, validationError("name", "must contain 1 to 100 characters")
	}
	if input.Provider == "" {
		input.Provider = "s3_compatible"
	}
	if _, ok := allowedRepositoryProviders[input.Provider]; !ok {
		return domain.Repository{}, validationError("provider", "is not a supported Restic or rclone repository type")
	}
	if err := validateRepositoryURL(input.Provider, input.URL); err != nil {
		return domain.Repository{}, validationError("url", err.Error())
	}
	if input.Password == "" {
		return domain.Repository{}, validationError("password", "is required")
	}
	for key := range input.Environment {
		if _, ok := allowedRepositoryEnvironment[key]; !ok {
			return domain.Repository{}, validationError("environment", fmt.Sprintf("variable %q is not allowed", key))
		}
	}
	for key, value := range input.Options {
		values, ok := allowedRepositoryOptions[key]
		if !ok {
			return domain.Repository{}, validationError("options", fmt.Sprintf("option %q is not allowed", key))
		}
		if _, ok := values[value]; !ok {
			return domain.Repository{}, validationError("options", fmt.Sprintf("value %q is not valid for %s", value, key))
		}
		if key == "s3.bucket-lookup" {
			if _, ok := s3RepositoryProviders[input.Provider]; !ok {
				return domain.Repository{}, validationError("options", "s3.bucket-lookup is valid only for S3 repositories")
			}
		}
		if key == "azure.access-tier" && input.Provider != "azure_blob" {
			return domain.Repository{}, validationError("options", "azure.access-tier is valid only for Azure Blob repositories")
		}
	}
	if _, ok := s3RepositoryProviders[input.Provider]; ok {
		if lookup := input.Options["s3.bucket-lookup"]; lookup == "" {
			if input.Options == nil {
				input.Options = map[string]string{}
			}
			input.Options["s3.bucket-lookup"] = "auto"
		}
	}
	if input.Provider == "cloudflare_r2" {
		endpoint, _ := url.Parse(strings.TrimPrefix(input.URL, "s3:"))
		if endpoint == nil || !strings.HasSuffix(strings.ToLower(endpoint.Hostname()), ".r2.cloudflarestorage.com") {
			return domain.Repository{}, validationError("url", "Cloudflare R2 endpoint must end with .r2.cloudflarestorage.com")
		}
		if input.Environment["AWS_ACCESS_KEY_ID"] == "" || input.Environment["AWS_SECRET_ACCESS_KEY"] == "" {
			return domain.Repository{}, validationError("environment", "Cloudflare R2 requires AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY")
		}
		if region := input.Environment["AWS_DEFAULT_REGION"]; region != "" && region != "auto" {
			return domain.Repository{}, validationError("environment", "Cloudflare R2 region must be auto")
		}
		input.Environment["AWS_DEFAULT_REGION"] = "auto"
		input.Options["s3.bucket-lookup"] = "path"
	}
	if input.Provider == "alibaba_oss" {
		input.Options["s3.bucket-lookup"] = "dns"
	}
	if input.Provider == "backblaze_b2" && (input.Environment["B2_ACCOUNT_ID"] == "" || input.Environment["B2_ACCOUNT_KEY"] == "") {
		return domain.Repository{}, validationError("environment", "Backblaze B2 requires B2_ACCOUNT_ID and B2_ACCOUNT_KEY")
	}
	if input.Provider == "azure_blob" {
		if input.Environment["AZURE_ACCOUNT_NAME"] == "" {
			return domain.Repository{}, validationError("environment", "Azure Blob requires AZURE_ACCOUNT_NAME")
		}
		if forceCLI := input.Environment["AZURE_FORCE_CLI_CREDENTIAL"]; forceCLI != "" && forceCLI != "true" {
			return domain.Repository{}, validationError("environment", "AZURE_FORCE_CLI_CREDENTIAL must be true when specified")
		}
	}
	if input.Provider == "rest_server" && (input.Environment["RESTIC_REST_USERNAME"] == "") != (input.Environment["RESTIC_REST_PASSWORD"] == "") {
		return domain.Repository{}, validationError("environment", "REST Server username and password must be provided together")
	}
	if input.Provider == "google_cloud_storage" && input.Environment["GOOGLE_PROJECT_ID"] == "" {
		return domain.Repository{}, validationError("environment", "Google Cloud Storage requires GOOGLE_PROJECT_ID")
	}
	if input.Provider == "openstack_swift" && !validSwiftEnvironment(input.Environment) {
		return domain.Repository{}, validationError("environment", "Swift requires a complete v1, Keystone password, application credential, or storage token configuration")
	}
	secretPayload, err := json.Marshal(struct {
		Password    string            `json:"password"`
		Environment map[string]string `json:"environment"`
		Options     map[string]string `json:"options,omitempty"`
	}{Password: input.Password, Environment: input.Environment, Options: input.Options})
	if err != nil {
		return domain.Repository{}, err
	}
	sealed, err := s.sealer.Seal(secretPayload)
	if err != nil {
		return domain.Repository{}, err
	}
	id, err := randomValue("repo", 10)
	if err != nil {
		return domain.Repository{}, err
	}
	repository := domain.Repository{
		ID:               id,
		Provider:         input.Provider,
		Name:             input.Name,
		URL:              input.URL,
		SecretCiphertext: sealed,
		CreatedAt:        s.now(),
	}
	return s.store.CreateRepository(ctx, repository)
}

func validSwiftEnvironment(environment map[string]string) bool {
	if environment["ST_AUTH"] != "" && environment["ST_USER"] != "" && environment["ST_KEY"] != "" {
		return true
	}
	if environment["OS_STORAGE_URL"] != "" && environment["OS_AUTH_TOKEN"] != "" {
		return true
	}
	if environment["OS_AUTH_URL"] == "" {
		return false
	}
	if environment["OS_APPLICATION_CREDENTIAL_SECRET"] != "" {
		if environment["OS_APPLICATION_CREDENTIAL_ID"] != "" {
			return true
		}
		if environment["OS_APPLICATION_CREDENTIAL_NAME"] != "" && environment["OS_USERNAME"] != "" && environment["OS_USER_DOMAIN_NAME"] != "" {
			return true
		}
	}
	return environment["OS_USERNAME"] != "" && environment["OS_PASSWORD"] != "" &&
		(environment["OS_PROJECT_NAME"] != "" || environment["OS_TENANT_NAME"] != "" || environment["OS_TENANT_ID"] != "")
}

func validateRepositoryURL(provider, value string) error {
	if _, ok := s3RepositoryProviders[provider]; ok {
		if !strings.HasPrefix(value, "s3:") {
			return errors.New("must be a Restic S3 URL beginning with s3:")
		}
		parsed, err := url.Parse(strings.TrimPrefix(value, "s3:"))
		if err != nil || parsed.Host == "" {
			return errors.New("must contain a valid S3 endpoint and bucket path")
		}
		if parsed.Scheme != "https" && parsed.Scheme != "http" {
			return errors.New("S3 endpoint must use http or https")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("credentials, query parameters, and fragments are not allowed in repository URLs; use encrypted credential fields")
		}
		if parsed.Scheme == "http" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "minio" {
			return errors.New("plain HTTP is allowed only for local MinIO development")
		}
		if strings.Trim(parsed.Path, "/") == "" {
			return errors.New("bucket path is required")
		}
		return nil
	}
	switch provider {
	case "local":
		if !filepath.IsAbs(value) {
			return errors.New("local repository path must be absolute")
		}
	case "sftp":
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "sftp" || parsed.Host == "" || parsed.User == nil || strings.Trim(parsed.Path, "/") == "" {
			return errors.New("must use sftp://user@host:port//absolute/path syntax")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("passwords, query parameters, and fragments are not allowed in repository URLs; use encrypted credential fields")
		}
	case "rest_server":
		parsed, err := url.Parse(strings.TrimPrefix(value, "rest:"))
		if !strings.HasPrefix(value, "rest:") || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return errors.New("must be a Restic REST URL beginning with rest:https://")
		}
		if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("credentials, query parameters, and fragments are not allowed in repository URLs; use encrypted credential fields")
		}
	case "openstack_swift":
		if !regexp.MustCompile(`^swift:[^:/]+:/.*`).MatchString(value) {
			return errors.New("must use swift:container:/path syntax")
		}
	case "backblaze_b2":
		if !regexp.MustCompile(`^b2:[^:]+:.*`).MatchString(value) {
			return errors.New("must use b2:bucket:path syntax")
		}
	case "azure_blob":
		if !regexp.MustCompile(`^azure:[^:/]+:/.*`).MatchString(value) {
			return errors.New("must use azure:container:/path syntax")
		}
	case "google_cloud_storage":
		if !regexp.MustCompile(`^gs:[^:/]+:/.*`).MatchString(value) {
			return errors.New("must use gs:bucket:/path syntax")
		}
	default:
		if _, ok := rcloneRepositoryProviders[provider]; !ok || !regexp.MustCompile(`^rclone:[A-Za-z0-9_-]+:.*`).MatchString(value) {
			return errors.New("must use rclone:remote:path syntax with a preconfigured remote")
		}
	}
	return nil
}
