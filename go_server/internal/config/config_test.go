package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes a traccar.xml into a temporary directory and returns its
// path.
func writeConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "traccar.xml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// settings parses args against cfg, the way main does.
func settings(t *testing.T, cfg *Config, args ...string) Settings {
	t.Helper()

	fs := newFlagSet()
	binder := Bind(fs, cfg, "traccar.xml")
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	parsed, err := binder.Settings()
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return parsed
}

func TestDefaultsMatchTheFlagsTheGatewayHad(t *testing.T) {
	got := settings(t, Default())

	addresses := map[string]string{}
	for _, protocol := range got.Protocols {
		addresses[protocol.Name] = protocol.Address
		if protocol.ReadTimeout != 10*time.Minute {
			t.Errorf("%s read timeout = %v, want 10m", protocol.Name, protocol.ReadTimeout)
		}
		if !protocol.AutoRegister {
			t.Errorf("%s auto register = false, want true", protocol.Name)
		}
	}
	want := map[string]string{
		"h02":       "0.0.0.0:5013",
		"gt06":      "0.0.0.0:5023",
		"jt808":     "0.0.0.0:8610",
		"teltonika": "0.0.0.0:5027",
	}
	for name, address := range want {
		if addresses[name] != address {
			t.Errorf("%s address = %q, want %q", name, addresses[name], address)
		}
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"database url", got.DatabaseURL, ""},
		{"batch size", got.BatchSize, 500},
		{"batch interval", got.BatchInterval, time.Second},
		{"web address", got.WebAddress, "0.0.0.0:8082"},
		{"api key", got.APIKey, ""},
		{"allow anonymous", got.AllowAnonymous, false},
		{"token ttl", got.TokenTTL, time.Duration(0)},
		{"token scopes", strings.Join(got.TokenScopes, ","), "read:positions,read:devices,read:alerts"},
		{"login failures", got.LoginMaxFailures, 5},
		{"login lock", got.LoginLockDuration, 15 * time.Minute},
		{"overspeed", got.OverspeedKnots, float64(0)},
		{"power cut", got.PowerCutVolts, float64(0)},
		{"prefer lowest", got.OverspeedLowest, false},
		{"alert repeat", got.AlertRepeat, time.Duration(0)},
		{"geofence refresh", got.GeofenceRefresh, 5 * time.Minute},
		{"device refresh", got.DeviceRefresh, 5 * time.Minute},
		{"auto register", got.AutoRegister, true},
		{"auto register set", got.AutoRegisterSet, false},
		{"command retry", got.CommandRetry, 30 * time.Second},
		{"command attempts", got.CommandAttempts, 3},
		{"command queue", got.CommandQueue, 32},
		{"command queue ttl", got.CommandQueueTTL, 5 * time.Minute},
		{"command retention", got.CommandRetention, 7 * 24 * time.Hour},
		{"notify types", strings.Join(got.NotifyTypes, ","), ""},
		{"notify timeout", got.NotifyTimeout, 10 * time.Second},
		{"notify queue", got.NotifyQueue, 256},
		{"email tls", got.EmailTLS, "auto"},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}

func TestFileLayerOverridesTheDefaults(t *testing.T) {
	path := writeConfig(t, `<?xml version='1.0' encoding='UTF-8'?>
<properties>
	<entry key='web.address'>127.0.0.1:9000</entry>
	<entry key='database.batchSize'>10</entry>
	<entry key='alert.overspeedKnots'>11.5</entry>
	<entry key='database.registerUnknown'>false</entry>
	<entry key='protocol.gt06.address'>127.0.0.1:15023</entry>
</properties>`)

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Source(WebAddress) != SourceFile {
		t.Errorf("web.address source = %q, want %q", cfg.Source(WebAddress), SourceFile)
	}
	if cfg.String(WebAddress) != "127.0.0.1:9000" {
		t.Errorf("web.address = %q", cfg.String(WebAddress))
	}
	if cfg.String(DatabaseBatchSize) != "10" {
		t.Errorf("database.batchSize = %q", cfg.String(DatabaseBatchSize))
	}

	got := settings(t, cfg)
	if got.BatchSize != 10 {
		t.Errorf("batch size = %d, want 10", got.BatchSize)
	}
	if got.OverspeedKnots != 11.5 {
		t.Errorf("overspeed = %v, want 11.5", got.OverspeedKnots)
	}
	if got.AutoRegister {
		t.Error("auto register = true, want false")
	}
	for _, protocol := range got.Protocols {
		if protocol.AutoRegister {
			t.Errorf("%s auto register = true, want false", protocol.Name)
		}
		if protocol.Name == "gt06" && protocol.Address != "127.0.0.1:15023" {
			t.Errorf("gt06 address = %q", protocol.Address)
		}
	}
}

