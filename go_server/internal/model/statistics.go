package model

import "time"

// Statistics is one row of the statistics table: a periodic snapshot of what
// the server did. The Java server keeps the same counters in tc_statistics and
// /api/statistics reads them back.
type Statistics struct {
	ID                  int64
	CaptureTime         time.Time
	ActiveUsers         int
	ActiveDevices       int
	Requests            int
	MessagesReceived    int
	MessagesStored      int
	MailSent            int
	SmsSent             int
	GeocoderRequests    int
	GeolocationRequests int
	// Protocols counts the messages per protocol name for this snapshot.
	Protocols  map[string]int
	Attributes Attributes
}
