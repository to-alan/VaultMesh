package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func jsonUnmarshalStrict(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	return nil
}

func dialTCP(ctx context.Context, host string, port int) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 800 * time.Millisecond}
	return dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// detectLimits bound the filesystem scan so a large host cannot stall the
// agent or the control plane.
const (
	detectMaxScanRoots   = 64
	detectMaxMarkers     = 512
	detectMaxDepth       = 4
	detectScanTimeout    = 20 * time.Second
	detectTimeoutCommand = 5 * time.Second
)

// appMarkers map well-known project signatures to display metadata. Only
// file names are matched; contents are never read, so no secrets leave the
// host through detection.
var appMarkers = map[string]struct{ kind, name string }{
	"composer.json":       {"php", "PHP (Composer)"},
	"index.php":           {"php", "PHP"},
	"go.mod":              {"go", "Go"},
	"go.sum":              {"go", "Go"},
	"package.json":        {"nodejs", "Node.js"},
	"docker-compose.yml":  {"compose", "Docker Compose"},
	"docker-compose.yaml": {"compose", "Docker Compose"},
	"wp-config.php":       {"wordpress", "WordPress"},
	"requirements.txt":    {"python", "Python"},
	"pyproject.toml":      {"python", "Python"},
}

// detectRoots are the conventional locations scanned for app markers.
var detectRoots = []string{
	"/var/www",
	"/srv",
	"/opt",
	"/home",
	"/data",
	"/app",
}

type detectDocker struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
}

// Detect performs the read-only inventory. Every step degrades gracefully:
// docker missing yields no containers, scans walk a bounded set of roots,
// and port checks are best-effort connect attempts.
func (r *Runner) Detect(ctx context.Context, commandID string) RunResult {
	stats := map[string]any{"operation": "detect"}
	report := domain.DetectionReport{CommandID: commandID, GeneratedAt: time.Now().UTC()}

	report.Containers = r.detectContainers(ctx)
	report.Databases = r.detectDatabases(ctx, report.Containers)
	report.Apps = detectApps(ctx)
	report.Tools = r.detectTools(ctx)

	stats["containers"] = len(report.Containers)
	stats["databases"] = len(report.Databases)
	stats["apps"] = len(report.Apps)
	return RunResult{Status: domain.RunSucceeded, Stats: stats, DetectionReport: &report}
}

func (r *Runner) detectContainers(ctx context.Context) []domain.DetectedContainer {
	if r.dockerPath == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeoutCommand)
	defer cancel()
	output, err := exec.CommandContext(ctx, r.dockerPath, "ps", "--all", "--format", "{{.Names}}\t{{.Image}}\t{{.State}}").Output()
	if err != nil {
		return nil
	}
	containers := make([]domain.DetectedContainer, 0, 16)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			continue
		}
		if isInfrastructureContainer(parts[0], parts[1]) {
			continue
		}
		containers = append(containers, domain.DetectedContainer{
			Name:    parts[0],
			Image:   parts[1],
			Running: parts[2] == "running",
		})
	}
	// Enrich with ports and mounts via inspect, best-effort per container.
	for index := range containers {
		inspect, err := exec.CommandContext(ctx, r.dockerPath, "inspect", "--type", "container", containers[index].Name).Output()
		if err != nil || len(inspect) > 4<<20 {
			continue
		}
		var parsed []detectDocker
		if jsonUnmarshalStrict(inspect, &parsed) != nil || len(parsed) != 1 {
			continue
		}
		for port := range parsed[0].NetworkSettings.Ports {
			containers[index].Ports = append(containers[index].Ports, port)
		}
		for _, mount := range parsed[0].Mounts {
			if mount.Source != "" {
				containers[index].Mounts = append(containers[index].Mounts, mount.Source)
			}
		}
	}
	return containers
}

// infrastructurePatterns match containers and runtime directories that are
// supporting infrastructure rather than user data worth backing up: the
// VaultMesh deployment itself, shared runtimes, admin panels, and build
// tooling. Their content is either reproducible or already covered by the
// applications that use them.
var infrastructurePatterns = []string{
	"vaultmesh", "grafana", "prometheus", "portainer",
	"phpmyadmin", "adminer", "redis", "memcached", "nginx-proxy",
	"runtime/php", "runtime/node", "runtime/python", "runtime/java",
	"openresty/openresty",
}

func isInfrastructureContainer(name, image string) bool {
	haystack := strings.ToLower(name + " " + image)
	for _, pattern := range infrastructurePatterns {
		if strings.Contains(haystack, pattern) {
			return true
		}
	}
	return false
}

// isInfrastructurePath filters app directories that are runtimes or tooling
// rather than a deployable project.
func isInfrastructurePath(path string) bool {
	lower := strings.ToLower(path)
	for _, pattern := range infrastructurePatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	// Shared runtime directories (1Panel keeps per-version runtimes here).
	if strings.Contains(lower, "/runtime/") {
		return true
	}
	return false
}

// databaseSignals are probed on loopback and via listening docker ports.
var databaseSignals = []struct {
	kind string
	port int
}{
	{"mysql", 3306},
	{"postgresql", 5432},
}

