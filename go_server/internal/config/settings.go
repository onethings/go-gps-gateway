package config

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ProtocolSettings is one protocol server's resolved options.
type ProtocolSettings struct {
	// Name is the registry name of the protocol, e.g. gt06.
	Name string
	// Address is the listen address. An empty address disables the server,
	// which is how -gt06 "" used to turn the listener off.
	Address string
	// ReadTimeout closes idle connections after this duration; 0 keeps them
	// open.
	ReadTimeout time.Duration
	// AutoRegister creates a device row the first time an unknown identifier
	// appears.
	AutoRegister bool
	// Transport is tcp (the default), udp or tls. The protocol settings table
	// overrides it at startup and while the server runs.
	Transport string
}

// Settings is the gateway configuration with the values parsed. Every field is
// fed by one flag, whose default comes from the configuration file or the
// environment, so a flag the operator did not pass keeps the configured value.
type Settings struct {
	// Config is the layered configuration, kept for per-key lookups the typed
	// fields do not cover.
	Config *Config

	Protocols []ProtocolSettings

	DatabaseURL   string
	BatchSize     int
	BatchInterval time.Duration

	WebAddress string
	// WebPath is the traccar-web build (index.html and assets). GET serves it
	// and POST /api/server/file/{path} writes below it, matching the Java
	// server's web.path. Empty disables both.
	WebPath string

	// ServerTimeout is how long a device may stay silent before its session is
	// expired and the device declared offline.
	ServerTimeout time.Duration

	APIKey            string
	AllowAnonymous    bool
	IssueToken        string
	TokenScopes       []string
	TokenTTL          time.Duration
	LoginMaxFailures  int
	LoginLockDuration time.Duration
	// DisableShare refuses the share routes, which would otherwise mint a
	// read only token for one device or group.
	DisableShare bool

	OverspeedKnots     float64
	PowerCutVolts      float64
	OverspeedLowest    bool
	AlertRepeat        time.Duration
	GeofenceRefresh    time.Duration
	DeviceRefresh      time.Duration
	StatisticsInterval time.Duration

	// M4-1 event rule thresholds. Zero disables the rule, matching the Java
	// defaults.
	FuelDropThreshold       float64
	FuelIncreaseThreshold   float64
	ProximityEnter          float64
	ProximityExit           float64
	Unaccompanied           float64
	BehaviorAcceleration    float64
	BehaviorBraking         float64
	IgnoreDuplicateAlerts   bool
	GeofenceSegmentCrossing bool

	// AutoRegister is the flag value; AutoRegisterSet reports whether the flag
	// was passed, which decides whether it overrides a protocol section.
	AutoRegister    bool
	AutoRegisterSet bool

	CommandRetry     time.Duration
	CommandAttempts  int
	CommandQueue     int
	CommandQueueTTL  time.Duration
	CommandRetention time.Duration

	WebhookURL           string
	WebhookAuthorization string

	EmailSMTP     string
	EmailFrom     string
	EmailTo       string
	EmailUsername string
	EmailPassword string
	EmailTLS      string

	NotifyTypes   []string
	NotifyTimeout time.Duration
	NotifyQueue   int
}

// Binder registers the gateway flags and converts them once parsing is done.
type Binder struct {
	fs  *flag.FlagSet
	cfg *Config
	raw rawFlags
}

