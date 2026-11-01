package model

// Server is the single row of server settings. It mirrors
// org.traccar.model.Server: the values an administrator can change from the
// web interface. Values that only describe the running process (version,
// storage space, which channels are enabled) are not stored here.
type Server struct {
	ID               int64
	Registration     bool
	Readonly         bool
	DeviceReadonly   bool
	LimitCommands    bool
	DisableReports   bool
	FixedEmail       bool
	ForceSettings    bool
	Announcement     string
	Map              string
	MapURL           string
	OverlayURL       string
	BingKey          string
	PoiLayer         string
	CoordinateFormat string
	Latitude         float64
	Longitude        float64
	Zoom             int
	Attributes       Attributes
}
