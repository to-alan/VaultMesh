package control

import "testing"

func TestValidateRepositoryURLSupportsCanonicalResticBackends(t *testing.T) {
	tests := []struct {
		provider string
		url      string
	}{
		{"local", "/mnt/backups/vaultmesh"},
		{"sftp", "sftp://backup@example.com:22//srv/restic"},
		{"rest_server", "rest:https://backup.example.com:8000/vaultmesh"},
		{"amazon_s3", "s3:https://s3.us-east-1.amazonaws.com/backups/vaultmesh"},
		{"cloudflare_r2", "s3:https://account.r2.cloudflarestorage.com/backups/vaultmesh"},
		{"openstack_swift", "swift:backups:/vaultmesh"},
		{"backblaze_b2", "b2:backups:vaultmesh"},
		{"azure_blob", "azure:backups:/vaultmesh"},
		{"google_cloud_storage", "gs:backups:/vaultmesh"},
		{"rclone", "rclone:archive:vaultmesh"},
		{"webdav_rclone", "rclone:webdav:vaultmesh"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			if err := validateRepositoryURL(test.provider, test.url); err != nil {
				t.Fatalf("valid repository URL rejected: %v", err)
			}
		})
	}
}

func TestValidateRepositoryURLRejectsProtocolMismatch(t *testing.T) {
	tests := []struct {
		provider string
		url      string
	}{
		{"local", "relative/path"},
		{"sftp", "sftp://example.com/no-user"},
		{"rest_server", "https://backup.example.com"},
		{"amazon_s3", "s3:ftp://example.com/bucket"},
		{"amazon_s3", "s3:https://access:secret@example.com/bucket"},
		{"cloudflare_r2", "s3:https://example.com/bucket?token=secret"},
		{"sftp", "sftp://backup:password@example.com//srv/restic"},
		{"rest_server", "rest:https://user:password@backup.example.com/vaultmesh"},
		{"openstack_swift", "swift:/missing-container"},
		{"rclone", "rclone:bad remote:path"},
	}
	for _, test := range tests {
		t.Run(test.provider, func(t *testing.T) {
			if err := validateRepositoryURL(test.provider, test.url); err == nil {
				t.Fatalf("invalid repository URL accepted: %s", test.url)
			}
		})
	}
}
