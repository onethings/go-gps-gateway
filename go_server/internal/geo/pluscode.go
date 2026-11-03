package geo

import (
	"math"
	"strings"
)

// plusCode encodes a coordinate as a full Open Location Code, which needs no
// key and no request.
func plusCode(latitude, longitude float64) string {
	// The encoding works on a latitude shifted to 0..180 and a longitude
	// shifted to 0..360.
	latitude = math.Max(math.Min(latitude, 90), -90)
	longitude = math.Mod(longitude+180, 360)
	if longitude < 0 {
		longitude += 360
	}
	latitude += 90

	resolutions := [...]float64{20, 1, 0.05, 0.0025, 0.000125}
	var code strings.Builder
	for index, resolution := range resolutions {
		if index == 4 {
			code.WriteByte('+')
		}
		latitudeIndex := int(latitude / resolution)
		longitudeIndex := int(longitude / resolution)
		code.WriteByte(plusCodeAlphabet[latitudeIndex])
		code.WriteByte(plusCodeAlphabet[longitudeIndex])
		latitude -= float64(latitudeIndex) * resolution
		longitude -= float64(longitudeIndex) * resolution
	}
	return code.String()
}

const plusCodeAlphabet = "23456789CFGHJMPQRVWX"
