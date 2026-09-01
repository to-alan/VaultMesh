package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

const (
	maxControlPlaneResponse = 4 << 20
	maxDiscardResponse      = 4 << 10
)

var (
	ErrNotModified = errors.New("configuration not modified")
	errRedirect    = errors.New("control plane redirects are not allowed")
)

// ControlPlaneError preserves the stable error code returned by the API so
// callers can make retry decisions without matching human-readable messages.
type ControlPlaneError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ControlPlaneError) Error() string {
	if e == nil {
		return "control plane request failed"
	}
	if e.Code != "" && e.Message != "" {
		return fmt.Sprintf("control plane returned HTTP %d (%s): %s", e.StatusCode, e.Code, e.Message)
	}
	if e.Code != "" {
		return fmt.Sprintf("control plane returned HTTP %d (%s)", e.StatusCode, e.Code)
	}
	return fmt.Sprintf("control plane returned HTTP %d", e.StatusCode)
}

// IsPermanentReportError returns true only for application-level rejections
// that cannot become valid by retrying the same immutable run report. Network
// failures, authentication errors, rate limits, server errors, clock skew and
// unstructured proxy responses remain retryable.
func IsPermanentReportError(err error) bool {
	var controlError *ControlPlaneError
	if !errors.As(err, &controlError) {
		return false
	}
	switch controlError.Code {
	case "validation_failed":
		// Older Control Planes used the generic validation code for clock skew.
		// Preserve retryability during rolling upgrades; current servers return
		// the stable agent_clock_ahead code instead.
		return !strings.Contains(strings.ToLower(controlError.Message), "too far in the future")
	case "invalid_json", "snapshot_inventory_too_large", "conflict", "not_found":
		return true
	default:
		return false
	}
}

type Client struct {
	baseURL string
	client  *http.Client
	version string
}

func NewClient(baseURL, version string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("control plane URL is invalid")
	}
	if parsed.EscapedPath() != "" && parsed.EscapedPath() != "/" {
		return nil, errors.New("control plane URL must not contain a path")
	}
	host := parsed.Hostname()
	isLoopback := host == "localhost"
	if ip := net.ParseIP(host); ip != nil {
		isLoopback = ip.IsLoopback()
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback) {
		return nil, errors.New("control plane URL must use HTTPS; HTTP is allowed only on localhost")
	}
	return &Client{
		baseURL: baseURL,
		version: version,
		client: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errRedirect },
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}, nil
}

func (c *Client) Enroll(ctx context.Context, enrollmentToken string, info domain.AgentInfo) (domain.AgentIdentity, error) {
	payload := struct {
		EnrollmentToken string `json:"enrollment_token"`
		domain.AgentInfo
	}{EnrollmentToken: enrollmentToken, AgentInfo: info}
	var identity domain.AgentIdentity
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/enroll", "", payload, &identity); err != nil {
		return domain.AgentIdentity{}, err
	}
	return identity, nil
}

func (c *Client) Heartbeat(ctx context.Context, token string, heartbeat domain.Heartbeat) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", token, heartbeat, nil)
}

// Sync sends a heartbeat and receives the desired configuration in the same
// request whenever the agent lags behind the control-plane revision. The
// returned flag reports whether the response carried a configuration.
func (c *Client) Sync(ctx context.Context, token string, heartbeat domain.Heartbeat) (domain.AgentConfig, bool, error) {
	var payload struct {
		Config *domain.AgentConfig `json:"config"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent/heartbeat", token, heartbeat, &payload); err != nil {
		return domain.AgentConfig{}, false, err
	}
	if payload.Config == nil {
		return domain.AgentConfig{}, false, nil
	}
	return *payload.Config, true, nil
}

func (c *Client) Config(ctx context.Context, token string, revision int64) (domain.AgentConfig, error) {
	path := "/api/v1/agent/config?after=" + strconv.FormatInt(revision, 10)
	var config domain.AgentConfig
	err := c.doJSON(ctx, http.MethodGet, path, token, nil, &config)
	if err != nil {
		return domain.AgentConfig{}, err
	}
	return config, nil
}

func (c *Client) Report(ctx context.Context, token string, report domain.RunReport) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/agent/runs", token, report, nil)
}

func (c *Client) ReportSnapshots(ctx context.Context, token string, projectID string, snapshots []domain.Snapshot) error {
	payload := struct {
		ProjectID string            `json:"project_id"`
		Snapshots []domain.Snapshot `json:"snapshots"`
	}{ProjectID: projectID, Snapshots: snapshots}
	return c.doJSON(ctx, http.MethodPut, "/api/v1/agent/snapshots", token, payload, nil)
}

func (c *Client) Commands(ctx context.Context, token string) ([]domain.Command, error) {
	var response struct {
		Items []domain.Command `json:"items"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/agent/commands", token, nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
}

func (c *Client) doJSON(ctx context.Context, method, path, token string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "vaultmesh-agent/"+c.version)
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("request control plane: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return ErrNotModified
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(limited, &envelope) == nil && envelope.Error.Message != "" {
			return &ControlPlaneError{
				StatusCode: response.StatusCode,
				Code:       envelope.Error.Code,
				Message:    envelope.Error.Message,
			}
		}
		return &ControlPlaneError{StatusCode: response.StatusCode}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxDiscardResponse))
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil {
		return fmt.Errorf("read control plane response: %w", err)
	}
	if len(data) > maxControlPlaneResponse {
		return errors.New("control plane response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("response contains multiple JSON values")
		}
		return err
	}
	return nil
}
