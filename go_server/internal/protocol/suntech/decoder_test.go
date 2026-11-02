// Package suntech_test verifies the Suntech decoder against synthetic vectors
// covering every report layout. Ported field order from SuntechProtocolDecoder.
package suntech_test

import (
	"testing"

	"traccar-server_go/go_server/internal/model"
	"traccar-server_go/go_server/internal/protocol/protocaltest"
	"traccar-server_go/go_server/internal/protocol/suntech"
)

func newDecoder(opts ...func(*suntech.Decoder)) *suntech.Decoder {
	d := suntech.NewDecoder(protocaltest.Base())
	for _, o := range opts {
		o(d)
	}
	return d
}

func pt(v int) func(*suntech.Decoder)  { return func(d *suntech.Decoder) { d.ProtocolType = v } }
func hbm(v int) func(*suntech.Decoder) { return func(d *suntech.Decoder) { d.HBM = v } }

// decode9: plain Location report (ST9 family).
func TestDecodeST9Location(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"ST910;Location;354541030647827;V1.0.1;2024030512;:30:45;22.123456;114.123456;60;120;1"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyVersionFW, "V1.0.1"))
}

// decode9: Emergency report adds the general alarm.
func TestDecodeST9Emergency(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"ST910;Emergency;354541030647827;V1.0.1;2024030512;:30:45;22.123456;114.123456;30;90;0"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", false, 22.123456, 114.123456).
			Attribute(model.KeyVersionFW, "V1.0.1"))
}

// decode9: Alert report with protocolType 1 adds cell, odometer, and alarm.
func TestDecodeST9AlertProtocolType1(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(pt(1)), []byte(
		"ST910;Alert;354541030647827;2024030512;:30:45;1;22.123456;114.123456;10;45;1;12345"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyOdometer, 12345))
}

// decode4: STT with model 41, seven cell towers and location.
func TestDecodeST4STTModel41(t *testing.T) {
	frame := "ST400STT;354541030647827;V2.0;41;V1;" +
		"28304;460;0;21;25742;0;" +
		"28305;460;0;25742;18;0;" +
		"28306;460;0;25742;15;0;" +
		"28307;460;0;25742;12;0;" +
		"28308;460;0;25742;9;0;" +
		"28309;460;0;25742;6;0;" +
		"28310;460;0;25742;3;0;" +
		"12.4;0;7;1;123;0;25;2;2024030512;:30:45;22.123456;114.123456;40;90;9;1"
	protocaltest.VerifyDecode(t, newDecoder(), []byte(frame),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "STT").
			Attribute(model.KeyVersionFW, "V2.0").
			Attribute(model.KeyBattery, 12.4).
			Attribute(model.KeyArchive, true).
			Attribute(model.KeyIndex, 7).
			Attribute(model.KeyStatus, 1).
			Attribute(model.KeyMotion, true).
			Attribute(model.KeySatellites, 9))
}

// decode2356: ST100 STT with status and index.
func TestDecodeST100STT(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"ST100STT;354541030647827;V1.0;2024030512;:30:45;28304;22.123456;114.123456;40;90;9;1;1234;12.4;100000;3;7"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "STT").
			Attribute(model.KeyVersionFW, "V1.0").
			Attribute(model.KeyOdometer, 1234).
			Attribute(model.KeyPower, 12.4).
			Attribute(model.KeyIgnition, true).
			Attribute(model.KeyStatus, 3).
			Attribute(model.KeyIndex, 7).
			Attribute(model.KeySatellites, 9))
}

// decode2356: ST500 skips the cell id field.
func TestDecodeST500STT(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"ST500STT;354541030647827;1;V1.0;2024030512;:30:45;22.123456;114.123456;40;90;9;1;1234;12.4;100000;3;7"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "STT").
			Attribute(model.KeyVersionFW, "V1.0").
			Attribute(model.KeyOdometer, 1234))
}

// decode2356: ST600 EMG adds model + network and an emergency alarm.
func TestDecodeST600EMG(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"ST600EMG;354541030647827;1;V1.0;2024030512;:30:45;28304;460;0;25742;21;22.123456;114.123456;40;90;9;1;1234;12.4;100000;8"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "EMG").
			Attribute(model.KeyVersionFW, "V1.0").
			Attribute(model.KeyOdometer, 1234).
			Attribute(model.KeyPower, 12.4))
}

// decode2356: ST300 ALT with HBM 2 adds the extended hours/battery/driver block.
func TestDecodeST300ALT(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(hbm(2)), []byte(
		"ST300ALT;354541030647827;1;V1.0;2024030512;:30:45;28304;22.123456;114.123456;40;90;9;1;1234;12.4;100000;41;12345;12.4;0;DRV-1;1;28304;460;0;21;25742"),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "ALT").
			Attribute(model.KeyHours, int64(12345)*60*1000).
			Attribute(model.KeyBattery, 12.4).
			Attribute(model.KeyArchive, true).
			Attribute(model.KeyDriverID, "DRV-1").
			Attribute(model.KeyOdometer, 1234))
}

