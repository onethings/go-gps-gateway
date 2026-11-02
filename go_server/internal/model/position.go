// Package model defines the protocol independent data model that every
// decoder converts into. It mirrors the parts of org.traccar.model.Position
// that the protocol decoders rely on.
package model

import (
	"fmt"
	"time"
)

// Attribute keys, matching the constants in org.traccar.model.Position.
const (
	KeyOriginal   = "raw"
	KeyPriority   = "priority"
	KeyIndex      = "index"
	KeyHDOP       = "hdop"
	KeyVDOP       = "vdop"
	KeyPDOP       = "pdop"
	KeySatellites = "sat"
	KeySatVisible = "satVisible"
	KeyRSSI       = "rssi"
	KeyGPS        = "gps"
	KeyRoaming    = "roaming"
	KeyEvent      = "event"
	KeyAlarm      = "alarm"
	// KeySpeedLimit is the posted limit of the road the device is on, in knots,
	// written by the geographic speed limit service.
	KeySpeedLimit = "speedLimit"
	KeyStatus     = "status"
	KeyOdometer   = "odometer"
	KeyOdometerTrip = "odometerTrip"
	KeyHours      = "hours"
	KeySteps      = "steps"
	KeyHeartRate  = "heartRate"
	KeyInput      = "input"
	KeyOutput     = "output"
	KeyImage      = "image"
	KeyVideo      = "video"
	KeyAudio      = "audio"

	KeyPower           = "power"
	KeyBattery         = "battery"
	KeyBatteryLevel    = "batteryLevel"
	KeyFuel            = "fuel"
	KeyFuelUsed        = "fuelUsed"
	KeyFuelConsumption = "fuelConsumption"
	KeyFuelLevel       = "fuelLevel"
	KeyEnergy          = "energy"
	KeyEnergyLevel     = "energyLevel"

	KeyVersionFW = "versionFw"
	KeyVersionHW = "versionHw"
	KeyType      = "type"
	KeyIgnition  = "ignition"
	KeyBusiness  = "business"
	KeyFlags     = "flags"
	KeyAntenna   = "antenna"
	KeyCharge    = "charge"
	KeyIP        = "ip"
	KeyArchive   = "archive"
	KeyDistance  = "distance"
	KeyTotalDist = "totalDistance"
	KeyRPM       = "rpm"
	KeyVIN       = "vin"
	KeyApprox    = "approximate"
	KeyThrottle  = "throttle"
	KeyMotion    = "motion"
	KeyArmed     = "armed"
	KeyGeofence  = "geofence"
	KeyAccel     = "acceleration"
	KeyHumidity  = "humidity"
	KeyDeviceTmp = "deviceTemp"
	KeyCoolant   = "coolantTemp"
	KeyEngineLd  = "engineLoad"
	KeyEngineTmp = "engineTemp"
	KeyOperator  = "operator"
	KeyCommand   = "command"
	KeyBlocked   = "blocked"
	KeyLock      = "lock"
	KeyDoor      = "door"
	KeyAxleWt    = "axleWeight"
	KeyGSensor   = "gSensor"
	KeyICCID     = "iccid"
	KeyPhone     = "phone"
	KeySpeedLim  = "speedLimit"
	KeyDrivingTm = "drivingTime"

	KeyDTCs     = "dtcs"
	KeyOBDSpeed = "obdSpeed"
	KeyOBDOdo   = "obdOdometer"

	KeyResult        = "result"
	KeyDriverID      = "driverUniqueId"
	KeyCard          = "card"
	KeyCell          = "cell"
	KeyBlockedCount  = "blockedCount"
	KeyDeliveryState = "deliveryState"
)

// Attribute prefix keys, matching PREFIX_* in org.traccar.model.Position.
const (
	PrefixTemp  = "temp"
	PrefixADC   = "adc"
	PrefixIO    = "io"
	PrefixCount = "count"
	PrefixIn    = "in"
	PrefixOut   = "out"
)

