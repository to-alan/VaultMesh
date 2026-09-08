package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/to-alan/vaultmesh/internal/domain"
)

func TestDetectEnrichesContainersWithPortsAndMounts(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	// A fake docker CLI whose inspect output mirrors the real one: dozens of
	// fields the detection struct does not model. A strict decoder would
	// reject it and silently strip ports/mounts.
	inspectJSON := `[{
	  "Id":"abc123","Created":"2026-01-01T00:00:00Z","Path":"docker-entrypoint.sh",
	  "Args":[],"State":{"Status":"running","Running":true,"Pid":42},
	  "Image":"sha256:deadbeef","Name":"/mysql-main","RestartCount":0,
	  "Platform":"linux","MountLabels":{},"Config":{"Hostname":"h","Image":"mysql:8.4.9","Labels":{}},
	  "NetworkSettings":{"Ports":{"3306/tcp":[{"HostIp":"0.0.0.0","HostPort":"3306"}]},"Networks":{}},
	  "Mounts":[{"Type":"volume","Name":"mysql-data","Source":"/var/lib/docker/volumes/mysql-data/_data","Destination":"/var/lib/mysql","Mode":"z","RW":true,"Propagation":""}]
	}]`

	bin := t.TempDir()
	script := filepath.Join(bin, "docker")
	contents := "#!/bin/sh\n" +
		"if [ \"$1\" = inspect ]; then printf '%s' '" + strings.ReplaceAll(inspectJSON, "'", "'\\''") + "'; exit 0; fi\n" +
		"if [ \"$1\" = ps ]; then printf 'mysql-main\tmysql:8.4.9\trunning\n'; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}

	runner := NewRunnerWithTools(filepath.Join(bin, "restic"), filepath.Join(bin, "mysqldump"), filepath.Join(bin, "pg_dump"), script, "")
	result := runner.Detect(context.Background(), "cmd_test")
	if result.Status != domain.RunSucceeded || result.DetectionReport == nil {
		t.Fatalf("detect failed: %+v", result)
	}
	containers := result.DetectionReport.Containers
	if len(containers) != 1 {
		t.Fatalf("expected 1 container, got %+v", containers)
	}
	if len(containers[0].Mounts) != 1 || containers[0].Mounts[0] != "/var/lib/docker/volumes/mysql-data/_data" {
		t.Fatalf("mounts were not enriched: %+v", containers[0])
	}
	if len(containers[0].Ports) != 1 || containers[0].Ports[0] != "3306/tcp" {
		t.Fatalf("ports were not enriched: %+v", containers[0])
	}
	if len(containers[0].PortBindings) != 1 {
		t.Fatalf("port bindings were not enriched: %+v", containers[0])
	}
	binding := containers[0].PortBindings[0]
	if binding.ContainerPort != 3306 || binding.Protocol != "tcp" || binding.HostIP != "0.0.0.0" || binding.HostPort != 3306 {
		t.Fatalf("unexpected port binding: %+v", binding)
	}
}

func TestDetectedDatabaseEndpointUsesPublishedHostPort(t *testing.T) {
	container := domain.DetectedContainer{
		Name: "mysql-main",
		PortBindings: []domain.DetectedPortBinding{
			{ContainerPort: 33060, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 33060},
			{ContainerPort: 3306, Protocol: "tcp", HostIP: "0.0.0.0", HostPort: 13306},
		},
	}
	var probedHost string
	var probedPort int
	host, port, reachable := detectedDatabaseEndpoint(container, 3306, func(host string, port int) bool {
		probedHost, probedPort = host, port
		return host == "127.0.0.1" && port == 13306
	})
	if !reachable || host != "127.0.0.1" || port != 13306 {
		t.Fatalf("published endpoint was not selected: host=%q port=%d reachable=%v", host, port, reachable)
	}
	if probedHost != host || probedPort != port {
		t.Fatalf("wrong endpoint was probed: host=%q port=%d", probedHost, probedPort)
	}
}

func TestDetectedDatabaseEndpointDoesNotGuessFromExposedPort(t *testing.T) {
	container := domain.DetectedContainer{Name: "mysql-internal", Ports: []string{"3306/tcp"}}
	probed := false
	host, port, reachable := detectedDatabaseEndpoint(container, 3306, func(string, int) bool {
		probed = true
		return true
	})
	if reachable || port != 0 || host != "127.0.0.1" {
		t.Fatalf("unpublished port produced an endpoint: host=%q port=%d reachable=%v", host, port, reachable)
	}
	if probed {
		t.Fatal("an exposed-only container port must not be probed on host loopback")
	}
}

