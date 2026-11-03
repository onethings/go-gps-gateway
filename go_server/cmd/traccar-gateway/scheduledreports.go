package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/notify"
	"traccar-server_go/go_server/internal/report"
	"traccar-server_go/go_server/internal/schedule"
)

// reportGenerator renders a scheduled report as CSV through the same service the
// REST exports use, so a mailed report and a downloaded one cannot diverge.
type reportGenerator struct {
	service *report.Service
}

// Generate implements schedule.Generator.
func (g reportGenerator) Generate(
	ctx context.Context, kind string, deviceIDs []int64, from, to time.Time,
) (string, string, []byte, error) {
	if g.service == nil {
		return "", "", nil, fmt.Errorf("reports are not enabled")
	}

	var table report.Table
	switch kind {
	case "route":
		positions, err := g.service.Route(ctx, deviceIDs, from, to)
		if err != nil {
			return "", "", nil, err
		}
		table = report.RouteTable(positions)
	case "trips":
		items, err := g.service.TripsReport(ctx, deviceIDs, from, to)
		if err != nil {
			return "", "", nil, err
		}
		table = report.TripsTable(items)
	case "stops":
		items, err := g.service.StopsReport(ctx, deviceIDs, from, to)
		if err != nil {
			return "", "", nil, err
		}
		table = report.StopsTable(items)
	case "summary":
		items, err := g.service.SummaryReport(ctx, deviceIDs, from, to, false)
		if err != nil {
			return "", "", nil, err
		}
		table = report.SummaryTable(items)
	case "events":
		items, err := g.service.EventsReport(ctx, deviceIDs, from, to, nil, nil)
		if err != nil {
			return "", "", nil, err
		}
		table = report.EventsTable(items)
	case "devices":
		items, err := g.service.DevicesReport(ctx, deviceIDs)
		if err != nil {
			return "", "", nil, err
		}
		table = report.DevicesTable(items)
	default:
		return "", "", nil, fmt.Errorf("scheduled report type %q is not supported", kind)
	}

	var buffer bytes.Buffer
	if err := report.WriteCSV(&buffer, table); err != nil {
		return "", "", nil, err
	}
	return kind + ".csv", "text/csv; charset=utf-8", buffer.Bytes(), nil
}

// buildScheduler registers the maintenance tasks the configuration enables.
func buildScheduler(
	settings schedulerSettings, store schedulerStore, generator schedule.Generator,
	mailer schedule.Mailer, onEvent func(*model.Event), logf func(string, ...any),
) *schedule.Scheduler {
	scheduler := schedule.New(schedule.WithLogger(logf))
	if store == nil {
		return scheduler
	}

	if settings.Expiration > 0 {
		scheduler.Add(&schedule.Expiration{Store: store}, settings.Expiration, true)
	}
	if settings.Temporary > 0 {
		scheduler.Add(&schedule.TemporaryCleanup{
			Store: store,
			Report: func(kind string, deleted int64) {
				logf("schedule: deleted %d expired %s", deleted, kind)
			},
		}, settings.Temporary, true)
	}
	if settings.Cleanup > 0 {
		cleanup := &schedule.Cleanup{
			Store:           store,
			ActionRetention: settings.ActionRetention,
			Report: func(kind string, deleted int64) {
				logf("schedule: deleted %d expired %s", deleted, kind)
			},
		}
		scheduler.Add(cleanup, settings.Cleanup, true)
	}
	if settings.Reports > 0 && generator != nil {
		scheduler.Add(&schedule.Reports{
			Store:     store,
			Generator: generator,
			Mailer:    mailer,
		}, settings.Reports, true)
	}
	if settings.Health > 0 {
		scheduler.Add(&schedule.Health{Pinger: store}, settings.Health, false)
	}
	if settings.Inactive > 0 {
		scheduler.Add(&schedule.DeviceInactive{
			Store:   store,
			Timeout: settings.InactiveTimeout,
			OnEvent: onEvent,
		}, settings.Inactive, false)
	}
	if settings.DeviceExpiration > 0 {
		scheduler.Add(&schedule.DeviceExpiration{Store: store}, settings.DeviceExpiration, false)
	}
	if settings.Events > 0 {
		scheduler.Add(&schedule.Retention{
			Label:  "events",
			Keep:   settings.EventRetention,
			Delete: store.DeleteEventsBefore,
			Report: func(kind string, deleted int64) {
				logf("schedule: deleted %d old %s", deleted, kind)
			},
		}, settings.Events, false)
	}
	if settings.Positions > 0 {
		scheduler.Add(&schedule.Retention{
			Label:  "positions",
			Keep:   settings.PositionRetention,
			Delete: store.DeletePositionsBefore,
			Report: func(kind string, deleted int64) {
				logf("schedule: deleted %d old %s", deleted, kind)
			},
		}, settings.Positions, false)
	}
	if settings.Commands > 0 {
		scheduler.Add(&schedule.Retention{
			Label: "commands",
			Keep:  settings.CommandRetention,
			Delete: func(ctx context.Context, before time.Time) (int64, error) {
				age := time.Since(before)
				if age <= 0 {
					return 0, nil
				}
				return store.DeleteCommandsBefore(ctx, age)
			},
			Report: func(kind string, deleted int64) {
				logf("schedule: deleted %d old %s", deleted, kind)
			},
		}, settings.Commands, false)
	}
	return scheduler
}