func (r *Runner) detectDatabases(ctx context.Context, containers []domain.DetectedContainer) []domain.DetectedDatabase {
	reachable := func(host string, port int) bool {
		conn, err := dialTCP(ctx, host, port)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}

	databases := make([]domain.DetectedDatabase, 0, 2)
	for _, signal := range databaseSignals {
		entry := domain.DetectedDatabase{Kind: signal.kind, Source: "loopback", Host: "127.0.0.1", Port: signal.port}
		if reachable(entry.Host, signal.port) {
			entry.Reachable = true
			databases = append(databases, entry)
		}
	}

	seen := map[string]bool{}
	for _, database := range databases {
		seen[database.Kind] = true
	}

	for _, container := range containers {
		if !container.Running {
			continue
		}
		kind := ""
		switch {
		case strings.Contains(strings.ToLower(container.Image), "mysql"), strings.Contains(strings.ToLower(container.Image), "mariadb"):
			kind = "mysql"
		case strings.Contains(strings.ToLower(container.Image), "postgres"):
			kind = "postgresql"
		}
		if kind == "" {
			continue
		}
		port := 0
		for _, published := range container.Ports {
			// "0.0.0.0:3306->3306/tcp" style entries are collapsed by the ps
			// format already; inspect gave raw container ports like
			// "3306/tcp". Parse the numeric prefix.
			if numeric := strings.SplitN(published, "/", 2)[0]; numeric != "" {
				if value, err := strconv.Atoi(numeric); err == nil {
					port = value
					break
				}
			}
		}
		host := "127.0.0.1"
		reach := port != 0 && reachable(host, port)
		if seen[kind] && !reach {
			continue
		}
		seen[kind] = true
		databases = append(databases, domain.DetectedDatabase{
			Kind: kind, Source: "docker", Container: container.Name,
			Host: host, Port: port, Reachable: reach,
		})
	}

	for index := range databases {
		if databases[index].Kind == "mysql" {
			databases[index].DumpTool = toolVersion(ctx, r.mysqlDumpPath, "--version")
		} else {
			databases[index].DumpTool = toolVersion(ctx, r.pgDumpPath, "--version")
		}
	}
	return databases
}

func detectApps(ctx context.Context) []domain.DetectedApp {
	ctx, cancel := context.WithTimeout(ctx, detectScanTimeout)
	defer cancel()

	var apps []domain.DetectedApp
	scanned := 0
	for _, root := range detectRoots {
		if scanned >= detectMaxScanRoots {
			break
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		scanned++
		depth := detectMaxDepth
		if root == "/home" || root == "/opt" {
			depth = detectMaxDepth + 1
		}
		apps = append(apps, scanMarkers(ctx, root, depth)...)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Path < apps[j].Path })
	if len(apps) > detectMaxMarkers {
		apps = apps[:detectMaxMarkers]
	}
	return apps
}

func scanMarkers(ctx context.Context, root string, depth int) []domain.DetectedApp {
	if ctx.Err() != nil || depth < 0 {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var found []domain.DetectedApp
	var markers []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && name != ".git" {
			continue
		}
		if _, ok := appMarkers[name]; ok && entry.Type().IsRegular() {
			markers = append(markers, name)
		}
	}
	if len(markers) > 0 {
		kind, name := classifyApp(markers)
		found = append(found, domain.DetectedApp{Path: filepath.Clean(root), Name: name, Kind: kind, Markers: markers})
	}
	if depth == 0 {
		return found
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			break
		}
		name := entry.Name()
		if !entry.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		switch name {
		case "node_modules", "vendor", "dist", "build", "target", "__pycache__", "proc", "sys", "dev", "var/lib/docker":
			continue
		}
		child := filepath.Join(root, name)
		cleaned, pathErr := safePath(child)
		if pathErr != nil || isInfrastructurePath(cleaned) {
			continue
		}
		found = append(found, scanMarkers(ctx, cleaned, depth-1)...)
	}
	if len(found) > detectMaxMarkers {
		found = found[:detectMaxMarkers]
	}
	return found
}

func classifyApp(markers []string) (string, string) {
	for _, marker := range markers {
		if meta, ok := appMarkers[marker]; ok {
			return meta.kind, meta.name
		}
	}
	return "unknown", "未知应用"
}

func (r *Runner) detectTools(ctx context.Context) map[string]string {
	tools := map[string]string{}
	tools["restic"] = toolVersion(ctx, r.resticPath, "--version")
	tools["docker"] = toolVersion(ctx, r.dockerPath, "--version")
	return tools
}

func toolVersion(ctx context.Context, name string, args ...string) string {
	if name == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeoutCommand)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(output))
	if index := strings.IndexAny(line, "\r\n"); index >= 0 {
		line = line[:index]
	}
	if len(line) > 120 {
		line = line[:120]
	}
	return line
}

// safePath reuses the agent's static path policy for scan traversal.
func safePath(value string) (string, error) {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(cleaned) {
		return "", fmt.Errorf("path %q is not absolute", value)
	}
	for _, blocked := range []string{"/proc", "/sys", "/dev"} {
		if cleaned == blocked || strings.HasPrefix(cleaned, blocked+"/") {
			return "", fmt.Errorf("path %q is blocked", cleaned)
		}
	}
	return cleaned, nil
}

// RunDetection executes the read-only inventory scan and posts the report
// through the dedicated channel. Detection is idempotent and stateless: a
// crashed attempt is simply re-leased and re-run by the control plane.
func RunDetection(ctx context.Context, client *Client, runner *Runner, identity domain.AgentIdentity, command domain.Command, logger *slog.Logger) {
	detectCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result := runner.Detect(detectCtx, command.ID)
	if result.Status != domain.RunSucceeded || result.DetectionReport == nil {
		logger.Warn("detection scan failed", "command_id", command.ID, "error", result.ErrorMessage)
		return
	}
	if err := client.ReportDetection(ctx, identity.Token, *result.DetectionReport); err != nil {
		logger.Warn("report detection", "command_id", command.ID, "error", err)
		return
	}
	logger.Info("detection report delivered", "command_id", command.ID,
		"containers", len(result.DetectionReport.Containers),
		"databases", len(result.DetectionReport.Databases),
		"apps", len(result.DetectionReport.Apps))
}
