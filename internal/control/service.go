package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/schedule"
	"github.com/to-alan/vaultmesh/internal/secret"
	"github.com/to-alan/vaultmesh/internal/store"
)

const enrollmentTTL = 15 * time.Minute

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

var dockerContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

type Service struct {
	store  store.Store
	sealer *secret.Sealer
	now    func() time.Time
}

func NewService(dataStore store.Store, sealer *secret.Sealer) *Service {
	return &Service{store: dataStore, sealer: sealer, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) CreateServer(ctx context.Context, name string) (domain.EnrollmentResult, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return domain.EnrollmentResult{}, validationError("name", "must contain 1 to 100 characters")
	}
	id, err := randomValue("srv", 10)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	token, err := randomValue("enroll", 32)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	now := s.now()
	expiresAt := now.Add(enrollmentTTL)
	server, err := s.store.CreateServer(ctx, domain.Server{
		ID:        id,
		Name:      name,
		Status:    domain.ServerPending,
		CreatedAt: now,
	}, hashToken(token), expiresAt)
	if err != nil {
		return domain.EnrollmentResult{}, err
	}
	return domain.EnrollmentResult{Server: server, EnrollmentToken: token, ExpiresAt: expiresAt}, nil
}

func (s *Service) EnrollAgent(ctx context.Context, enrollmentToken string, info domain.AgentInfo) (domain.AgentIdentity, error) {
	enrollmentToken = strings.TrimSpace(enrollmentToken)
	if enrollmentToken == "" {
		return domain.AgentIdentity{}, store.ErrInvalidEnrollment
	}
	if strings.TrimSpace(info.Hostname) == "" {
		return domain.AgentIdentity{}, validationError("hostname", "is required")
	}
	deviceToken, err := randomValue("agent", 32)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	server, err := s.store.EnrollAgent(ctx, hashToken(enrollmentToken), hashToken(deviceToken), info)
	if err != nil {
		return domain.AgentIdentity{}, err
	}
	return domain.AgentIdentity{AgentID: server.ID, Token: deviceToken}, nil
}

func (s *Service) AuthenticateAgent(ctx context.Context, token string) (domain.Server, error) {
	if token == "" {
		return domain.Server{}, store.ErrUnauthorized
	}
	return s.store.AuthenticateAgent(ctx, hashToken(token))
}

