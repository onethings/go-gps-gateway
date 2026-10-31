package model

// Command types, matching org.traccar.model.Command.TYPE_*.
const (
	CommandCustom           = "custom"
	CommandIdentification   = "deviceIdentification"
	CommandPositionSingle   = "positionSingle"
	CommandPositionPeriodic = "positionPeriodic"
	CommandPositionStop     = "positionStop"
	CommandEngineStop       = "engineStop"
	CommandEngineResume     = "engineResume"
	CommandAlarmArm         = "alarmArm"
	CommandAlarmDisarm      = "alarmDisarm"
	CommandAlarmDismiss     = "alarmDismiss"
	CommandSetTimezone      = "setTimezone"
	CommandRequestPhoto     = "requestPhoto"
	CommandPowerOff         = "powerOff"
	CommandRebootDevice     = "rebootDevice"
	CommandFactoryReset     = "factoryReset"
	CommandSendSMS          = "sendSms"
	CommandSendUSSD         = "sendUssd"
	CommandSOSNumber        = "sosNumber"
	CommandSilenceTime      = "silenceTime"
	CommandSetPhonebook     = "setPhonebook"
	CommandMessage          = "message"
	CommandVoiceMessage     = "voiceMessage"
	CommandOutputControl    = "outputControl"
	CommandVoiceMonitoring  = "voiceMonitoring"
	CommandSetAGPS          = "setAgps"
	CommandSetIndicator     = "setIndicator"
	CommandConfiguration    = "configuration"
	CommandGetVersion       = "getVersion"
	CommandFirmwareUpdate   = "firmwareUpdate"
	CommandSetConnection    = "setConnection"
	CommandSetOdometer      = "setOdometer"
	CommandGetModemStatus   = "getModemStatus"
	CommandGetDeviceStatus  = "getDeviceStatus"
	CommandSetSpeedLimit    = "setSpeedLimit"
	CommandModePowerSaving  = "modePowerSaving"
	CommandModeDeepSleep    = "modeDeepSleep"
	CommandVideoStart       = "videoStart"
	CommandVideoStop        = "videoStop"

	CommandAlarmGeofence  = "alarmGeofence"
	CommandAlarmBattery   = "alarmBattery"
	CommandAlarmSOS       = "alarmSos"
	CommandAlarmRemove    = "alarmRemove"
	CommandAlarmClock     = "alarmClock"
	CommandAlarmSpeed     = "alarmSpeed"
	CommandAlarmFall      = "alarmFall"
	CommandAlarmVibration = "alarmVibration"
)

// Command attribute keys, matching org.traccar.model.Command.KEY_*.
const (
	CmdKeyUniqueID       = "uniqueId"
	CmdKeyFrequency      = "frequency"
	CmdKeyLanguage       = "language"
	CmdKeyTimezone       = "timezone"
	CmdKeyDevicePassword = "devicePassword"
	CmdKeyRadius         = "radius"
	CmdKeyMessage        = "message"
	CmdKeyEnable         = "enable"
	CmdKeyData           = "data"
	CmdKeyDuration       = "duration"
	CmdKeyIndex          = "index"
	CmdKeyPhone          = "phone"
	CmdKeyServer         = "server"
	CmdKeyPort           = "port"
)

// Command is a device independent instruction, the input of every encoder.
type Command struct {
	DeviceID   int64
	Type       string
	Attributes map[string]any
}

// NewCommand creates a command with an initialised attribute map.
func NewCommand(deviceID int64, commandType string) *Command {
	return &Command{DeviceID: deviceID, Type: commandType, Attributes: map[string]any{}}
}

// SetAttribute stores a command parameter.
func (c *Command) SetAttribute(key string, value any) {
	if c.Attributes == nil {
		c.Attributes = map[string]any{}
	}
	c.Attributes[key] = value
}

// GetAttribute returns a command parameter.
func (c *Command) GetAttribute(key string) (any, bool) {
	v, ok := c.Attributes[key]
	return v, ok
}
