// Package geofence decides whether a position lies inside a Traccar geofence
// area. It ports org.traccar.geofence.GeofenceGeometry, GeofencePolygon and
// GeofenceCircle, including the "latitude longitude" ordering and the
// antimeridian handling of the Java implementation.
package geofence

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"traccar-server_go/go_server/internal/model"
)

// earthRadius matches DistanceCalculator.EQUATORIAL_EARTH_RADIUS.
const earthRadius = 6378137.0

const degToRad = math.Pi / 180

// Area is a two dimensional geofence shape.
type Area interface {
	// Contains reports whether the coordinate is inside the area.
	Contains(latitude, longitude float64) bool
	// WKT renders the area in the Traccar WKT dialect and round-trips Parse.
	WKT() string
}

// Geofence is a named area plus the altitude limits Traccar keeps in the
// attributes map (floor / ceiling, both in metres).
type Geofence struct {
	ID         int64
	Name       string
	Area       Area
	Attributes map[string]any
}

// Contains reports whether the position is inside the area and within the
// optional floor / ceiling altitude range.
func (g *Geofence) Contains(position *model.Position) bool {
	if g == nil || g.Area == nil || position == nil {
		return false
	}
	if floor := numberAttribute(g.Attributes, "floor"); floor != 0 && position.Altitude < floor {
		return false
	}
	if ceiling := numberAttribute(g.Attributes, "ceiling"); ceiling != 0 && position.Altitude > ceiling {
		return false
	}
	return g.Area.Contains(position.Latitude, position.Longitude)
}

// Parse builds an area from the WKT the Java server stores in geofence areas.
// Supported forms are
//
//	POLYGON ((lat lon, lat lon, ...))
//	CIRCLE (lat lon, radius-in-metres)
func Parse(wkt string) (Area, error) {
	trimmed := strings.TrimSpace(wkt)
	switch {
	case strings.HasPrefix(trimmed, "POLYGON"):
		return parsePolygon(trimmed)
	case strings.HasPrefix(trimmed, "CIRCLE"):
		return parseCircle(trimmed)
	default:
		return nil, fmt.Errorf("geofence: unknown geometry type in %q", wkt)
	}
}

// Polygon is a closed ring of coordinates. The edge coefficients are
// precomputed once, like GeofencePolygon does, so the per position test is a
// single pass over the ring.
type Polygon struct {
	coordinates []Coordinate
	constants   []float64
	multiples   []float64
	normalize   bool
	min         Coordinate
	max         Coordinate
}

// Coordinate is a WGS84 point; the Java print order is latitude then
// longitude, which is the opposite of the WKT standard.
type Coordinate struct {
	Latitude  float64
	Longitude float64
}

// NewPolygon builds a polygon from at least three coordinates.
func NewPolygon(coordinates []Coordinate) (*Polygon, error) {
	if len(coordinates) < 3 {
		return nil, fmt.Errorf("geofence: polygon needs at least 3 coordinates, got %d", len(coordinates))
	}

	p := &Polygon{coordinates: append([]Coordinate(nil), coordinates...)}

	p.min, p.max = p.coordinates[0], p.coordinates[0]
	hasNegative, hasPositive := false, false
	for _, coordinate := range p.coordinates {
		if coordinate.Longitude > 90 {
			hasPositive = true
		} else if coordinate.Longitude < -90 {
			hasNegative = true
		}
		p.min.Latitude = math.Min(p.min.Latitude, coordinate.Latitude)
		p.min.Longitude = math.Min(p.min.Longitude, coordinate.Longitude)
		p.max.Latitude = math.Max(p.max.Latitude, coordinate.Latitude)
		p.max.Longitude = math.Max(p.max.Longitude, coordinate.Longitude)
	}
	p.normalize = hasPositive && hasNegative

	corners := len(p.coordinates)
	p.constants = make([]float64, corners)
	p.multiples = make([]float64, corners)

	// Iteration k uses the edge (k-1, k) and stores its coefficients at k,
	// which is exactly the Java `for (i = 0; j = corners - 1; i < corners; j = i++)`.
	for i, j := 0, corners-1; i < corners; j, i = i, i+1 {
		lonI := p.normalizeLongitude(p.coordinates[i].Longitude)
		lonJ := p.normalizeLongitude(p.coordinates[j].Longitude)
		latI := p.coordinates[i].Latitude
		latJ := p.coordinates[j].Latitude

		if lonI == lonJ {
			p.constants[i] = p.coordinates[i].Longitude
			p.multiples[i] = 0
			continue
		}
		p.constants[i] = latI - (lonI*latJ)/(lonJ-lonI) + (lonI*latI)/(lonJ-lonI)
		p.multiples[i] = (latJ - latI) / (lonJ - lonI)
	}

	return p, nil
}

