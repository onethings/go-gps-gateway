package schedule

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"traccar-server_go/go_server/internal/model"
)

// Attribute keys of a scheduled report. The Java server keeps the schedule in a
// calendar row; the gateway keeps it in the report's own attributes, which the
// REST resource already stores.
const (
	// AttrDevices lists the device ids the report covers.
	AttrDevices = "devices"
	// AttrEmail lists the recipients.
	AttrEmail = "email"
	// AttrPeriod is the window the report covers, as a duration.
	AttrPeriod = "period"
	// AttrSchedule is the pause between two runs, as a duration.
	AttrSchedule = "schedule"
	// AttrLastRun is the last run stamp, written back by the task.
	AttrLastRun = "lastRun"
)

// ReportStore is the part of the storage the reports task needs.
type ReportStore interface {
	Reports(ctx context.Context, filter model.ResourceFilter) ([]*model.Report, error)
	UpdateReport(ctx context.Context, report *model.Report) error
}

// Generator renders one report into a downloadable file.
type Generator interface {
	Generate(ctx context.Context, kind string, deviceIDs []int64, from, to time.Time) (
		filename, contentType string, data []byte, err error)
}

// Mailer delivers a rendered report.
type Mailer interface {
	SendReport(ctx context.Context, recipients []string, subject, body string,
		filename, contentType string, data []byte) error
}

// Reports runs the saved report definitions that carry a schedule.
type Reports struct {
	Store     ReportStore
	Generator Generator
	Mailer    Mailer
	Now       func() time.Time
}

// Name implements Task.
func (r *Reports) Name() string { return "reports" }

// Run implements Task.
func (r *Reports) Run(ctx context.Context) error {
	if r.Store == nil || r.Generator == nil {
		return nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}

	definitions, err := r.Store.Reports(ctx, model.ResourceFilter{})
	if err != nil {
		return err
	}
	for _, definition := range definitions {
		if err := r.runOne(ctx, definition, now); err != nil {
			return err
		}
	}
	return nil
}

// runOne renders and mails one report when it is due.
func (r *Reports) runOne(ctx context.Context, definition *model.Report, now time.Time) error {
	schedule, ok := durationAttribute(definition.Attributes, AttrSchedule)
	if !ok || schedule <= 0 {
		return nil
	}
	if lastRun, ok := timeAttribute(definition.Attributes, AttrLastRun); ok &&
		now.Sub(lastRun) < schedule {
		return nil
	}

	period, ok := durationAttribute(definition.Attributes, AttrPeriod)
	if !ok || period <= 0 {
		period = schedule
	}
	from := now.Add(-period)

	filename, contentType, data, err := r.Generator.Generate(
		ctx, definition.Type, intList(definition.Attributes, AttrDevices), from, now)
	if err != nil {
		return fmt.Errorf("report %d: %w", definition.ID, err)
	}

	recipients := stringList(definition.Attributes, AttrEmail)
	if r.Mailer != nil && len(recipients) > 0 {
		subject := definition.Description
		if subject == "" {
			subject = definition.Type + " report"
		}
		if err := r.Mailer.SendReport(ctx, recipients, subject,
			"Scheduled report of "+definition.Type, filename, contentType, data); err != nil {
			return fmt.Errorf("report %d: %w", definition.ID, err)
		}
	}

	// The stamp is written back even when the export is not mailed, so a report
	// without recipients does not run on every tick.
	attributes := definition.Attributes
	if attributes == nil {
		attributes = model.Attributes{}
	}
	attributes[AttrLastRun] = now.UTC().Format(time.RFC3339)
	definition.Attributes = attributes
	return r.Store.UpdateReport(ctx, definition)
}

// durationAttribute reads a duration written as "24h" or as a number of seconds.
func durationAttribute(attributes model.Attributes, key string) (time.Duration, bool) {
	if attributes == nil {
		return 0, false
	}
	switch value := attributes[key].(type) {
	case string:
		parsed, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			seconds, secondsErr := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if secondsErr != nil {
				return 0, false
			}
			return time.Duration(seconds * float64(time.Second)), true
		}
		return parsed, true
	case float64:
		return time.Duration(value * float64(time.Second)), true
	}
	return 0, false
}

// timeAttribute reads an RFC3339 stamp.
func timeAttribute(attributes model.Attributes, key string) (time.Time, bool) {
	if attributes == nil {
		return time.Time{}, false
	}
	text, ok := attributes[key].(string)
	if !ok {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// intList reads a list of ids written as an array or as a comma separated string.
func intList(attributes model.Attributes, key string) []int64 {
	if attributes == nil {
		return nil
	}
	switch value := attributes[key].(type) {
	case []any:
		ids := make([]int64, 0, len(value))
		for _, item := range value {
			if id, ok := numberToID(item); ok {
				ids = append(ids, id)
			}
		}
		return ids
	case string:
		var ids []int64
		for _, part := range strings.Split(value, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	}
	return nil
}

func numberToID(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case string:
		id, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return id, err == nil
	}
	return 0, false
}

// stringList reads a list of strings written as an array or as a comma separated
// string.
func stringList(attributes model.Attributes, key string) []string {
	if attributes == nil {
		return nil
	}
	switch value := attributes[key].(type) {
	case []any:
		items := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				items = append(items, strings.TrimSpace(text))
			}
		}
		return items
	case string:
		var items []string
		for _, part := range strings.Split(value, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				items = append(items, trimmed)
			}
		}
		return items
	}
	return nil
}
