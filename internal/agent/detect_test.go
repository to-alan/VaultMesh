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
}