func TestDetectContainersDoesNotHideDataServicesByName(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "docker")
	contents := "#!/bin/sh\n" +
		"if [ \"$1\" = ps ]; then printf 'redis-cache\\tredis:7\\trunning\\nvaultmesh-worker\\tcustom/vaultmesh-worker:latest\\trunning\\n'; exit 0; fi\n" +
		"if [ \"$1\" = inspect ]; then printf '[{\"NetworkSettings\":{\"Ports\":{}},\"Mounts\":[]}]'; exit 0; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(script, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}

	runner := NewRunnerWithTools("", "", "", script, "")
	containers := runner.detectContainers(context.Background())
	if len(containers) != 2 {
		t.Fatalf("data-service containers were hidden: %+v", containers)
	}
	if containers[0].Name != "redis-cache" || containers[1].Name != "vaultmesh-worker" {
		t.Fatalf("unexpected container inventory: %+v", containers)
	}
	for _, container := range containers {
		if container.ExclusionReason != "" {
			t.Fatalf("business container incorrectly excluded: %+v", container)
		}
	}
}

func TestPlatformDetectionUsesOwnershipNotNames(t *testing.T) {
	containers := []domain.DetectedContainer{
		{Name: "custom-postgres-1", Image: "postgres:17"},
		{Name: "custom-control-1", Image: "ghcr.io/to-alan/vaultmesh/vaultmesh-control:edge-test"},
		{Name: "vaultmesh-postgres-1", Image: "postgres:17"},
		{Name: "custom-worker-1", Image: "custom/worker:latest"},
		{Name: "renamed-web", Image: "private/web:latest"},
	}
	labels := []map[string]string{
		{"com.docker.compose.project": "custom", "com.docker.compose.project.working_dir": "/opt/platform", "com.docker.compose.service": "postgres"},
		{"com.docker.compose.project": "custom", "com.docker.compose.project.working_dir": "/opt/platform", "com.docker.compose.service": "control"},
		{"com.docker.compose.project": "customer", "com.docker.compose.project.working_dir": "/opt/customer", "com.docker.compose.service": "postgres"},
		{"com.docker.compose.project": "custom", "com.docker.compose.project.working_dir": "/opt/platform", "com.docker.compose.service": "worker"},
		{"io.vaultmesh.component": "web"},
	}
	roots := markPlatformContainers(containers, labels)
	for i, excluded := range []bool{true, true, false, false, true} {
		if (containers[i].ExclusionReason != "") != excluded {
			t.Fatalf("container %s exclusion mismatch: %+v", containers[i].Name, containers[i])
		}
	}
	if len(roots) != 1 || roots[0] != "/opt/platform" {
		t.Fatalf("unexpected platform roots: %v", roots)
	}
}

func TestPlatformAppDetectionKeepsSiblingApplications(t *testing.T) {
	apps := []domain.DetectedApp{{Path: "/opt/platform"}, {Path: "/opt/platform/web"}, {Path: "/opt/platform-shop"}, {Path: "/data/agent/state"}, {Path: "/srv/shop"}}
	annotateExcludedApps(apps, []string{"/opt/platform", "/"}, []string{"/data/agent"})
	for i, excluded := range []bool{true, true, false, true, false} {
		if (apps[i].ExclusionReason != "") != excluded {
			t.Fatalf("app %s exclusion mismatch: %+v", apps[i].Path, apps[i])
		}
	}
	for _, root := range []string{"/", "/opt", "/srv", "relative"} {
		if isPlatformProjectRoot(root) {
			t.Fatalf("shared or unsafe root accepted: %s", root)
		}
	}
}

func TestDatabaseDetectionDoesNotMistakeToolsForDatabases(t *testing.T) {
	for image, want := range map[string]string{
		"postgres:17-alpine": "postgresql", "bitnami/postgresql:17": "postgresql", "mysql/mysql-server:8": "mysql",
		"registry.example:5000/team/mariadb@sha256:abc": "mysql", "timescale/timescaledb:latest": "postgresql",
		"prometheus/mysqld-exporter:latest": "", "postgres-backup:latest": "", "phpmyadmin:latest": "",
	} {
		if got := detectedDatabaseKind(image); got != want {
			t.Errorf("%s: got %q, want %q", image, got, want)
		}
	}
}

func TestScanMarkersRecognizesCanonicalComposeFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	apps := scanMarkers(context.Background(), root, 0)
	if len(apps) != 1 || apps[0].Kind != "compose" {
		t.Fatalf("canonical Compose project not detected: %+v", apps)
	}
}