func TestEnvironmentOverridesTheFileAndTheFlagOverridesBoth(t *testing.T) {
	path := writeConfig(t, `<properties>
	<entry key='web.address'>127.0.0.1:9000</entry>
</properties>`)

	cfg, err := Load(path, []string{"TRACCAR_WEB_ADDRESS=127.0.0.1:9100"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.String(WebAddress) != "127.0.0.1:9100" {
		t.Fatalf("web.address = %q, want the environment value", cfg.String(WebAddress))
	}
	if cfg.Source(WebAddress) != SourceEnv {
		t.Errorf("web.address source = %q, want %q", cfg.Source(WebAddress), SourceEnv)
	}

	got := settings(t, cfg, "-http", "127.0.0.1:9200")
	if got.WebAddress != "127.0.0.1:9200" {
		t.Errorf("web address = %q, want the flag value", got.WebAddress)
	}
}

func TestLegacyEnvironmentNamesStillWork(t *testing.T) {
	cfg, err := Load("", []string{
		"TRACCAR_DSN=postgres://legacy/db",
		"TRACCAR_API_KEY=legacy-key",
		"TRACCAR_SMTP_PASSWORD=legacy-secret",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.String(DatabaseURL) != "postgres://legacy/db" {
		t.Errorf("database.url = %q", cfg.String(DatabaseURL))
	}
	if cfg.String(APIKey) != "legacy-key" {
		t.Errorf("api.key = %q", cfg.String(APIKey))
	}
	if cfg.String(MailSMTPPassword) != "legacy-secret" {
		t.Errorf("mail.smtp.password = %q", cfg.String(MailSMTPPassword))
	}

	// The canonical spelling wins when both are present.
	cfg, err = Load("", []string{"TRACCAR_DSN=legacy", "TRACCAR_DATABASE_URL=canonical"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.String(DatabaseURL) != "canonical" {
		t.Errorf("database.url = %q, want the canonical variable to win", cfg.String(DatabaseURL))
	}
}

func TestUnrelatedEnvironmentVariablesAreIgnored(t *testing.T) {
	cfg, err := Load("", []string{
		"TRACCAR_UNKNOWN_KEY=1",
		"TRACCAR_PROTOCOL_NOSUCHPROTOCOL=1",
		"HOME=/root",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.env) != 0 {
		t.Errorf("environment layer = %v, want it empty", cfg.env)
	}
}

func TestConfigDefaultIncludeIsLoadedFirst(t *testing.T) {
	dir := t.TempDir()
	included := filepath.Join(dir, "default.xml")
	root := filepath.Join(dir, "traccar.xml")

	if err := os.WriteFile(included, []byte(`<properties>
		<entry key='web.address'>included:1</entry>
		<entry key='database.batchSize'>7</entry>
	</properties>`), 0o600); err != nil {
		t.Fatalf("write include: %v", err)
	}
	if err := os.WriteFile(root, []byte(`<properties>
		<entry key='config.default'>default.xml</entry>
		<entry key='web.address'>root:2</entry>
	</properties>`), 0o600); err != nil {
		t.Fatalf("write root: %v", err)
	}

	cfg, err := Load(root, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.String(WebAddress) != "root:2" {
		t.Errorf("web.address = %q, want the including file to win", cfg.String(WebAddress))
	}
	if cfg.String(DatabaseBatchSize) != "7" {
		t.Errorf("database.batchSize = %q, want the included value", cfg.String(DatabaseBatchSize))
	}
	if len(cfg.Files()) != 2 {
		t.Errorf("files = %v, want the include and the root file", cfg.Files())
	}
	if cfg.String(ConfigDefault) != "" {
		t.Error("config.default leaked into the values")
	}
}

func TestMissingFilesBehaveDifferentlyWhenNamedOnPurpose(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.xml"), nil); err == nil {
		t.Error("an explicitly named file that is missing should fail")
	}

	// The default path is optional, so running somewhere without traccar.xml
	// keeps working with defaults only.
	dir := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	if _, err := Load("", nil); err != nil {
		t.Fatalf("load without a file: %v", err)
	}
}

func TestConfigPathFromTheEnvironment(t *testing.T) {
	path := writeConfig(t, `<properties><entry key='web.address'>env-file:1</entry></properties>`)

	cfg, err := Load("", []string{"TRACCAR_CONFIG=" + path})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.String(WebAddress) != "env-file:1" {
		t.Errorf("web.address = %q, want the file named by TRACCAR_CONFIG", cfg.String(WebAddress))
	}
	if cfg.String(WebAddress) == "" || len(cfg.Files()) != 1 {
		t.Errorf("files = %v, want the one file", cfg.Files())
	}
}

func TestProtocolSectionBeatsTheGlobalValue(t *testing.T) {
	path := writeConfig(t, `<properties>
	<entry key='protocol.timeout'>30s</entry>
	<entry key='protocol.gt06.timeout'>2m</entry>
	<entry key='h02.port'>15013</entry>
	<entry key='protocol.registerUnknown'>false</entry>
	<entry key='protocol.gt06.registerUnknown'>true</entry>
</properties>`)

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := settings(t, cfg)

	byName := map[string]ProtocolSettings{}
	for _, protocol := range got.Protocols {
		byName[protocol.Name] = protocol
	}

	if byName["gt06"].ReadTimeout != 2*time.Minute {
		t.Errorf("gt06 timeout = %v, want the section value", byName["gt06"].ReadTimeout)
	}
	if byName["h02"].ReadTimeout != 30*time.Second {
		t.Errorf("h02 timeout = %v, want the shared protocol value", byName["h02"].ReadTimeout)
	}
	if byName["h02"].Address != "0.0.0.0:15013" {
		t.Errorf("h02 address = %q, want it built from the port", byName["h02"].Address)
	}
	if !byName["gt06"].AutoRegister {
		t.Error("gt06 auto register = false, want the section value true")
	}
	if byName["h02"].AutoRegister {
		t.Error("h02 auto register = true, want the shared protocol value false")
	}
}

func TestAutoRegisterFlagBeatsEverySection(t *testing.T) {
	path := writeConfig(t, `<properties>
	<entry key='protocol.gt06.registerUnknown'>true</entry>
</properties>`)

	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	got := settings(t, cfg, "-auto-register=false")
	if !got.AutoRegisterSet {
		t.Error("-auto-register should be reported as set")
	}
	for _, protocol := range got.Protocols {
		if protocol.AutoRegister {
			t.Errorf("%s auto register = true, want the flag value false", protocol.Name)
		}
	}
}

func TestEnabledFlagKeepsTheConfiguredValue(t *testing.T) {
	cfg, err := Load("", []string{"TRACCAR_PROTOCOL_GT06_ADDRESS=127.0.0.1:25023"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := settings(t, cfg)
	for _, protocol := range got.Protocols {
		if protocol.Name != "gt06" {
			continue
		}
		if protocol.Address != "127.0.0.1:25023" {
			t.Errorf("gt06 address = %q, want the environment value", protocol.Address)
		}
	}
}

func TestEmptyAddressDisablesTheServer(t *testing.T) {
	got := settings(t, Default(), "-gt06", "", "-http", "")
	if got.WebAddress != "" {
		t.Errorf("web address = %q, want it disabled", got.WebAddress)
	}
	for _, protocol := range got.Protocols {
		if protocol.Name == "gt06" && protocol.Address != "" {
			t.Errorf("gt06 address = %q, want it disabled", protocol.Address)
		}
		if protocol.Name == "h02" && protocol.Address == "" {
			t.Error("h02 address should keep its default")
		}
	}
}

func TestDurationsAcceptSecondsAndUnits(t *testing.T) {
	cfg, err := Load("", []string{
		"TRACCAR_DATABASE_BATCH_INTERVAL=2",
		"TRACCAR_NOTIFY_TIMEOUT=1500ms",
		"TRACCAR_COMMAND_RETENTION=48h",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got := settings(t, cfg)

	if got.BatchInterval != 2*time.Second {
		t.Errorf("batch interval = %v, want 2s", got.BatchInterval)
	}
	if got.NotifyTimeout != 1500*time.Millisecond {
		t.Errorf("notify timeout = %v, want 1.5s", got.NotifyTimeout)
	}
	if got.CommandRetention != 48*time.Hour {
		t.Errorf("command retention = %v, want 48h", got.CommandRetention)
	}
}

func TestSettingsReportsEveryProblem(t *testing.T) {
	cfg, err := Load("", []string{
		"TRACCAR_DATABASE_BATCH_SIZE=lots",
		"TRACCAR_NOTIFY_TIMEOUT=soon",
		"TRACCAR_PROTOCOL_GT06_TIMEOUT=never",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	fs := newFlagSet()
	binder := Bind(fs, cfg, "traccar.xml")
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("parse: %v", err)
	}

	_, err = binder.Settings()
	if err == nil {
		t.Fatal("want an error for the three broken values")
	}
	for _, want := range []string{"batch-size", "notify-timeout", "protocol.gt06.timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestBooleansAcceptTheSpellingsAFileUses(t *testing.T) {
	for _, text := range []string{"true", "TRUE", "yes", "on", "1"} {
		value, err := parseBool(text)
		if err != nil || !value {
			t.Errorf("parseBool(%q) = %v, %v", text, value, err)
		}
	}
	for _, text := range []string{"false", "FALSE", "no", "off", "0"} {
		value, err := parseBool(text)
		if err != nil || value {
			t.Errorf("parseBool(%q) = %v, %v", text, value, err)
		}
	}
	if _, err := parseBool("maybe"); err == nil {
		t.Error("parseBool(maybe) should fail")
	}
}

func TestEnvName(t *testing.T) {
	cases := map[string]string{
		"database.url":          "TRACCAR_DATABASE_URL",
		"alert.overspeedKnots":  "TRACCAR_ALERT_OVERSPEED_KNOTS",
		"api.tokenTTL":          "TRACCAR_API_TOKEN_TTL",
		"web.address":           "TRACCAR_WEB_ADDRESS",
		"protocol.gt06.address": "TRACCAR_PROTOCOL_GT06_ADDRESS",
	}
	for key, want := range cases {
		if got := EnvName(key); got != want {
			t.Errorf("EnvName(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestReportMaxPositionsIsPortable(t *testing.T) {
	// A traccar.xml copied from a Java installation has to keep its meaning.
	path := writeConfig(t, `<properties>
    <entry key='report.maxPositions'>1234</entry>
</properties>`)
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if value, err := cfg.Int(ReportMaxPositions); err != nil || value != 1234 {
		t.Errorf("report.maxPositions = %d (%v), want 1234 from traccar.xml", value, err)
	}

	// Environment wins over the file, like every other key.
	fromEnv, err := Load(path, []string{"TRACCAR_REPORT_MAX_POSITIONS=7"})
	if err != nil {
		t.Fatalf("load with environment: %v", err)
	}
	if value, err := fromEnv.Int(ReportMaxPositions); err != nil || value != 7 {
		t.Errorf("report.maxPositions = %d (%v), want 7 from the environment", value, err)
	}

	// The compiled default is the Java one.
	defaults, err := Load("", nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if value, err := defaults.Int(ReportMaxPositions); err != nil || value != 50000 {
		t.Errorf("default report.maxPositions = %d (%v), want 50000", value, err)
	}
}

func TestSummaryNamesTheLayers(t *testing.T) {
	if got := Default().Summary(); got != "defaults only" {
		t.Errorf("summary = %q", got)
	}

	cfg, err := Load("", []string{"TRACCAR_WEB_ADDRESS=127.0.0.1:1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Summary(); !strings.Contains(got, "environment") {
		t.Errorf("summary = %q, want it to mention the environment", got)
	}
}

func TestPathFromArgs(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		path  string
		found bool
	}{
		{"none", []string{"-http", ":8082"}, "", false},
		{"separate", []string{"-config", "a.xml"}, "a.xml", true},
		{"inline", []string{"--config=a.xml"}, "a.xml", true},
		{"short", []string{"-c", "b.xml"}, "b.xml", true},
		{"after other flags", []string{"-dsn", "x", "-config", "c.xml"}, "c.xml", true},
		{"after the terminator", []string{"--", "-config", "d.xml"}, "", false},
	}
	for _, test := range cases {
		path, found := PathFromArgs(test.args)
		if path != test.path || found != test.found {
			t.Errorf("%s: PathFromArgs(%v) = %q, %v; want %q, %v",
				test.name, test.args, path, found, test.path, test.found)
		}
	}
}
