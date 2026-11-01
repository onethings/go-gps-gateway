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
	WebPath             = "web.path"
	APIKey              = "api.key"              // Go only
	APIAllowAnonymous   = "api.allowAnonymous"   // Go only
	APIIssueToken       = "api.issueToken"       // Go only: one shot command
	APITokenScopes      = "api.tokenScopes"      // Go only
	APITokenTTL         = "api.tokenTTL"         // Go only
	APILoginMaxFailures = "api.loginMaxFailures" // Go only
	APILoginLockout     = "api.loginLockout"     // Go only
	// DisableShare refuses POST /api/share/*. The Java server keeps the same
	// switch as a server attribute named disableShare.
	DisableShare = "disableShare"

	// Alert rules.
	AlertOverspeedKnots  = "alert.overspeedKnots"  // Go only
	AlertPowerCutVolts   = "alert.powerCutVolts"   // Go only
	AlertOverspeedLowest = "alert.overspeedLowest" // Go only
	AlertRepeat          = "alert.repeat"          // Go only
	AlertGeofenceRefresh = "alert.geofenceRefresh" // Go only
	AlertDeviceRefresh   = "alert.deviceRefresh"   // Go only

	// M4-1 event rules. The threshold names match the Java Keys, because a
	// device attribute with the same name overrides them per device.
	EventFuelDropThreshold       = "fuelDropThreshold"
	EventFuelIncreaseThreshold   = "fuelIncreaseThreshold"
	EventProximityEnter          = "proximityEnterDistance"
	EventProximityExit           = "proximityExitDistance"
	EventUnaccompanied           = "unaccompaniedDistance"
	EventGeofenceSegmentCrossing = "event.geofence.segmentCrossing"
	EventBehaviorAcceleration    = "event.behavior.accelerationThreshold"
	EventBehaviorBraking         = "event.behavior.brakingThreshold"
	EventIgnoreDuplicateAlerts   = "event.ignoreDuplicateAlerts"

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

	// Additional notification providers. These have no flags: they are set in
	// the configuration file or the environment and read directly, so adding a
	// provider does not grow the command line.
	NotifyTelegramToken     = "notificator.telegram.key"
	NotifyTelegramChatID    = "notificator.telegram.chatId"
	NotifyTelegramBaseURL   = "notificator.telegram.baseUrl"
	NotifyPushoverToken     = "notificator.pushover.token"
	NotifyPushoverUser      = "notificator.pushover.user"
	NotifyPushoverURL       = "notificator.pushover.url"
	NotifyFirebaseAccount   = "notificator.firebase.serviceAccount"
	NotifyFirebaseMode      = "notificator.firebase.mode"
	NotifyFirebaseTokenURL  = "notificator.firebase.tokenUrl"
	NotifyFirebaseURL       = "notificator.firebase.url"
	NotifyTraccarKey        = "notificator.traccar.key"
	NotifyTraccarURL        = "notificator.traccar.url"
	NotifyWhatsappToken     = "notificator.whatsapp.token"
	NotifyWhatsappPhoneID   = "notificator.whatsapp.phoneNumberId"
	NotifyWhatsappTemplate  = "notificator.whatsapp.template"
	NotifyWhatsappLanguage  = "notificator.whatsapp.language"
	NotifyWhatsappURL       = "notificator.whatsapp.url"
	SMSHTTPURL              = "sms.http.url"
	SMSHTTPTemplate         = "sms.http.template"
	SMSHTTPAuthorizationHdr = "sms.http.authorizationHeader"
	SMSHTTPAuthorization    = "sms.http.authorization"
	SMSHTTPUser             = "sms.http.user"
	SMSHTTPPassword         = "sms.http.password"
	SMSAWSAccess            = "sms.aws.access"
	SMSAWSSecret            = "sms.aws.secret"
	SMSAWSRegion            = "sms.aws.region"
	SMSAWSEndpoint          = "sms.aws.endpoint" // Go only: an SNS compatible endpoint

	// Report limits and trip detection. They match the Java Keys names and are
	// read from the configuration file without a flag.
	ReportMaxPositions        = "report.maxPositions"
	ReportPeriodLimit         = "report.periodLimit"
	ReportTripMinDistance     = "report.trip.minimalTripDistance"
	ReportTripMinDuration     = "report.trip.minimalTripDuration"
	ReportTripParkingDuration = "report.trip.minimalParkingDuration"
	ReportTripNoDataDuration  = "report.trip.minimalNoDataDuration"
	ReportIgnoreOdometer      = "report.ignoreOdometer"

	// Forwarding. forward.url and event.forward.url are the Java keys; the
	// mqtt, redis and wialon sections are Go only, because the Java server
	// forwards through its protocol modules instead of a dedicated transport.
	ForwardURL            = "forward.url"
	ForwardType           = "forward.type"
	ForwardHeader         = "forward.header"
	EventForwardURL       = "event.forward.url"
	EventForwardType      = "event.forward.type"
	EventForwardHeader    = "event.forward.header"
	ForwardMQTTBroker     = "forward.mqtt.broker"
	ForwardMQTTTopic      = "forward.mqtt.topic"
	ForwardMQTTClientID   = "forward.mqtt.clientId"
	ForwardMQTTUser       = "forward.mqtt.username"
	ForwardMQTTPassword   = "forward.mqtt.password"
	ForwardRedisURL       = "forward.redis.url"
	ForwardRedisKeyPrefix = "forward.redis.keyPrefix"
	ForwardWialonAddress  = "forward.wialon.address"
	ForwardWialonVersion  = "forward.wialon.version"
	ForwardKind           = "forward.kind"
	ForwardQueue          = "forward.queue"
	ForwardAttempts       = "forward.attempts"
	ForwardBackoff        = "forward.backoff"
	ForwardTimeout        = "forward.timeout"

	// Geographic services. They are read through their own helper because a
	// service is only enabled when it has everything it needs.
	GeocoderEnable    = "geocoder.enable"
	GeocoderType      = "geocoder.type"
	GeocoderURL       = "geocoder.url"
	GeocoderKey       = "geocoder.key"
	GeocoderLanguage  = "geocoder.language"
	GeocoderCache     = "geocoder.cache"
	GeocoderCacheFile = "geocoder.cache.file"
	GeocoderTimeout   = "geocoder.timeout"
	SpeedLimitEnable  = "speedlimit.enable"
	SpeedLimitURL     = "speedlimit.url"
	SpeedLimitRadius  = "speedlimit.radius"
	SpeedLimitCache   = "speedlimit.cache"
	MapMatcherEnable  = "mapmatcher.enable"
	MapMatcherURL     = "mapmatcher.url"
	MapMatcherKey     = "mapmatcher.key"
	MapMatcherRadius  = "mapmatcher.radius"
	GeolocationEnable = "geolocation.enable"
	GeolocationURL    = "geolocation.url"
	GeolocationKey    = "geolocation.key"
	GeolocationCache  = "geolocation.cache"

	// StatisticsInterval is how often a row is written to the statistics table.
	// Zero disables the history while /metrics keeps working.
	StatisticsInterval = "statistics.interval"

	// Scheduled maintenance. The intervals are durations; zero turns a task off.
	ScheduleExpiration      = "schedule.expiration.interval"
	ScheduleCleanup         = "schedule.cleanup.interval"
	ScheduleActionRetention = "schedule.cleanup.actionRetention"
	ScheduleReports         = "schedule.reports.interval"
	ScheduleHealth          = "schedule.health.interval"

	// TLS certificates for the protocol listeners. The Java server configures
	// these on the web server only, so this is a Go addition.
	ServerTLSCertificate = "server.tls.certificate"
	ServerTLSKey         = "server.tls.key"
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
	WebPath,
	APIKey,
	APIAllowAnonymous,
	APIIssueToken,
	APITokenScopes,
	APITokenTTL,
	APILoginMaxFailures,
	APILoginLockout,
	DisableShare,

	AlertOverspeedKnots,
	AlertPowerCutVolts,
	AlertOverspeedLowest,
	AlertRepeat,
	AlertGeofenceRefresh,
	AlertDeviceRefresh,

	EventFuelDropThreshold,
	EventFuelIncreaseThreshold,
	EventProximityEnter,
	EventProximityExit,
	EventUnaccompanied,
	EventGeofenceSegmentCrossing,
	EventBehaviorAcceleration,
	EventBehaviorBraking,
	EventIgnoreDuplicateAlerts,

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

	NotifyTelegramToken,
	NotifyTelegramChatID,
	NotifyTelegramBaseURL,
	NotifyPushoverToken,
	NotifyPushoverUser,
	NotifyPushoverURL,
	NotifyFirebaseAccount,
	NotifyFirebaseMode,
	NotifyFirebaseTokenURL,
	NotifyFirebaseURL,
	NotifyTraccarKey,
	NotifyTraccarURL,
	NotifyWhatsappToken,
	NotifyWhatsappPhoneID,
	NotifyWhatsappTemplate,
	NotifyWhatsappLanguage,
	NotifyWhatsappURL,
	SMSHTTPURL,
	SMSHTTPTemplate,
	SMSHTTPAuthorizationHdr,
	SMSHTTPAuthorization,
	SMSHTTPUser,
	SMSHTTPPassword,
	SMSAWSAccess,
	SMSAWSSecret,
	SMSAWSRegion,
	SMSAWSEndpoint,

	ReportMaxPositions,
	ReportPeriodLimit,
	ReportTripMinDistance,
	ReportTripMinDuration,
	ReportTripParkingDuration,
	ReportTripNoDataDuration,
	ReportIgnoreOdometer,

	ForwardURL,
	ForwardType,
	ForwardHeader,
	EventForwardURL,
	EventForwardType,
	EventForwardHeader,
	ForwardMQTTBroker,
	ForwardMQTTTopic,
	ForwardMQTTClientID,
	ForwardMQTTUser,
	ForwardMQTTPassword,
	ForwardRedisURL,
	ForwardRedisKeyPrefix,
	ForwardWialonAddress,
	ForwardWialonVersion,
	ForwardKind,
	ForwardQueue,
	ForwardAttempts,
	ForwardBackoff,
	ForwardTimeout,

	ServerTLSCertificate,
	ServerTLSKey,

	GeocoderEnable,
	GeocoderType,
	GeocoderURL,
	GeocoderKey,
	GeocoderLanguage,
	GeocoderCache,
	GeocoderCacheFile,
	GeocoderTimeout,
	SpeedLimitEnable,
	SpeedLimitURL,
	SpeedLimitRadius,
	SpeedLimitCache,
	MapMatcherEnable,
	MapMatcherURL,
	MapMatcherKey,
	MapMatcherRadius,
	GeolocationEnable,
	GeolocationURL,
	GeolocationKey,
	GeolocationCache,

	StatisticsInterval,
	ScheduleExpiration,
	ScheduleCleanup,
	ScheduleActionRetention,
	ScheduleReports,
	ScheduleHealth,
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
	// ProtocolTransport is the listen transport: tcp (the default), udp or tls.
	ProtocolTransport = "transport"
)