// rawFlags holds the flag text. Keeping it as text means the flag default can be
// the configured value verbatim, including "" and "0", and the -help output
// shows what the configuration file and the environment resolved to.
type rawFlags struct {
	config string

	h02, gt06, jt808, teltonika, osmand, mqtt string
	gps103, tk103, meitrack, wondex           string
	suntech, ruptela                          string
	gt02                                      string
	gps056                                    string
	gator                                     string

	dsn           string
	batchSize     string
	batchInterval string

	http           string
	webPath        string
	apiKey         string
	allowAnonymous string
	issueToken     string
	tokenScopes    string
	tokenTTL       string
	loginFailures  string
	loginLockout   string
	disableShare   string

	overspeedKnots     string
	powerCutVolts      string
	overspeedLowest    string
	alertRepeat        string
	geofenceRefresh    string
	deviceRefresh      string
	statisticsInterval string

	fuelDropThreshold     string
	fuelIncreaseThreshold string
	proximityEnter        string
	proximityExit         string
	unaccompanied         string
	behaviorAcceleration  string
	behaviorBraking       string
	ignoreDuplicateAlerts string
	segmentCrossing       string

	autoRegister string

	commandRetry     string
	commandAttempts  string
	commandQueue     string
	commandQueueTTL  string
	commandRetention string

	notifyWebhook     string
	notifyWebhookAuth string
	emailSMTP         string
	emailFrom         string
	emailTo           string
	emailUsername     string
	emailPassword     string
	emailTLS          string
	notifyTypes       string
	notifyTimeout     string
	notifyQueue       string
}

// textValue is a flag.Value that writes straight into a Settings field of the
// binder. Its String method is what the flag package shows as the default.
type textValue struct{ target *string }

func (v textValue) String() string {
	if v.target == nil {
		return ""
	}
	return *v.target
}

func (v textValue) Set(text string) error {
	*v.target = text
	return nil
}