// decodeUniversal: mask-based STT layout.
func TestDecodeUniversalSTT(t *testing.T) {
	frame := "STT;354541030647827;7F834;V1.0;2024030512;:30:45;22.123456;114.123456;40;90;9;1;1;0"
	protocaltest.VerifyDecode(t, newDecoder(), []byte(frame),
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyType, "STT").
			Attribute(model.KeyVersionFW, "V1.0").
			Attribute(model.KeySatellites, 9).
			Attribute(model.KeyIgnition, true).
			Attribute(model.KeyInput, 1).
			Attribute(model.KeyOutput, 0))
}

// decodeBinary: compact binary record with mask 0x3F834.
func TestDecodeBinary(t *testing.T) {
	frame := protocaltest.Hex(t, ""+
		"0100000000000005"+ // type, length, imei (5)
		"03F834"+ // mask: 2,4,5,11,12,13,14,15,16,17
		"010001"+ // versionFw 1.0.1
		"1803050C1E2D"+ // 2024-03-05 12:30:45
		"0151A340"+ // lat 22123456
		"06CD6A40"+ // lon 114123456
		"0FB9"+ // speed 4025 (40.25 kph)
		"2328"+ // course 9000 (90.00)
		"09"+ // satellites
		"01"+ // valid
		"01") // input -> ignition true
	protocaltest.VerifyDecode(t, newDecoder(), frame,
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyVersionFW, "1.0.1").
			Attribute(model.KeySatellites, 9).
			Attribute(model.KeyIgnition, true).
			Attribute(model.KeyInput, 1))
}

// decodeBinary: alert id 59 (driver id) reads the 8-byte driver field.
func TestDecodeBinaryDriverID(t *testing.T) {
	frame := protocaltest.Hex(t, ""+
		"8200000000000005"+ // type 0x82, length, imei
		"280000"+ // mask bits 19 and 21
		"3B"+ // alert id 59
		"3132333435363738") // driver id "12345678"
	protocaltest.VerifyDecode(t, newDecoder(), frame,
		protocaltest.PositionWith(protocaltest.ChecksAttributes).
			Attribute(model.KeyDriverID, "3132333435363738"))
}

// decodeZip: binary STX record.
func TestDecodeZip(t *testing.T) {
	frame := protocaltest.Hex(t, ""+
		"020000"+ // header, length
		"10"+ // type
		"0000000005"+ // imei
		"010000"+ // model, software version
		"1803050C1E2D"+ // 2024-03-05 12:30:45
		"0000"+"00"+ // lac, cid
		"16123456"+ // lat 22 + 123456/1e6
		"72123456"+ // lon 114 + 123456/1e6
		"0028"+"25"+ // speed 40 + 0.25
		"005A"+"00"+ // course 90
		"80"+ // flags: valid
		"0001E240"+ // odometer 123456
		"0C04"+ // power 12 + 0.04
		"01"+ // io: ignition
		"05"+ // event
		"FF") // footer
	protocaltest.VerifyDecode(t, newDecoder(), frame,
		protocaltest.Position().
			Location("2024-03-05T12:30:45Z", true, 22.123456, 114.123456).
			Attribute(model.KeyOdometer, 123456).
			Attribute(model.KeyPower, 12.04).
			Attribute(model.KeyIgnition, true).
			Attribute(model.KeyEvent, 5))
}

// decodeTravelReport: *HTE frames only carry the driver unique id.
func TestDecodeTravelReport(t *testing.T) {
	protocaltest.VerifyDecode(t, newDecoder(), []byte(
		"SA200HTE;354541030647827;DRV-1"),
		protocaltest.PositionWith(protocaltest.ChecksAttributes).
			Attribute(model.KeyDriverID, "DRV-1"))
}

// CRR crash frames produce no position in the single-frame Go decoder.
func TestDecodeCrashSkip(t *testing.T) {
	protocaltest.VerifyObject(t, newDecoder(), []byte("CRR;354541030647827;1;1"))
}

// Unsupported ST9 report types are ignored.
func TestDecodeST9Ignore(t *testing.T) {
	protocaltest.VerifyObject(t, newDecoder(), []byte(
		"ST910;Config;354541030647827;V1.0.1;2024030512;:30:45;22.123456;114.123456;60;120;1"))
}

// Unsupported ST100 report types are ignored.
func TestDecodeST100Ignore(t *testing.T) {
	protocaltest.VerifyObject(t, newDecoder(), []byte(
		"ST100;TXT;354541030647827;V1.0;2024030512;:30:45;28304;22.123456;114.123456;40;90;9;1;1234;12.4;100000"))
}
