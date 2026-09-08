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
}
