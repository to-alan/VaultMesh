package schedule

import (
	"fmt"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

var Parser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func Validate(expression, timezone string) error {
	if _, err := Parser.Parse(expression); err != nil {
		return fmt.Errorf("invalid five-field cron expression: %w", err)
	}
	if timezone == "" {
		return fmt.Errorf("timezone is required")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return fmt.Errorf("invalid IANA timezone: %w", err)
	}
	return nil
}