func (s *Service) Heartbeat(ctx context.Context, serverID string, heartbeat domain.Heartbeat) error {
	return s.store.UpdateHeartbeat(ctx, serverID, heartbeat, s.now())
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

func (s *Service) CreateProject(ctx context.Context, input domain.Project) (domain.Project, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 100 {
		return domain.Project{}, validationError("name", "must contain 1 to 100 characters")
	}
	if input.ServerID == "" || input.RepositoryID == "" {
		return domain.Project{}, validationError("server_id", "server_id and repository_id are required")
	}
	if len(input.Sources) == 0 {
		return domain.Project{}, validationError("sources", "at least one source is required")
	}
	for i := range input.Sources {
		source := &input.Sources[i]
		if source.ID == "" {
			id, err := randomValue("src", 8)
			if err != nil {
				return domain.Project{}, err
			}
			source.ID = id
		}
		switch source.Type {
		case "files":
			if len(source.Paths) == 0 {
				return domain.Project{}, validationError("sources.paths", "at least one path is required")
			}
			for index, path := range source.Paths {
				cleaned, err := validateSourcePath(path)
				if err != nil {
					return domain.Project{}, validationError("sources.paths", err.Error())
				}
				source.Paths[index] = cleaned
			}
		case "mysql", "postgresql":
			if err := s.prepareDatabaseSource(source); err != nil {
				return domain.Project{}, err
			}
		case "docker":
			if err := prepareDockerSource(source); err != nil {
				return domain.Project{}, err
			}
		default:
			return domain.Project{}, validationError("sources.type", "must be files, mysql, postgresql, or docker")
		}
	}
	if input.Schedule.Timezone == "" {
		input.Schedule.Timezone = "UTC"
	}
	if input.Schedule.MissedRunPolicy == "" {
		input.Schedule.MissedRunPolicy = "skip"
	}
	if input.Schedule.ConcurrencyPolicy == "" {
		input.Schedule.ConcurrencyPolicy = "forbid"
	}
	if input.Schedule.MaxRuntimeSeconds == 0 {
		input.Schedule.MaxRuntimeSeconds = 6 * 60 * 60
	}
	if input.Schedule.JitterSeconds < 0 || input.Schedule.JitterSeconds > 3600 {
		return domain.Project{}, validationError("schedule.jitter_seconds", "must be between 0 and 3600")
	}
	if input.Schedule.MaxRuntimeSeconds < 60 || input.Schedule.MaxRuntimeSeconds > 7*24*60*60 {
		return domain.Project{}, validationError("schedule.max_runtime_seconds", "must be between 60 seconds and 7 days")
	}
	if input.Schedule.MissedRunPolicy != "skip" && input.Schedule.MissedRunPolicy != "run_once" {
		return domain.Project{}, validationError("schedule.missed_run_policy", "must be skip or run_once")
	}
	if input.Schedule.ConcurrencyPolicy != "forbid" {
		return domain.Project{}, validationError("schedule.concurrency_policy", "the current version supports only forbid")
	}
	if err := schedule.Validate(input.Schedule.Cron, input.Schedule.Timezone); err != nil {
		return domain.Project{}, validationError("schedule", err.Error())
	}
	if err := validateProjectPolicy(&input.Policy); err != nil {
		return domain.Project{}, err
	}
	id, err := randomValue("prj", 10)
	if err != nil {
		return domain.Project{}, err
	}
	now := s.now()
	input.ID = id
	input.Enabled = true
	input.CreatedAt = now
	input.UpdatedAt = now
	created, err := s.store.CreateProject(ctx, input)
	if err != nil {
		return domain.Project{}, err
	}
	return publicProject(created, s.now()), nil
}

func (s *Service) SetProjectEnabled(ctx context.Context, projectID string, enabled bool) (domain.Project, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Project{}, validationError("project_id", "is required")
	}
	project, err := s.store.SetProjectEnabled(ctx, projectID, enabled, s.now())
	if err != nil {
		return domain.Project{}, err
	}
	return publicProject(project, s.now()), nil
}

func (s *Service) DesiredConfig(ctx context.Context, serverID string) (domain.AgentConfig, error) {
	config, err := s.store.DesiredConfig(ctx, serverID)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	for index := range config.Projects {
		repository := &config.Projects[index].Repository
		plaintext, err := s.sealer.Open(repository.SecretCiphertext)
		if err != nil {
			return domain.AgentConfig{}, fmt.Errorf("decrypt repository %s: %w", repository.ID, err)
		}
		var payload struct {
			Password    string            `json:"password"`
			Environment map[string]string `json:"environment"`
			Options     map[string]string `json:"options,omitempty"`
		}
		if err := json.Unmarshal(plaintext, &payload); err != nil {
			return domain.AgentConfig{}, fmt.Errorf("decode repository secret %s: %w", repository.ID, err)
		}
		repository.Password = payload.Password
		repository.Environment = payload.Environment
		repository.Options = payload.Options
		repository.SecretCiphertext = nil
		repository.URL = strings.TrimRight(repository.URL, "/") + "/" + serverID
		for sourceIndex := range config.Projects[index].Sources {
			source := &config.Projects[index].Sources[sourceIndex]
			if source.SecretCiphertext == "" {
				continue
			}
			password, err := s.sealer.Open([]byte(source.SecretCiphertext))
			if err != nil {
				return domain.AgentConfig{}, fmt.Errorf("decrypt source %s: %w", source.ID, err)
			}
			if source.Database == nil {
				return domain.AgentConfig{}, fmt.Errorf("source %s has a secret but no database configuration", source.ID)
			}
			source.Database.Password = string(password)
			source.SecretCiphertext = ""
		}
	}
	return config, nil
}