// ProtocolKeys is the set of keys Protocol resolves. Only these can be set per
// protocol, which keeps the resolution order documented and testable.
var ProtocolKeys = []string{
	ProtocolAddress, ProtocolPort, ProtocolTimeout, ProtocolRegisterUnknown, ProtocolTransport,
}

// Protocols are the sections the gateway ships protocol servers for.
var Protocols = []string{
	"h02", "gt06", "jt808", "teltonika",
	"osmand", "mqtt",
	"gps103", "tk103", "meitrack", "wondex",
}

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
	DisableShare:        "false",

	AlertGeofenceRefresh: "5m",
	StatisticsInterval:   "1m",
	AlertDeviceRefresh:   "5m",

	EventIgnoreDuplicateAlerts: "true",

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
	// The two transports the gateway added in M6: OsmAnd is the HTTP protocol
	// the mobile app speaks, MQTT the broker-less variant of the same payload.
	"osmand": "0.0.0.0:5055",
	"mqtt":   "0.0.0.0:1883",

	// The text protocol family added in M6-2. The ports are the ones the Java
	// server ships in its default configuration.
	"gps103":   "0.0.0.0:5002",
	"tk103":    "0.0.0.0:5001",
	"meitrack": "0.0.0.0:5020",
	"wondex":   "0.0.0.0:5006",
}

// defaultProtocolTransports overrides the tcp default for the protocols that
// are not plain byte streams. A section that names its own transport wins.
var defaultProtocolTransports = map[string]string{
	"osmand": "http",
}

// legacyEnvAliases keeps the environment variables the gateway documented
// before the configuration layer existed working. The canonical spelling of a
// key is derived by EnvName.
var legacyEnvAliases = map[string]string{
	"TRACCAR_DSN":           DatabaseURL,
	"TRACCAR_API_KEY":       APIKey,
	"TRACCAR_SMTP_PASSWORD": MailSMTPPassword,
}
