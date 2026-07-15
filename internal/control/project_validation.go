package control

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/to-alan/vaultmesh/internal/domain"
	"github.com/to-alan/vaultmesh/internal/schedule"
)

var (
	dockerContainerName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	resticSize          = regexp.MustCompile(`(?i)^[1-9][0-9]*(?:\.[0-9]+)?[kmgt]?$`)
	resticDuration      = regexp.MustCompile(`(?i)^(?:[1-9][0-9]*[ymdh])+$`)
)

func (s *Service) prepareDatabaseSource(source *domain.Source, existingSecret string) error {
	if source.Database == nil {
		return validationError("sources.database", "database configuration is required")
	}
	database := source.Database
	database.Host = strings.TrimSpace(database.Host)
	database.Username = strings.TrimSpace(database.Username)
	database.Database = strings.TrimSpace(database.Database)
	if database.Host == "" || database.Username == "" || database.Database == "" {
		return validationError("sources.database", "host, username, and database are required")
	}
	if database.Password == "" && existingSecret == "" {
		return validationError("sources.database.password", "password is required for a new database source")
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
	if database.Password == "" {
		source.SecretCiphertext = existingSecret
	} else {
		sealed, err := s.sealer.Seal([]byte(database.Password))
		if err != nil {
			return err
		}
		source.SecretCiphertext = string(sealed)
	}
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

	retention := &policy.Retention
	retention.Mode = strings.TrimSpace(retention.Mode)
	retention.KeepWithin = strings.TrimSpace(retention.KeepWithin)
	if retention.Mode == "" {
		// Existing projects predate explicit modes and already use the six GFS
		// counters, so preserving that behavior is the only safe migration.
		retention.Mode = "gfs"
	}
	counts := []int{retention.KeepLast, retention.KeepHourly, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, retention.KeepYearly}
	positive := false
	for _, count := range counts {
		if count < 0 || count > 100000 {
			return validationError("policy.retention", "keep counts must be between 0 and 100000")
		}
		positive = positive || count > 0
	}
	if retention.Enabled {
		switch retention.Mode {
		case "count":
			if retention.KeepLast <= 0 {
				return validationError("policy.retention.keep_last", "must be greater than zero in count mode")
			}
			retention.KeepHourly, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, retention.KeepYearly = 0, 0, 0, 0, 0
			retention.KeepWithin = ""
		case "smart":
			// Smart retention follows Duplicati's documented 1 week daily,
			// 4 weeks weekly and 12 months monthly policy. It has no tunable
			// counters by design.
			retention.KeepLast, retention.KeepHourly, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, retention.KeepYearly = 0, 0, 0, 0, 0, 0
			retention.KeepWithin = ""
		case "gfs":
			if !positive {
				return validationError("policy.retention", "at least one keep rule is required in GFS mode")
			}
			retention.KeepWithin = ""
		case "age":
			if !resticDuration.MatchString(retention.KeepWithin) {
				return validationError("policy.retention.keep_within", "must be a Restic duration such as 30d, 6m, or 1y")
			}
			retention.KeepLast, retention.KeepHourly, retention.KeepDaily, retention.KeepWeekly, retention.KeepMonthly, retention.KeepYearly = 0, 0, 0, 0, 0, 0
		default:
			return validationError("policy.retention.mode", "must be count, smart, gfs, or age")
		}
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

func validateMaintenancePolicy(policy *domain.ProjectPolicy) error {
	maintenance := &policy.Maintenance
	if !maintenance.Separate {
		return nil
	}
	maintenance.Timezone = strings.TrimSpace(maintenance.Timezone)
	if maintenance.Timezone == "" {
		maintenance.Timezone = "UTC"
	}
	tasks := []struct {
		enabled bool
		field   string
		value   *string
	}{
		{policy.Retention.Enabled, "policy.maintenance.retention_cron", &maintenance.RetentionCron},
		{policy.Retention.Enabled && policy.Retention.Prune, "policy.maintenance.prune_cron", &maintenance.PruneCron},
		{policy.Verification.Mode != "off", "policy.maintenance.verification_cron", &maintenance.VerificationCron},
	}
	for _, task := range tasks {
		*task.value = strings.TrimSpace(*task.value)
		if !task.enabled {
			*task.value = ""
			continue
		}
		if err := schedule.Validate(*task.value, maintenance.Timezone); err != nil {
			return validationError(task.field, err.Error())
		}
	}
	return nil
}