func (s *Service) CreateManualRun(ctx context.Context, projectID string) (domain.Command, error) {
	if strings.TrimSpace(projectID) == "" {
		return domain.Command{}, validationError("project_id", "is required")
	}
	id, err := randomValue("cmd", 10)
	if err != nil {
		return domain.Command{}, err
	}
	return s.store.CreateCommand(ctx, domain.Command{
		ID:        id,
		ProjectID: projectID,
		Type:      "backup",
		CreatedAt: s.now(),
	})
}

func (s *Service) ClaimCommands(ctx context.Context, serverID string) ([]domain.Command, error) {
	now := s.now()
	return s.store.ClaimCommands(ctx, serverID, now, now.Add(2*time.Minute), 10)
}

func (s *Service) prepareDatabaseSource(source *domain.Source) error {
	if source.Database == nil {
		return validationError("sources.database", "database configuration is required")
	}
	database := source.Database
	database.Host = strings.TrimSpace(database.Host)
	database.Username = strings.TrimSpace(database.Username)
	database.Database = strings.TrimSpace(database.Database)
	if database.Host == "" || database.Username == "" || database.Database == "" || database.Password == "" {
		return validationError("sources.database", "host, username, password, and database are required")
	}
	if strings.ContainsAny(database.Host, "\r\n") || strings.ContainsAny(database.Username, "\r\n") || strings.ContainsAny(database.Database, "\r\n") {
		return validationError("sources.database", "database fields cannot contain newlines")
	}
	if strings.ContainsAny(database.Password, "\r\n") {
		return validationError("sources.database.password", "passwords containing newlines are not supported")
	}
	if database.Port == 0 {
		if source.Type == "mysql" {
			database.Port = 3306
		} else {
			database.Port = 5432
		}
	}
	if database.Port < 1 || database.Port > 65535 {
		return validationError("sources.database.port", "must be between 1 and 65535")
	}
	sealed, err := s.sealer.Seal([]byte(database.Password))
	if err != nil {
		return err
	}
	source.SecretCiphertext = string(sealed)
	database.Password = ""
	source.Paths = nil
	source.Excludes = nil
	source.Docker = nil
	return nil
}