// Bind registers the gateway flags on fs. Every default comes from cfg, so
// parsing only changes what the operator passed on the command line. path is
// the configuration file that was read; it is registered as -config for the
// -help output, and Load has already used it.
func Bind(fs *flag.FlagSet, cfg *Config, path string) *Binder {
	binder := &Binder{fs: fs, cfg: cfg}

	binder.text(&binder.raw.h02, "h02", protocolAddress(cfg, "h02"), "H02 listen address")
	binder.text(&binder.raw.gt06, "gt06", protocolAddress(cfg, "gt06"), "GT06 listen address")
	binder.text(&binder.raw.jt808, "jt808", protocolAddress(cfg, "jt808"), "JT808 listen address")
	binder.text(&binder.raw.teltonika, "teltonika", protocolAddress(cfg, "teltonika"),
		"Teltonika listen address")
	binder.text(&binder.raw.osmand, "osmand", protocolAddress(cfg, "osmand"),
		"OsmAnd HTTP listen address")
	binder.text(&binder.raw.mqtt, "mqtt", protocolAddress(cfg, "mqtt"),
		"MQTT listen address")
	binder.text(&binder.raw.gps103, "gps103", protocolAddress(cfg, "gps103"),
		"GPS103 (Coban) listen address")
	binder.text(&binder.raw.tk103, "tk103", protocolAddress(cfg, "tk103"),
		"TK103 (Xexun) listen address")
	binder.text(&binder.raw.meitrack, "meitrack", protocolAddress(cfg, "meitrack"),
		"Meitrack listen address")
	binder.text(&binder.raw.wondex, "wondex", protocolAddress(cfg, "wondex"),
		"Wondex listen address")
	binder.text(&binder.raw.suntech, "suntech", protocolAddress(cfg, "suntech"),
		"Suntech listen address")
	binder.text(&binder.raw.ruptela, "ruptela", protocolAddress(cfg, "ruptela"),
		"Ruptela listen address")
	binder.text(&binder.raw.gt02, "gt02", protocolAddress(cfg, "gt02"),
		"GT02 listen address")
	binder.text(&binder.raw.gps056, "gps056", protocolAddress(cfg, "gps056"),
		"GPS056 listen address")
	binder.text(&binder.raw.gator, "gator", protocolAddress(cfg, "gator"),
		"Gator listen address")

	binder.text(&binder.raw.dsn, "dsn", cfg.String(DatabaseURL),
		"PostgreSQL DSN (defaults to TRACCAR_DSN); empty uses an in-memory directory and a log sink")
	binder.text(&binder.raw.batchSize, "batch-size", cfg.String(DatabaseBatchSize),
		"positions per database insert")
	binder.text(&binder.raw.batchInterval, "batch-interval", cfg.String(DatabaseBatchInterval),
		"maximum delay before a batch is written")

	binder.text(&binder.raw.http, "http", cfg.String(WebAddress),
		"REST listen address; empty disables the API")
	binder.text(&binder.raw.webPath, "web-path", cfg.String(WebPath),
		"traccar-web build to serve and receive uploads; empty disables both")
	binder.text(&binder.raw.apiKey, "api-key", cfg.String(APIKey),
		"REST API key (defaults to TRACCAR_API_KEY); empty means database tokens only")
	binder.text(&binder.raw.allowAnonymous, "allow-anonymous", cfg.String(APIAllowAnonymous),
		"disable REST authentication entirely; local development only")
	binder.text(&binder.raw.issueToken, "issue-token", cfg.String(APIIssueToken),
		"issue an API token for this user email and exit (needs -dsn)")
	binder.text(&binder.raw.tokenScopes, "token-scopes", cfg.String(APITokenScopes),
		"comma separated scopes for -issue-token")
	binder.text(&binder.raw.tokenTTL, "token-ttl", cfg.String(APITokenTTL),
		"lifetime of an issued token; 0 never expires")
	binder.text(&binder.raw.loginFailures, "login-max-failures", cfg.String(APILoginMaxFailures),
		"consecutive wrong passwords that lock an account; 0 disables the lockout")
	binder.text(&binder.raw.loginLockout, "login-lock-duration", cfg.String(APILoginLockout),
		"how long a locked account refuses logins")
	binder.text(&binder.raw.disableShare, "disable-share", cfg.String(DisableShare),
		"refuse POST /api/share/* so no public share link can be created")

	binder.text(&binder.raw.overspeedKnots, "overspeed-knots", cfg.String(AlertOverspeedKnots),
		"overspeed alert threshold in knots; 0 disables the rule")
	binder.text(&binder.raw.powerCutVolts, "power-cut-volts", cfg.String(AlertPowerCutVolts),
		"power cut alert threshold in volts; 0 disables the rule")
	binder.text(&binder.raw.overspeedLowest, "overspeed-prefer-lowest", cfg.String(AlertOverspeedLowest),
		"use the lowest geofence speedLimit when several geofences contain a position (default: highest)")
	binder.text(&binder.raw.alertRepeat, "alert-repeat", cfg.String(AlertRepeat),
		"suppress repeat overspeed/powerCut alerts per device within this window; 0 disables")
	binder.text(&binder.raw.geofenceRefresh, "geofence-refresh", cfg.String(AlertGeofenceRefresh),
		"geofence reload interval; 0 loads them once")
	binder.text(&binder.raw.statisticsInterval, "statistics-interval",
		cfg.String(StatisticsInterval),
		"how often a statistics row is written; 0 disables the history")
	binder.text(&binder.raw.deviceRefresh, "device-refresh", cfg.String(AlertDeviceRefresh),
		"device attribute reload interval for per-device thresholds; 0 loads them once")

	binder.text(&binder.raw.fuelDropThreshold, "fuel-drop-threshold", cfg.String(EventFuelDropThreshold),
		"fuel level drop that raises deviceFuelDrop; 0 disables the rule")
	binder.text(&binder.raw.fuelIncreaseThreshold, "fuel-increase-threshold", cfg.String(EventFuelIncreaseThreshold),
		"fuel level increase that raises deviceFuelIncrease; 0 disables the rule")
	binder.text(&binder.raw.proximityEnter, "proximity-enter-distance", cfg.String(EventProximityEnter),
		"distance in metres that raises proximityEnter; 0 disables the rule")
	binder.text(&binder.raw.proximityExit, "proximity-exit-distance", cfg.String(EventProximityExit),
		"distance in metres that raises proximityExit; 0 disables the rule")
	binder.text(&binder.raw.unaccompanied, "unaccompanied-distance", cfg.String(EventUnaccompanied),
		"distance in metres under which a linked device counts as company; 0 disables the rule")
	binder.text(&binder.raw.behaviorAcceleration, "behavior-acceleration-threshold",
		cfg.String(EventBehaviorAcceleration),
		"hard acceleration threshold in m/s²; 0 disables the rule")
	binder.text(&binder.raw.behaviorBraking, "behavior-braking-threshold",
		cfg.String(EventBehaviorBraking),
		"hard braking threshold in m/s²; 0 disables the rule")
	binder.text(&binder.raw.ignoreDuplicateAlerts, "event-ignore-duplicates",
		cfg.String(EventIgnoreDuplicateAlerts),
		"raise an alarm event only for an alarm the previous position did not carry")
	binder.text(&binder.raw.segmentCrossing, "event-geofence-segment-crossing",
		cfg.String(EventGeofenceSegmentCrossing),
		"raise geofenceCrossed when the segment between two positions cuts a geofence")

	binder.text(&binder.raw.autoRegister, "auto-register", cfg.String(DatabaseRegisterUnknown),
		"create a device row the first time an unknown identifier appears; false drops that frame")

	binder.text(&binder.raw.commandRetry, "command-retry", cfg.String(CommandRetry),
		"how long a written command waits for the device answer before it is sent again")
	binder.text(&binder.raw.commandAttempts, "command-attempts", cfg.String(CommandAttempts),
		"how often a command payload may be written before it is marked failed")
	binder.text(&binder.raw.commandQueue, "command-queue", cfg.String(CommandQueue),
		"undelivered commands one device may hold; 0 falls back to the default")
	binder.text(&binder.raw.commandQueueTTL, "command-queue-ttl", cfg.String(CommandQueueTTL),
		"how long a command may wait for its device to reconnect")
	binder.text(&binder.raw.commandRetention, "command-retention", cfg.String(CommandRetention),
		"delete command history older than this; 0 keeps it forever")

	binder.text(&binder.raw.notifyWebhook, "notify-webhook", cfg.String(NotifyWebhook),
		"webhook URL that receives alert JSON; empty disables the webhook channel")
	binder.text(&binder.raw.notifyWebhookAuth, "notify-webhook-authorization",
		cfg.String(NotifyWebhookAuth), "Authorization header sent with every webhook request")
	binder.text(&binder.raw.emailSMTP, "notify-email-smtp", cfg.String(MailSMTPHost),
		"SMTP host:port for alert email; empty disables the mail channel")
	binder.text(&binder.raw.emailFrom, "notify-email-from", cfg.String(MailSMTPFrom),
		"sender address of alert email")
	binder.text(&binder.raw.emailTo, "notify-email-to", cfg.String(MailTo),
		"default alert email recipients, comma separated; empty uses only per-device notifyEmail")
	binder.text(&binder.raw.emailUsername, "notify-email-username", cfg.String(MailSMTPUsername),
		"SMTP username; empty disables AUTH")
	binder.text(&binder.raw.emailPassword, "notify-email-password", cfg.String(MailSMTPPassword),
		"SMTP password (defaults to TRACCAR_SMTP_PASSWORD)")
	binder.text(&binder.raw.emailTLS, "notify-email-tls", cfg.String(MailSMTPTLS),
		"SMTP TLS mode: auto, starttls, implicit or none")
	binder.text(&binder.raw.notifyTypes, "notify-types", cfg.String(NotifyTypes),
		"comma separated alert types to deliver; empty delivers every type")
	binder.text(&binder.raw.notifyTimeout, "notify-timeout", cfg.String(NotifyTimeout),
		"timeout of one device lookup or notification send")
	binder.text(&binder.raw.notifyQueue, "notify-queue", cfg.String(NotifyQueue),
		"alerts that may wait for notification delivery")

	// -config is resolved before the flags are registered, because the file has
	// to be read to know the other defaults. Registering it keeps -help honest.
	binder.text(&binder.raw.config, "config", path,
		"configuration file read before the flags are parsed")

	return binder
}

