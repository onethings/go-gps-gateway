package model

import "time"

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

// Command lifecycle states, matching the commands.status column. A command is
// pending from the moment it is recorded, becomes sent once the payload reached
// the device socket, and ends either done (the device answered) or failed (the
// send never got out or the device refused it).
const (
	CommandStatusPending = "pending"
	CommandStatusSent    = "sent"
	CommandStatusDone    = "done"
	CommandStatusFailed  = "failed"
)

// Command is a device independent instruction, the input of every encoder, and
// the record the commands table keeps of what was sent.
type Command struct {
	// ID is the commands row id, zero until the command has been recorded.
	ID int64
	// DeviceID, Type and Attributes are the request as the caller wrote it.
	DeviceID   int64
	Type       string
	Attributes Attributes
	// Status is one of the CommandStatus* values. An encoder ignores it.
	Status string
	// ResultCode is the result the device reported with its acknowledgement.
	// It stays nil for commands that were never answered, which is why a zero
	// code and no code are different values.
	ResultCode *int
	// Serial is the downlink message serial the encoder handed to this command,
	// or zero when the protocol has no serial. It is what the device echoes
	// back, so a JT808 answer that carries it closes exactly this row.
	Serial int
	// Attempts counts how often the payload was written to the device socket:
	// 1 for a command that went out once, more when it was retransmitted.
	Attempts int
	SentAt   time.Time
	DoneAt   time.Time
	// CreatedAt is when the row was written, before the payload was sent.
	CreatedAt time.Time
}

// CommandFilter selects commands for a query. Zero values mean "no
// restriction"; Limit and Offset are clamped by the storage layer.
type CommandFilter struct {
	DeviceID int64
	// DeviceIDs restricts the result to a set of devices. Nil means "no
	// restriction"; a non-nil empty slice matches nothing, which is how a
	// token without device grants stays isolated.
	DeviceIDs []int64
	Limit     int
	Offset    int
}

// NewCommand creates a command with an initialised attribute map.
func NewCommand(deviceID int64, commandType string) *Command {
	return &Command{DeviceID: deviceID, Type: commandType, Attributes: Attributes{}}
}

// SetAttribute stores a command parameter.
func (c *Command) SetAttribute(key string, value any) {
	if c.Attributes == nil {
		c.Attributes = Attributes{}
	}
	c.Attributes.Set(key, value)
}

// GetAttribute returns a command parameter.
func (c *Command) GetAttribute(key string) (any, bool) {
	v, ok := c.Attributes[key]
	return v, ok
}