// Alarm values, matching ALARM_* in org.traccar.model.Position.
const (
	AlarmGeneral          = "general"
	AlarmSOS              = "sos"
	AlarmVibration        = "vibration"
	AlarmMovement         = "movement"
	AlarmLowSpeed         = "lowspeed"
	AlarmOverspeed        = "overspeed"
	AlarmFallDown         = "fallDown"
	AlarmLowPower         = "lowPower"
	AlarmLowBattery       = "lowBattery"
	AlarmFault            = "fault"
	AlarmPowerOff         = "powerOff"
	AlarmPowerOn          = "powerOn"
	AlarmDoor             = "door"
	AlarmLock             = "lock"
	AlarmUnlock           = "unlock"
	AlarmGeofence         = "geofence"
	AlarmGeofenceEnter    = "geofenceEnter"
	AlarmGeofenceExit     = "geofenceExit"
	AlarmGPSAntennaCut    = "gpsAntennaCut"
	AlarmAccident         = "accident"
	AlarmTow              = "tow"
	AlarmIdle             = "idle"
	AlarmHighRPM          = "highRpm"
	AlarmHardAcceleration = "hardAcceleration"
	AlarmHardBraking      = "hardBraking"
	AlarmHardCornering    = "hardCornering"
	AlarmLaneChange       = "laneChange"
	AlarmFatigueDriving   = "fatigueDriving"
	AlarmPowerCut         = "powerCut"
	AlarmPowerRestored    = "powerRestored"
	AlarmJamming          = "jamming"
	AlarmBraking          = "braking"
	AlarmAcceleration     = "acceleration"
	AlarmTemperature      = "temperature"
	AlarmParking          = "parking"
	AlarmBonnet           = "bonnet"
	AlarmFootBrake        = "footBrake"
	AlarmFuelLeak         = "fuelLeak"
	AlarmTampering        = "tampering"
	AlarmRemoving         = "removing"
)

// Position is the unified output of every protocol decoder.
type Position struct {
	// ID is the database row id, zero until the position has been stored.
	ID int64
	// ServerTime is when the server stored the position, zero until then.
	ServerTime time.Time
	Protocol   string
	DeviceID   int64
	Time       time.Time
	Valid      bool
	Latitude   float64
	Longitude  float64
	Altitude   float64
	Speed      float64
	Course     float64
	Accuracy   float64
	Network    *Network
	Alarms     []string
	// Outdated marks a position that carries no GPS fix, only secondary
	// information such as cell towers or battery level.
	Outdated bool
	// Attributes holds every extra value reported by the device.
	Attributes Attributes
	// GeofenceIDs are the geofences containing the position, written by the
	// geofence stage. Empty when the position is outside every geofence.
	GeofenceIDs []int64
}

// NewPosition creates an empty position tagged with the protocol name.
func NewPosition(protocol string) *Position {
	return &Position{Protocol: protocol, Attributes: Attributes{}}
}

// Set stores an attribute, ignoring nil values like the Java implementation.
func (p *Position) Set(key string, value any) {
	if p.Attributes == nil {
		p.Attributes = Attributes{}
	}
	p.Attributes.Set(key, value)
}

// SetIndexed stores a prefixed attribute, e.g. SetIndexed("temp", 1, 21.5)
// produces the "temp1" key.
func (p *Position) SetIndexed(prefix string, index int, value any) {
	p.Set(fmt.Sprintf("%s%d", prefix, index), value)
}

// Get returns an attribute value.
func (p *Position) Get(key string) (any, bool) {
	v, ok := p.Attributes[key]
	return v, ok
}

// HasAttribute reports whether a key is present.
func (p *Position) HasAttribute(key string) bool {
	_, ok := p.Attributes[key]
	return ok
}

// AddAlarm records an alarm, keeping alarm attribute in sync.
func (p *Position) AddAlarm(alarm string) {
	p.Alarms = append(p.Alarms, alarm)
	p.Set(KeyAlarm, alarm)
}

// SetNetwork attaches network information.
func (p *Position) SetNetwork(network *Network) {
	if network != nil && !network.IsEmpty() {
		p.Network = network
	}
}

// MarkOutdated records that the position has no GPS fix of its own.
func (p *Position) MarkOutdated(deviceTime time.Time) {
	if p.DeviceID != 0 {
		p.Outdated = true
		if !deviceTime.IsZero() {
			p.Time = deviceTime
		}
	}
}

// IsLocated reports whether the position carries a latitude/longitude fix.
func (p *Position) IsLocated() bool {
	return p.Valid || p.Latitude != 0 || p.Longitude != 0
}

// PositionFilter selects positions for a query. Zero values mean "no
// restriction"; Limit and Offset are clamped by the storage layer. From and To
// are compared against the device time, which is what the reports use.
type PositionFilter struct {
	DeviceID int64
	// IDs restricts the result to a set of position ids, which is what
	// GET /api/positions/{id} uses. Nil means "no restriction".
	IDs []int64
	// DeviceIDs restricts the result to a set of devices. Nil means "no
	// restriction"; a non-nil empty slice matches nothing.
	DeviceIDs []int64
	From      time.Time
	To        time.Time
	// Ascending flips the order from newest first to oldest first, which the
	// KML / GPX / CSV exports need to draw a track in the direction it was
	// driven.
	Ascending bool
	Limit     int
	Offset    int
}

// String renders a compact human readable description, used by tests and
// debugging output.
func (p *Position) String() string {
	return fmt.Sprintf("Position{protocol=%s device=%d time=%s valid=%v lat=%.5f lon=%.5f speed=%.2f course=%.2f}",
		p.Protocol, p.DeviceID, p.Time.UTC().Format(time.RFC3339Nano), p.Valid,
		p.Latitude, p.Longitude, p.Speed, p.Course)
}