// text registers one flag whose default is the configured value.
func (b *Binder) text(target *string, name, value, usage string) {
	*target = value
	b.fs.Var(textValue{target}, name, usage)
}

// protocolAddress is the configured listen address of one protocol, falling
// back to the address the gateway shipped with.
func protocolAddress(cfg *Config, name string) string {
	address, _ := cfg.Protocol(name).Address()
	return address
}

// Settings converts the parsed flags. Every conversion problem is reported at
// once, so a configuration file with two typos does not take two restarts.
func (b *Binder) Settings() (Settings, error) {
	var errs []error

	settings := Settings{Config: b.cfg}

	toInt := func(label, text string) int {
		value, err := parseInt(label, text)
		if err != nil {
			errs = append(errs, err)
		}
		return value
	}
	toDuration := func(label, text string) time.Duration {
		value, err := ParseDuration(text)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			return 0
		}
		return value
	}
	toFloat := func(label, text string) float64 {
		value, err := parseFloat(label, text)
		if err != nil {
			errs = append(errs, err)
		}
		return value
	}
	toBool := func(label, text string) bool {
		if strings.TrimSpace(text) == "" {
			return false
		}
		value, err := parseBool(text)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", label, err))
			return false
		}
		return value
	}

	raw := b.raw

	settings.DatabaseURL = raw.dsn
	settings.BatchSize = toInt("batch-size", raw.batchSize)
	settings.BatchInterval = toDuration("batch-interval", raw.batchInterval)

	settings.WebAddress = raw.http
	settings.WebPath = raw.webPath

	timeout, err := b.cfg.Duration(ServerTimeout)
	if err != nil {
		errs = append(errs, err)
	}
	settings.ServerTimeout = timeout

	settings.APIKey = raw.apiKey
	settings.AllowAnonymous = toBool("allow-anonymous", raw.allowAnonymous)
	settings.IssueToken = raw.issueToken
	settings.TokenScopes = SplitList(raw.tokenScopes)
	settings.TokenTTL = toDuration("token-ttl", raw.tokenTTL)
	settings.LoginMaxFailures = toInt("login-max-failures", raw.loginFailures)
	settings.LoginLockDuration = toDuration("login-lock-duration", raw.loginLockout)
	settings.DisableShare = toBool("disable-share", raw.disableShare)

	settings.OverspeedKnots = toFloat("overspeed-knots", raw.overspeedKnots)
	settings.PowerCutVolts = toFloat("power-cut-volts", raw.powerCutVolts)
	settings.OverspeedLowest = toBool("overspeed-prefer-lowest", raw.overspeedLowest)
	settings.AlertRepeat = toDuration("alert-repeat", raw.alertRepeat)
	settings.GeofenceRefresh = toDuration("geofence-refresh", raw.geofenceRefresh)
	settings.DeviceRefresh = toDuration("device-refresh", raw.deviceRefresh)
	settings.StatisticsInterval = toDuration("statistics-interval", raw.statisticsInterval)

	settings.FuelDropThreshold = toFloat("fuel-drop-threshold", raw.fuelDropThreshold)
	settings.FuelIncreaseThreshold = toFloat("fuel-increase-threshold", raw.fuelIncreaseThreshold)
	settings.ProximityEnter = toFloat("proximity-enter-distance", raw.proximityEnter)
	settings.ProximityExit = toFloat("proximity-exit-distance", raw.proximityExit)
	settings.Unaccompanied = toFloat("unaccompanied-distance", raw.unaccompanied)
	settings.BehaviorAcceleration = toFloat("behavior-acceleration-threshold", raw.behaviorAcceleration)
	settings.BehaviorBraking = toFloat("behavior-braking-threshold", raw.behaviorBraking)
	settings.IgnoreDuplicateAlerts = toBool("event-ignore-duplicates", raw.ignoreDuplicateAlerts)
	settings.GeofenceSegmentCrossing = toBool("event-geofence-segment-crossing", raw.segmentCrossing)

	settings.AutoRegister = toBool("auto-register", raw.autoRegister)
	settings.AutoRegisterSet = b.wasSet("auto-register")

	settings.CommandRetry = toDuration("command-retry", raw.commandRetry)
	settings.CommandAttempts = toInt("command-attempts", raw.commandAttempts)
	settings.CommandQueue = toInt("command-queue", raw.commandQueue)
	settings.CommandQueueTTL = toDuration("command-queue-ttl", raw.commandQueueTTL)
	settings.CommandRetention = toDuration("command-retention", raw.commandRetention)

	settings.WebhookURL = raw.notifyWebhook
	settings.WebhookAuthorization = raw.notifyWebhookAuth
	settings.EmailSMTP = raw.emailSMTP
	settings.EmailFrom = raw.emailFrom
	settings.EmailTo = raw.emailTo
	settings.EmailUsername = raw.emailUsername
	settings.EmailPassword = raw.emailPassword
	settings.EmailTLS = raw.emailTLS
	settings.NotifyTypes = SplitList(raw.notifyTypes)
	settings.NotifyTimeout = toDuration("notify-timeout", raw.notifyTimeout)
	settings.NotifyQueue = toInt("notify-queue", raw.notifyQueue)

	protocols, protocolErrs := b.protocols(raw, settings)
	settings.Protocols = protocols
	errs = append(errs, protocolErrs...)

	return settings, errors.Join(errs...)
}

