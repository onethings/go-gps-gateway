package helper

// KnotsFromKph converts km/h to knots, matching UnitsConverter.knotsFromKph.
func KnotsFromKph(kph float64) float64 { return kph / 1.852 }

// MpsFromKph converts km/h to m/s.
func MpsFromKph(kph float64) float64 { return kph / 3.6 }
