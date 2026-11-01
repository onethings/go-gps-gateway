package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"time"

	"traccar-server_go/go_server/internal/config"
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
	mailer schedule.Mailer, logf func(string, ...any),
) *schedule.Scheduler {
	scheduler := schedule.New(schedule.WithLogger(logf))
	if store == nil {
		return scheduler
	}

	if settings.Expiration > 0 {
		scheduler.Add(&schedule.Expiration{Store: store}, settings.Expiration, true)
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
	read(config.ScheduleCleanup, &settings.Cleanup)
	read(config.ScheduleActionRetention, &settings.ActionRetention)
	read(config.ScheduleReports, &settings.Reports)
	read(config.ScheduleHealth, &settings.Health)
	return settings
}

// schedulerSettings is the resolved task intervals.
type schedulerSettings struct {
	Expiration      time.Duration
	Cleanup         time.Duration
	ActionRetention time.Duration
	Reports         time.Duration
	Health          time.Duration
}

// schedulerStore is what every task needs together. The postgres store satisfies
// it, a test supplies the fakes it cares about.
type schedulerStore interface {
	schedule.ExpirationStore
	schedule.CleanupStore
	schedule.ReportStore
	schedule.Pinger
}
