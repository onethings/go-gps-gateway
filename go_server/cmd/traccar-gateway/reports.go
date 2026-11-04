package main

import (
	"context"
	"log"
	"time"

	"traccar-server_go/go_server/internal/config"
	"traccar-server_go/go_server/internal/notify"
	"traccar-server_go/go_server/internal/report"
)

// reportConfig reads the report keys. A missing or invalid value falls back to
// the Java defaults so a report never fails because of one bad key.
func reportConfig(cfg *config.Config) report.Config {
	settings := report.DefaultConfig()
	if cfg == nil {
		return settings
	}

	// report.maxPositions is the one report key where zero is meaningful: the
	// Java server documents it as "no limit", which this layer spells as -1.
	switch value, err := cfg.Int(config.ReportMaxPositions); {
	case err != nil:
		log.Printf("report: %v", err)
	case value == 0:
		settings.MaxPositions = -1
	case value > 0:
		settings.MaxPositions = value
	}
	if value, err := cfg.Int(config.ReportTripMinDistance); err == nil && value > 0 {
		settings.TripMinDistance = float64(value)
	} else if err != nil {
		log.Printf("report: %v", err)
	}

	for _, entry := range []struct {
		key    string
		target *time.Duration
	}{
		{config.ReportPeriodLimit, &settings.PeriodLimit},
		{config.ReportTripMinDuration, &settings.TripMinDuration},
		{config.ReportTripParkingDuration, &settings.TripParkingDuration},
		{config.ReportTripNoDataDuration, &settings.TripNoDataDuration},
	} {
		value, err := cfg.Duration(entry.key)
		if err != nil {
			log.Printf("report: %v", err)
			continue
		}
		if value > 0 {
			*entry.target = value
		}
	}

	if value, err := cfg.Bool(config.ReportIgnoreOdometer); err == nil {
		settings.IgnoreOdometer = value
	} else {
		log.Printf("report: %v", err)
	}
	return settings
}

// reportMaxPositions reads report.maxPositions for the API and the exports, where
// zero means no cap. A bad value falls back to the Java default instead of
// silently removing the cap.
func reportMaxPositions(cfg *config.Config) int {
	if cfg == nil {
		return report.DefaultConfig().MaxPositions
	}
	value, err := cfg.Int(config.ReportMaxPositions)
	if err != nil {
		log.Printf("api: %v", err)
		return report.DefaultConfig().MaxPositions
	}
	return value
}

// reportMailer sends a report export as an email attachment.
type reportMailer struct {
	mail *notify.Mail
}

// SendReport implements api.ReportMailer.
func (m reportMailer) SendReport(
	ctx context.Context, recipients []string, subject, body, filename, contentType string, data []byte,
) error {
	return m.mail.SendAttachment(ctx, notify.Message{Subject: subject, Body: body}, recipients,
		notify.Attachment{Filename: filename, ContentType: contentType, Data: data})
}
