package agent

import (
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
	"compose.yml":         {"compose", "Docker Compose"},
	"compose.yaml":        {"compose", "Docker Compose"},
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
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
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

	var platformRoots []string
	report.Containers, platformRoots = r.detectContainerInventory(ctx)
	report.Databases = r.detectDatabases(ctx, report.Containers)
	report.Apps = detectApps(ctx)
	annotateExcludedApps(report.Apps, platformRoots, r.protectedPaths)
	report.Tools = r.detectTools(ctx)

	stats["containers"] = len(report.Containers)
	stats["databases"] = len(report.Databases)
	stats["apps"] = len(report.Apps)
	return RunResult{Status: domain.RunSucceeded, Stats: stats, DetectionReport: &report}
}

func (r *Runner) detectContainers(ctx context.Context) []domain.DetectedContainer {
	containers, _ := r.detectContainerInventory(ctx)
	return containers
}

func (r *Runner) detectContainerInventory(ctx context.Context) ([]domain.DetectedContainer, []string) {
	if r.dockerPath == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, detectTimeoutCommand)
	defer cancel()
	output, err := exec.CommandContext(ctx, r.dockerPath, "ps", "--all", "--format", "{{.Names}}\t{{.Image}}\t{{.State}}").Output()
	if err != nil {
		return nil, nil
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
		containers = append(containers, domain.DetectedContainer{
			Name:    parts[0],
			Image:   parts[1],
			Running: parts[2] == "running",
		})
	}
	labels := make([]map[string]string, len(containers))
	// Enrich with ports and mounts via inspect, best-effort per container.
	for index := range containers {
		inspect, err := exec.CommandContext(ctx, r.dockerPath, "inspect", "--type", "container", containers[index].Name).Output()
		if err != nil || len(inspect) > 4<<20 {
			continue
		}
		// Docker inspect emits dozens of fields beyond this struct; a strict
		// decode would fail on every container and silently strip all port
		// and mount enrichment.
		var parsed []detectDocker
		if json.Unmarshal(inspect, &parsed) != nil || len(parsed) != 1 {
			continue
		}
		labels[index] = parsed[0].Config.Labels
		for portSpec, bindings := range parsed[0].NetworkSettings.Ports {
			containers[index].Ports = append(containers[index].Ports, portSpec)
			containerPort, protocol, ok := parseDockerPortSpec(portSpec)
			if !ok {
				continue
			}
			for _, binding := range bindings {
				hostPort, err := strconv.Atoi(binding.HostPort)
				if err != nil || hostPort < 1 || hostPort > 65535 {
					continue
				}
				containers[index].PortBindings = append(containers[index].PortBindings, domain.DetectedPortBinding{
					ContainerPort: containerPort,
					Protocol:      protocol,
					HostIP:        binding.HostIP,
					HostPort:      hostPort,
				})
			}
		}
		for _, mount := range parsed[0].Mounts {
			if mount.Source != "" {
				containers[index].Mounts = append(containers[index].Mounts, mount.Source)
			}
		}
		sort.Strings(containers[index].Ports)
		sort.Strings(containers[index].Mounts)
		sort.Slice(containers[index].PortBindings, func(i, j int) bool {
			left, right := containers[index].PortBindings[i], containers[index].PortBindings[j]
			if left.ContainerPort != right.ContainerPort {
				return left.ContainerPort < right.ContainerPort
			}
			if left.HostIP != right.HostIP {
				return left.HostIP < right.HostIP
			}
			return left.HostPort < right.HostPort
		})
	}
	return containers, markPlatformContainers(containers, labels)
}

const platformExclusionReason = "VaultMesh 自身组件；请使用专门的平台灾备方案"

// Use explicit ownership, never a container-name substring. Compose identity
// also identifies the platform's generic postgres image on older deployments.
// Labels and environment values are not forwarded in the detection report.
func markPlatformContainers(containers []domain.DetectedContainer, labels []map[string]string) []string {
	scopes := map[string]bool{}
	rootSet := map[string]bool{}
	owned := make([]bool, len(containers))
	for i, container := range containers {
		image := strings.SplitN(strings.SplitN(container.Image, "@", 2)[0], ":", 2)[0]
		component := labels[i]["io.vaultmesh.component"]
		owned[i] = component == "control" || component == "web" || component == "postgres" || component == "agent" ||
			image == "ghcr.io/to-alan/vaultmesh/vaultmesh-control" || image == "ghcr.io/to-alan/vaultmesh/vaultmesh-web" || image == "ghcr.io/to-alan/vaultmesh/vaultmesh-agent"
		if !owned[i] {
			continue
		}
		project, root := labels[i]["com.docker.compose.project"], labels[i]["com.docker.compose.project.working_dir"]
		if isPlatformProjectRoot(root) {
			root = filepath.Clean(root)
			rootSet[root] = true
			if project != "" {
				scopes[project+"\x00"+root] = true
			}
		}
	}
	for i := range containers {
		project, root := labels[i]["com.docker.compose.project"], labels[i]["com.docker.compose.project.working_dir"]
		service := labels[i]["com.docker.compose.service"]
		platformService := service == "control" || service == "web" || service == "postgres" || service == "agent"
		if owned[i] || (platformService && scopes[project+"\x00"+filepath.Clean(root)]) {
			containers[i].ExclusionReason = platformExclusionReason
		}
	}
	roots := make([]string, 0, len(rootSet))
	for root := range rootSet {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	return roots
}

func isPlatformProjectRoot(root string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
		return false
	}
	// A stack launched directly from a shared scan root does not own every
	// neighbouring application below that root.
	for _, shared := range detectRoots {
		if filepath.Clean(root) == shared {
			return false
		}
	}
	return true
}

