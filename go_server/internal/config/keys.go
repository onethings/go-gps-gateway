package config

// Key names follow the Java server's config/Keys.java wherever the Java server
// has an equivalent, so a traccar.xml written for it stays valid here. The keys
// marked "Go only" configure something the Java server has no key for.
const (
	// Database and the position writer.
	DatabaseURL             = "database.url"
	DatabaseRegisterUnknown = "database.registerUnknown"
	DatabaseBatchSize       = "database.batchSize"     // Go only: the BatchSink
	DatabaseBatchInterval   = "database.batchInterval" // Go only: the BatchSink

	// Server side state.
	ServerTimeout = "server.timeout"

	// REST layer.
	WebAddress          = "web.address"
	APIKey              = "api.key"              // Go only
	APIAllowAnonymous   = "api.allowAnonymous"   // Go only
	APIIssueToken       = "api.issueToken"       // Go only: one shot command
	APITokenScopes      = "api.tokenScopes"      // Go only
	APITokenTTL         = "api.tokenTTL"         // Go only
	APILoginMaxFailures = "api.loginMaxFailures" // Go only
	APILoginLockout     = "api.loginLockout"     // Go only

	// Alert rules.
	AlertOverspeedKnots  = "alert.overspeedKnots"  // Go only
	AlertPowerCutVolts   = "alert.powerCutVolts"   // Go only
	AlertOverspeedLowest = "alert.overspeedLowest" // Go only
	AlertRepeat          = "alert.repeat"          // Go only
	AlertGeofenceRefresh = "alert.geofenceRefresh" // Go only
	AlertDeviceRefresh   = "alert.deviceRefresh"   // Go only

	// Command dispatch.
	CommandRetry     = "command.retry"        // Go only
	CommandAttempts  = "command.attempts"     // Go only
	CommandQueue     = "command.queue"        // Go only
	CommandQueueTTL  = "command.queueTimeout" // Go only
	CommandRetention = "command.retention"    // Go only

	// Notification.
	NotifyWebhook     = "notify.webhook"              // Go only
	NotifyWebhookAuth = "notify.webhookAuthorization" // Go only
	NotifyTypes       = "notify.types"                // Go only
	NotifyTimeout     = "notify.timeout"              // Go only
	NotifyQueue       = "notify.queue"                // Go only
	MailSMTPHost      = "mail.smtp.host"
	MailSMTPFrom      = "mail.smtp.from"
	MailSMTPUsername  = "mail.smtp.username"
	MailSMTPPassword  = "mail.smtp.password"
	MailSMTPTLS       = "mail.smtp.tls" // Go only: auto, starttls, implicit, none
	MailTo            = "mail.to"       // Go only: default recipients
)

// ConfigDefault is the key the Java server uses to chain a second file. The
// gateway reads it too: the included file is loaded first, so the including
// file wins.
const ConfigDefault = "config.default"

// KnownKeys is every global key the gateway reads. The environment layer maps
// a variable onto one of these, so a typo in a key name stays silent instead of
// being read as a value nothing uses.
var KnownKeys = []string{
	DatabaseURL,
	DatabaseRegisterUnknown,
	DatabaseBatchSize,
	DatabaseBatchInterval,

	ServerTimeout,

	WebAddress,
	APIKey,
	APIAllowAnonymous,
	APIIssueToken,
	APITokenScopes,
	APITokenTTL,
	APILoginMaxFailures,
	APILoginLockout,

	AlertOverspeedKnots,
	AlertPowerCutVolts,
	AlertOverspeedLowest,
	AlertRepeat,
	AlertGeofenceRefresh,
	AlertDeviceRefresh,

	CommandRetry,
	CommandAttempts,
	CommandQueue,
	CommandQueueTTL,
	CommandRetention,

	NotifyWebhook,
	NotifyWebhookAuth,
	NotifyTypes,
	NotifyTimeout,
	NotifyQueue,
	MailSMTPHost,
	MailSMTPFrom,
	MailSMTPUsername,
	MailSMTPPassword,
	MailSMTPTLS,
	MailTo,
}

// Keys a protocol section may set. They are resolved through Protocol, which
// lets one section change what the global configuration says.
const (
	// ProtocolAddress is the listen address of a protocol server, e.g.
	// 0.0.0.0:5023. It is the Go spelling of the Java server's per-protocol
	// port entry, which is read as well.
	ProtocolAddress = "address"
	// ProtocolPort is the Java server's per-protocol port. When only a port is
	// configured, the address becomes 0.0.0.0:<port>.
	ProtocolPort = "port"
	// ProtocolTimeout is how long an idle connection stays open.
	ProtocolTimeout = "timeout"
	// ProtocolRegisterUnknown turns the auto-register default on or off for one
	// protocol.
	ProtocolRegisterUnknown = "registerUnknown"
)

// ProtocolKeys is the set of keys Protocol resolves. Only these can be set per
// protocol, which keeps the resolution order documented and testable.
var ProtocolKeys = []string{ProtocolAddress, ProtocolPort, ProtocolTimeout, ProtocolRegisterUnknown}

// Protocols are the sections the gateway ships protocol servers for.
var Protocols = []string{"h02", "gt06", "jt808", "teltonika"}

// defaults are the compiled-in values, the lowest layer. They match the flag
// defaults the gateway had before the configuration layer existed.
var defaults = map[string]string{
	DatabaseRegisterUnknown: "true",
	DatabaseBatchSize:       "500",
	DatabaseBatchInterval:   "1s",

	ServerTimeout: "120s",

	WebAddress:          "0.0.0.0:8082",
	APITokenScopes:      "read:positions,read:devices,read:alerts",
	APITokenTTL:         "0",
	APILoginMaxFailures: "5",
	APILoginLockout:     "15m",

	AlertGeofenceRefresh: "5m",
	AlertDeviceRefresh:   "5m",

	CommandRetry:     "30s",
	CommandAttempts:  "3",
	CommandQueue:     "32",
	CommandQueueTTL:  "5m",
	CommandRetention: "168h",

	NotifyTimeout: "10s",
	NotifyQueue:   "256",
	MailSMTPTLS:   "auto",

	// protocol.<key> is the shared protocol default: a section that does not
	// set its own timeout keeps the connection open for ten minutes.
	"protocol." + ProtocolTimeout: "10m",
}

// defaultProtocolAddresses are the listen addresses the gateway used before the
// configuration layer existed. A protocol with no default has no server unless
// its section names an address.
var defaultProtocolAddresses = map[string]string{
	"h02":       "0.0.0.0:5013",
	"gt06":      "0.0.0.0:5023",
	"jt808":     "0.0.0.0:8610",
	"teltonika": "0.0.0.0:5027",
}

// legacyEnvAliases keeps the environment variables the gateway documented
// before the configuration layer existed working. The canonical spelling of a
// key is derived by EnvName.
var legacyEnvAliases = map[string]string{
	"TRACCAR_DSN":           DatabaseURL,
	"TRACCAR_API_KEY":       APIKey,
	"TRACCAR_SMTP_PASSWORD": MailSMTPPassword,
}