// scheduledReportMailer delivers a scheduled report as an email attachment.
type scheduledReportMailer struct {
	mail *notify.Mail
}

// SendReport implements schedule.Mailer.
func (m scheduledReportMailer) SendReport(
	ctx context.Context, recipients []string, subject, body, filename, contentType string, data []byte,
) error {
	return m.mail.SendAttachment(ctx, notify.Message{Subject: subject, Body: body}, recipients,
		notify.Attachment{Filename: filename, ContentType: contentType, Data: data})
}

// readSchedulerSettings turns the configuration into the task intervals. Every
// task is off unless the operator sets an interval, so a gateway that has never
// been configured does not start deleting rows.
func readSchedulerSettings(cfg *config.Config) schedulerSettings {
	settings := schedulerSettings{}
	if cfg == nil {
		return settings
	}
	read := func(key string, target *time.Duration) {
		value, err := cfg.Duration(key)
		if err != nil {
			log.Printf("schedule: %v", err)
			return
		}
		if value > 0 {
			*target = value
		}
	}
	read(config.ScheduleExpiration, &settings.Expiration)
	read(config.ScheduleTemporary, &settings.Temporary)
	read(config.ScheduleCleanup, &settings.Cleanup)
	read(config.ScheduleActionRetention, &settings.ActionRetention)
	read(config.ScheduleReports, &settings.Reports)
	read(config.ScheduleHealth, &settings.Health)
	read(config.ScheduleInactive, &settings.Inactive)
	read(config.ScheduleDeviceExpiration, &settings.DeviceExpiration)
	read(config.ScheduleEventRetention, &settings.EventRetention)
	read(config.ScheduleEvents, &settings.Events)
	read(config.SchedulePositionRetention, &settings.PositionRetention)
	read(config.SchedulePositions, &settings.Positions)
	read(config.ScheduleCommandRetention, &settings.CommandRetention)
	read(config.ScheduleCommands, &settings.Commands)
	read(config.ServerTimeout, &settings.InactiveTimeout)
	return settings
}

// schedulerSettings is the resolved task intervals.
type schedulerSettings struct {
	Expiration        time.Duration
	Temporary         time.Duration
	Cleanup           time.Duration
	ActionRetention   time.Duration
	Reports           time.Duration
	Health            time.Duration
	Inactive          time.Duration
	InactiveTimeout   time.Duration
	DeviceExpiration  time.Duration
	EventRetention    time.Duration
	Events            time.Duration
	PositionRetention time.Duration
	Positions         time.Duration
	CommandRetention  time.Duration
	Commands          time.Duration
}

// schedulerStore is what every task needs together. The storage store satisfies
// it, a test supplies the fakes it cares about.
type schedulerStore interface {
	schedule.ExpirationStore
	schedule.TemporaryStore
	schedule.CleanupStore
	schedule.ReportStore
	schedule.Pinger
	schedule.InactiveStore
	schedule.DeviceExpirationStore
	schedule.RetentionStore
}