func annotateExcludedApps(apps []domain.DetectedApp, platformRoots, protectedPaths []string) {
	for i := range apps {
		for _, root := range platformRoots {
			if pathWithinDetectionRoot(apps[i].Path, root) {
				apps[i].ExclusionReason = platformExclusionReason
			}
		}
		for _, root := range protectedPaths {
			if pathWithinDetectionRoot(apps[i].Path, root) {
				apps[i].ExclusionReason = "Agent 私有状态或凭据目录，不应作为业务数据源"
			}
		}
	}
}

func pathWithinDetectionRoot(path, root string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) == "/" {
		return false
	}
	path, root = filepath.Clean(path), filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func parseDockerPortSpec(value string) (int, string, bool) {
	parts := strings.SplitN(value, "/", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	port, err := strconv.Atoi(parts[0])
	if err != nil || port < 1 || port > 65535 {
		return 0, "", false
	}
	return port, parts[1], true
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

	for _, container := range containers {
		if !container.Running {
			continue
		}
		kind := detectedDatabaseKind(container.Image)
		if kind == "" {
			continue
		}
		expectedPort := 5432
		if kind == "mysql" {
			expectedPort = 3306
		}
		host, port, reach := detectedDatabaseEndpoint(container, expectedPort, reachable)
		databases = append(databases, domain.DetectedDatabase{
			Kind: kind, Source: "docker", Container: container.Name,
			Host: host, Port: port, Reachable: reach,
			ExclusionReason: container.ExclusionReason,
		})
	}

	// A container database that publishes a port and the loopback listener
	// on the same port are the same instance. Prefer the container entry:
	// it carries the container name, which tells the user where the data
	// lives and which compose project to restart. Two passes, because the
	// loopback probe appends before the container inspection runs.
	claimedEndpoint := map[string]bool{}
	for _, database := range databases {
		if database.Source == "docker" && database.Reachable {
			claimedEndpoint[database.Kind+"\x00"+database.Host+"\x00"+strconv.Itoa(database.Port)] = true
		}
	}
	deduped := make([]domain.DetectedDatabase, 0, len(databases))
	for _, database := range databases {
		key := database.Kind + "\x00" + database.Host + "\x00" + strconv.Itoa(database.Port)
		if database.Source == "loopback" && claimedEndpoint[key] {
			continue
		}
		deduped = append(deduped, database)
	}

	for index := range deduped {
		if deduped[index].Kind == "mysql" {
			deduped[index].DumpTool = toolVersion(ctx, r.mysqlDumpPath, "--version")
		} else {
			deduped[index].DumpTool = toolVersion(ctx, r.pgDumpPath, "--version")
		}
	}
	return deduped
}

func detectedDatabaseKind(image string) string {
	image = strings.SplitN(image, "@", 2)[0]
	image = image[strings.LastIndex(image, "/")+1:]
	image = strings.ToLower(strings.SplitN(image, ":", 2)[0])
	switch image {
	case "mysql", "mysql-server", "mariadb", "percona", "percona-server":
		return "mysql"
	case "postgres", "postgresql", "timescaledb":
		return "postgresql"
	default:
		return ""
	}
}

func detectedDatabaseEndpoint(container domain.DetectedContainer, expectedContainerPort int, reachable func(string, int) bool) (string, int, bool) {
	var fallback *domain.DetectedPortBinding
	for index := range container.PortBindings {
		binding := &container.PortBindings[index]
		if binding.ContainerPort != expectedContainerPort || binding.Protocol != "tcp" {
			continue
		}
		if fallback == nil {
			fallback = binding
		}
		host := dockerBindingHost(binding.HostIP)
		if reachable(host, binding.HostPort) {
			return host, binding.HostPort, true
		}
	}
	if fallback != nil {
		return dockerBindingHost(fallback.HostIP), fallback.HostPort, false
	}
	// An exposed container port without a host binding is not reachable from
	// the host. Do not probe the same loopback port: another local database
	// could answer and be falsely attributed to this container.
	return "127.0.0.1", 0, false
}

func dockerBindingHost(host string) string {
	switch host {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::":
		return "::1"
	default:
		return host
	}
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
		if pathErr != nil {
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
	tools["restic"] = toolVersion(ctx, r.resticPath, "version")
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