func (p *Polygon) normalizeLongitude(longitude float64) float64 {
	if p.normalize && longitude < -90 {
		return longitude + 360
	}
	return longitude
}

// Contains implements Area.
func (p *Polygon) Contains(latitude, longitude float64) bool {
	if !p.intersectsBoundary(latitude, latitude, longitude, longitude) {
		return false
	}
	return p.containsPoint(latitude, longitude)
}

func (p *Polygon) intersectsBoundary(minLatitude, maxLatitude, minLongitude, maxLongitude float64) bool {
	return boxIntersects(
		p.min.Latitude, p.min.Longitude, p.max.Latitude, p.max.Longitude,
		minLatitude, maxLatitude, minLongitude, maxLongitude)
}

func (p *Polygon) containsPoint(latitude, longitude float64) bool {
	corners := len(p.coordinates)
	longitudeNorm := p.normalizeLongitude(longitude)

	oddNodes := false
	for i, j := 0, corners-1; i < corners; j, i = i, i+1 {
		lonI := p.normalizeLongitude(p.coordinates[i].Longitude)
		lonJ := p.normalizeLongitude(p.coordinates[j].Longitude)
		if lonI < longitudeNorm && lonJ >= longitudeNorm || lonJ < longitudeNorm && lonI >= longitudeNorm {
			oddNodes = oddNodes != (longitudeNorm*p.multiples[i]+p.constants[i] < latitude)
		}
	}
	return oddNodes
}

// WKT implements Area.
func (p *Polygon) WKT() string {
	var builder strings.Builder
	builder.WriteString("POLYGON ((")
	for _, coordinate := range p.coordinates {
		builder.WriteString(formatCoordinate(coordinate.Latitude))
		builder.WriteByte(' ')
		builder.WriteString(formatCoordinate(coordinate.Longitude))
		builder.WriteString(", ")
	}
	return builder.String()[:builder.Len()-2] + "))"
}

// Circle is a centre point with a radius in metres.
type Circle struct {
	CenterLatitude  float64
	CenterLongitude float64
	Radius          float64

	minLatitude  float64
	minLongitude float64
	maxLatitude  float64
	maxLongitude float64
}

// NewCircle builds a circle from a centre and a radius in metres.
func NewCircle(latitude, longitude, radius float64) *Circle {
	c := &Circle{CenterLatitude: latitude, CenterLongitude: longitude, Radius: radius}

	latitudeDelta := radius / 111320
	longitudeDelta := radius / (111320 * math.Max(math.Cos(latitude*degToRad), 1e-6))
	c.minLatitude = latitude - latitudeDelta
	c.maxLatitude = latitude + latitudeDelta
	c.minLongitude = longitude - longitudeDelta
	c.maxLongitude = longitude + longitudeDelta
	return c
}

// Contains implements Area.
func (c *Circle) Contains(latitude, longitude float64) bool {
	if !boxIntersects(
		c.minLatitude, c.minLongitude, c.maxLatitude, c.maxLongitude,
		latitude, latitude, longitude, longitude) {
		return false
	}
	return distance(c.CenterLatitude, c.CenterLongitude, latitude, longitude) <= c.Radius
}

// WKT implements Area.
func (c *Circle) WKT() string {
	return "CIRCLE (" + formatCoordinate(c.CenterLatitude) + " " + formatCoordinate(c.CenterLongitude) +
		", " + formatCoordinate(c.Radius) + ")"
}