func prepareDockerSource(source *domain.Source) error {
	if source.Docker == nil {
		return validationError("sources.docker", "Docker configuration is required")
	}
	if len(source.Docker.Containers) == 0 || len(source.Docker.Containers) > 50 {
		return validationError("sources.docker.containers", "must contain between 1 and 50 container names or IDs")
	}
	seen := make(map[string]struct{}, len(source.Docker.Containers))
	containers := make([]string, 0, len(source.Docker.Containers))
	for _, value := range source.Docker.Containers {
		value = strings.TrimSpace(value)
		if !dockerContainerName.MatchString(value) {
			return validationError("sources.docker.containers", fmt.Sprintf("container %q has an invalid name or ID", value))
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		containers = append(containers, value)
	}
	source.Docker.Containers = containers
	source.Paths = nil
	source.Excludes = nil
	source.Database = nil
	return nil
}

var resticSize = regexp.MustCompile(`(?i)^[1-9][0-9]*(?:\.[0-9]+)?[kmgt]?$`)

func validateProjectPolicy(policy *domain.ProjectPolicy) error {
	backup := &policy.Backup
	backup.ExcludeLargerThan = strings.TrimSpace(backup.ExcludeLargerThan)
	if backup.ExcludeLargerThan != "" && !resticSize.MatchString(backup.ExcludeLargerThan) {
		return validationError("policy.backup.exclude_larger_than", "must be a Restic size such as 500M or 2G")
	}
	if len(backup.ExcludeIfPresent) > 20 {
		return validationError("policy.backup.exclude_if_present", "must contain no more than 20 marker filenames")
	}
	seenMarkers := make(map[string]struct{}, len(backup.ExcludeIfPresent))
	markers := make([]string, 0, len(backup.ExcludeIfPresent))
	for _, marker := range backup.ExcludeIfPresent {
		marker = strings.TrimSpace(marker)
		if marker == "" || len(marker) > 255 || marker == "." || marker == ".." || filepath.Base(marker) != marker || strings.ContainsAny(marker, "\x00\r\n") {
			return validationError("policy.backup.exclude_if_present", fmt.Sprintf("%q is not a valid marker filename", marker))
		}
		if _, exists := seenMarkers[marker]; exists {
			continue
		}
		seenMarkers[marker] = struct{}{}
		markers = append(markers, marker)
	}
	backup.ExcludeIfPresent = markers

	retention := policy.Retention
	counts := []int{retention.KeepLast, retention.KeepHourly, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, retention.KeepYearly}
	positive := false
	for _, count := range counts {
		if count < 0 || count > 100000 {
			return validationError("policy.retention", "keep counts must be between 0 and 100000")
		}
		positive = positive || count > 0
	}
	if retention.Enabled && !positive {
		return validationError("policy.retention", "at least one keep rule is required when retention is enabled")
	}
	if retention.Prune && !retention.Enabled {
		return validationError("policy.retention.prune", "requires retention to be enabled")
	}

	verification := &policy.Verification
	verification.Mode = strings.TrimSpace(verification.Mode)
	if verification.Mode == "" {
		verification.Mode = "off"
	}
	switch verification.Mode {
	case "off", "metadata", "full":
		verification.ReadDataSubset = ""
	case "subset":
		verification.ReadDataSubset = strings.TrimSpace(verification.ReadDataSubset)
		matched, _ := regexp.MatchString(`^(?:100|[1-9]?[0-9])%$`, verification.ReadDataSubset)
		if !matched || verification.ReadDataSubset == "0%" {
			return validationError("policy.verification.read_data_subset", "must be a percentage from 1% to 100%")
		}
	default:
		return validationError("policy.verification.mode", "must be off, metadata, subset, or full")
	}
	return nil
}

func (s *Service) Store() store.Store { return s.store }

type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

func validationError(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}

func randomValue(prefix string, size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	value := strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buffer), "=")
	return prefix + "_" + strings.ToLower(value), nil
}

func hashToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
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
	case "rest_server":
		parsed, err := url.Parse(strings.TrimPrefix(value, "rest:"))
		if !strings.HasPrefix(value, "rest:") || err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return errors.New("must be a Restic REST URL beginning with rest:https://")
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

func validateSourcePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !filepath.IsAbs(value) {
		return "", fmt.Errorf("path %q must be absolute", value)
	}
	cleaned := filepath.Clean(value)
	if cleaned == string(filepath.Separator) {
		return "", errors.New("backing up the filesystem root is not allowed in the current version")
	}
	for _, forbidden := range []string{"/proc", "/sys", "/dev"} {
		if cleaned == forbidden || strings.HasPrefix(cleaned, forbidden+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is a virtual system path and is not allowed", cleaned)
		}
	}
	return cleaned, nil
}

func publicProject(project domain.Project, now time.Time) domain.Project {
	if project.Enabled {
		if location, err := time.LoadLocation(project.Schedule.Timezone); err == nil {
			if parsed, err := schedule.Parser.Parse(project.Schedule.Cron); err == nil {
				next := parsed.Next(now.In(location)).UTC()
				project.NextRunAt = &next
			}
		}
	}
	for index := range project.Sources {
		project.Sources[index].SecretCiphertext = ""
		if project.Sources[index].Database != nil {
			database := *project.Sources[index].Database
			database.Password = ""
			project.Sources[index].Database = &database
		}
	}
	return project
}