// protocols resolves every protocol server. A section may turn auto-register on
// or off for one protocol, but an -auto-register on the command line wins over
// the whole file, which is what the flag always did.
func (b *Binder) protocols(raw rawFlags, settings Settings) ([]ProtocolSettings, []error) {
	addresses := map[string]string{
		"h02":       raw.h02,
		"gt06":      raw.gt06,
		"jt808":     raw.jt808,
		"teltonika": raw.teltonika,
		"osmand":    raw.osmand,
		"mqtt":      raw.mqtt,
		"gps103":    raw.gps103,
		"tk103":     raw.tk103,
		"meitrack":  raw.meitrack,
		"wondex":    raw.wondex,
		"suntech":   raw.suntech,
		"ruptela":   raw.ruptela,
		"gt02":      raw.gt02,
		"gps056":    raw.gps056,
		"gator":     raw.gator,
	}

	var (
		errs     []error
		resolved = make([]ProtocolSettings, 0, len(Protocols))
	)
	for _, name := range Protocols {
		protocol := b.cfg.Protocol(name)

		autoRegister := settings.AutoRegister
		if !settings.AutoRegisterSet {
			value, set, err := protocol.Bool(ProtocolRegisterUnknown)
			if err != nil {
				errs = append(errs, err)
			} else if set {
				autoRegister = value
			}
		}

		timeout, err := protocol.Duration(ProtocolTimeout)
		if err != nil {
			errs = append(errs, fmt.Errorf("protocol.%s.%s: %w", name, ProtocolTimeout, err))
		}

		transport := strings.ToLower(strings.TrimSpace(protocol.String(ProtocolTransport)))
		if transport == "" {
			transport = defaultProtocolTransports[name]
		}
		if transport == "" {
			transport = "tcp"
		}

		resolved = append(resolved, ProtocolSettings{
			Name:         name,
			Address:      addresses[name],
			ReadTimeout:  timeout,
			AutoRegister: autoRegister,
			Transport:    transport,
		})
	}
	return resolved, errs
}

// wasSet reports whether the operator passed the named flag.
func (b *Binder) wasSet(name string) bool {
	set := false
	b.fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}

func parseInt(label, text string) (int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(text)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", label, text)
	}
	return value, nil
}

func parseFloat(label, text string) (float64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number", label, text)
	}
	return value, nil
}