// boxIntersects is GeofenceGeometry.intersectsBoundary: when the box spans the
// antimeridian the cheap latitude/longitude test is skipped entirely.
func boxIntersects(
	minLatitude, minLongitude, maxLatitude, maxLongitude,
	testMinLatitude, testMaxLatitude, testMinLongitude, testMaxLongitude float64,
) bool {
	if minLongitude >= 0 || maxLongitude < 0 {
		if testMaxLatitude < minLatitude || testMinLatitude > maxLatitude {
			return false
		}
		if testMaxLongitude < minLongitude || testMinLongitude > maxLongitude {
			return false
		}
	}
	return true
}

// distance is DistanceCalculator.distance: a haversine on the equatorial
// radius, in metres.
func distance(lat1, lon1, lat2, lon2 float64) float64 {
	deltaLon := (lon2 - lon1) * degToRad
	deltaLat := (lat2 - lat1) * degToRad
	sinLat := math.Sin(deltaLat / 2)
	sinLon := math.Sin(deltaLon / 2)
	a := sinLat*sinLat + math.Cos(lat1*degToRad)*math.Cos(lat2*degToRad)*sinLon*sinLon
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
	return earthRadius * c
}

func parsePolygon(wkt string) (*Polygon, error) {
	content, err := innerContent(wkt, "POLYGON")
	if err != nil {
		return nil, err
	}

	parts := strings.Split(content, ",")
	if len(parts) < 3 {
		return nil, fmt.Errorf("geofence: polygon needs at least 3 coordinates, got %d", len(parts))
	}

	coordinates := make([]Coordinate, 0, len(parts))
	for _, part := range parts {
		coordinate, err := parseCoordinate(part)
		if err != nil {
			return nil, fmt.Errorf("geofence: polygon coordinate %q: %w", strings.TrimSpace(part), err)
		}
		coordinates = append(coordinates, coordinate)
	}
	return NewPolygon(coordinates)
}

func parseCircle(wkt string) (*Circle, error) {
	content, err := innerContent(wkt, "CIRCLE")
	if err != nil {
		return nil, err
	}

	parts := strings.Split(content, ",")
	if len(parts) != 2 {
		return nil, fmt.Errorf("geofence: circle needs a centre and a radius, got %q", content)
	}

	center, err := parseCoordinate(parts[0])
	if err != nil {
		return nil, fmt.Errorf("geofence: circle centre %q: %w", strings.TrimSpace(parts[0]), err)
	}
	radius, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return nil, fmt.Errorf("geofence: circle radius %q: %w", strings.TrimSpace(parts[1]), err)
	}
	if radius <= 0 {
		return nil, fmt.Errorf("geofence: circle radius must be positive, got %v", radius)
	}
	return NewCircle(center.Latitude, center.Longitude, radius), nil
}

// innerContent returns the text between the outermost parentheses, so both
// "POLYGON ((...))" and "POLYGON((...))" work.
func innerContent(wkt, geometryType string) (string, error) {
	open := strings.Index(wkt, "(")
	closing := strings.LastIndex(wkt, ")")
	if open < 0 || closing <= open {
		return "", fmt.Errorf("geofence: %s has no parenthesis block: %q", geometryType, wkt)
	}
	content := strings.TrimSpace(wkt[open+1 : closing])
	content = strings.TrimSpace(strings.TrimPrefix(content, "("))
	content = strings.TrimSpace(strings.TrimSuffix(content, ")"))
	if content == "" {
		return "", fmt.Errorf("geofence: %s has no content: %q", geometryType, wkt)
	}
	return content, nil
}

func parseCoordinate(text string) (Coordinate, error) {
	fields := strings.Fields(text)
	if len(fields) != 2 {
		return Coordinate{}, fmt.Errorf("expected two values, got %d", len(fields))
	}
	latitude, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return Coordinate{}, fmt.Errorf("latitude: %w", err)
	}
	longitude, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return Coordinate{}, fmt.Errorf("longitude: %w", err)
	}
	return Coordinate{Latitude: latitude, Longitude: longitude}, nil
}

func formatCoordinate(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// numberAttribute reads a numeric attribute that JSON decoding may have left
// as float64, int or a string.
func numberAttribute(attributes map[string]any, key string) float64 {
	switch value := attributes[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
