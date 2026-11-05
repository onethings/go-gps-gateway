// Package geofence decides whether a position lies inside a Traccar geofence
// area. It ports org.traccar.geofence.GeofenceGeometry, GeofencePolygon,
// GeofenceCircle and GeofencePolyline, including the "latitude longitude"
// ordering and the antimeridian handling of the Java implementation.
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
	ID          int64
	CalendarID  int64
	Name        string
	Description string
	Area        Area
	Attributes  model.Attributes
}

// WKT renders the stored area, which is what the geofences.area column keeps.
// An empty string means the row cannot be stored; callers parse the text back
// with Parse before they build the struct.
func (g *Geofence) WKT() string {
	if g == nil || g.Area == nil {
		return ""
	}
	return g.Area.WKT()
}

// Contains reports whether the position is inside the area and within the
// optional floor / ceiling altitude range.
func (g *Geofence) Contains(position *model.Position) bool {
	if g == nil || g.Area == nil || position == nil {
		return false
	}
	if floor := g.Attributes.GetDouble("floor", 0); floor != 0 && position.Altitude < floor {
		return false
	}
	if ceiling := g.Attributes.GetDouble("ceiling", 0); ceiling != 0 && position.Altitude > ceiling {
		return false
	}
	return g.Area.Contains(position.Latitude, position.Longitude)
}

// PolylineDistance is the corridor width a LINESTRING uses. It reads the
// polylineDistance attribute the way Geofence.getGeometry does, so an absent
// attribute means DefaultPolylineDistance.
func (g *Geofence) PolylineDistance() float64 {
	if g == nil || g.Attributes == nil {
		return DefaultPolylineDistance
	}
	return g.Attributes.GetDouble("polylineDistance", DefaultPolylineDistance)
}

// Parse builds an area from the WKT the Java server stores in geofence areas.
// Supported forms are
//
//	POLYGON ((lat lon, lat lon, ...))
//	CIRCLE (lat lon, radius-in-metres)
//	LINESTRING (lat lon, lat lon, ...)
//
// A line uses DefaultPolylineDistance as its corridor width. Use
// ParseWithDistance when the geofence carries a polylineDistance attribute.
func Parse(wkt string) (Area, error) {
	return ParseWithDistance(wkt, DefaultPolylineDistance)
}

// ParseWithDistance is Parse for callers that know the polyline corridor width,
// which the Java server reads from the geofence attributes. The distance is
// ignored by the other geometry types.
func ParseWithDistance(wkt string, polylineDistance float64) (Area, error) {
	trimmed := strings.TrimSpace(wkt)
	switch {
	case strings.HasPrefix(trimmed, "POLYGON"):
		return parsePolygon(trimmed)
	case strings.HasPrefix(trimmed, "CIRCLE"):
		return parseCircle(trimmed)
	case strings.HasPrefix(trimmed, "LINESTRING"):
		return parsePolyline(trimmed, polylineDistance)
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

// SegmentCrosser reports whether the straight line between two coordinates
// crosses the area. The Java server uses GeofencePolygon.containsPoint on the
// segment endpoints plus a segment/edge intersection test; only the polygon and
// the circle implement it, because a shape without an interior has nothing to
// cross.
type SegmentCrosser interface {
	CrossesSegment(fromLatitude, fromLongitude, toLatitude, toLongitude float64) bool
}

// CrossesSegment reports whether moving from one position to the other crosses
// the geofence boundary. It is nil-safe, so a device without a previous
// position simply never raises geofenceCrossed.
func (g *Geofence) CrossesSegment(from, to *model.Position) bool {
	if g == nil || g.Area == nil || from == nil || to == nil {
		return false
	}
	crosser, ok := g.Area.(SegmentCrosser)
	if !ok {
		return false
	}
	return crosser.CrossesSegment(
		from.Latitude, from.Longitude, to.Latitude, to.Longitude)
}

// CrossesSegment implements SegmentCrosser. The segment must intersect one of
// the ring edges; a segment that only touches a vertex counts as crossing,
// which is the conservative choice for an alert.
func (p *Polygon) CrossesSegment(fromLatitude, fromLongitude, toLatitude, toLongitude float64) bool {
	if !p.intersectsBoundary(
		math.Min(fromLatitude, toLatitude), math.Max(fromLatitude, toLatitude),
		math.Min(fromLongitude, toLongitude), math.Max(fromLongitude, toLongitude)) {
		return false
	}

	corners := len(p.coordinates)
	for i, j := 0, corners-1; i < corners; j, i = i, i+1 {
		if segmentsIntersect(
			fromLatitude, fromLongitude, toLatitude, toLongitude,
			p.coordinates[j].Latitude, p.coordinates[j].Longitude,
			p.coordinates[i].Latitude, p.coordinates[i].Longitude,
		) {
			return true
		}
	}
	return false
}

// CrossesSegment implements SegmentCrosser. A circle is crossed when the
// segment comes within the radius while both endpoints are outside it.
func (c *Circle) CrossesSegment(fromLatitude, fromLongitude, toLatitude, toLongitude float64) bool {
	if !boxIntersects(
		c.minLatitude, c.minLongitude, c.maxLatitude, c.maxLongitude,
		math.Min(fromLatitude, toLatitude), math.Max(fromLatitude, toLatitude),
		math.Min(fromLongitude, toLongitude), math.Max(fromLongitude, toLongitude)) {
		return false
	}
	closest := closestPointOnSegment(
		fromLatitude, fromLongitude, toLatitude, toLongitude,
		c.CenterLatitude, c.CenterLongitude,
	)
	if distance(c.CenterLatitude, c.CenterLongitude, closest[0], closest[1]) > c.Radius {
		return false
	}
	// Both endpoints outside and the segment touching the disc is a crossing;
	// an endpoint inside is an enter or exit, which the membership state
	// already reports.
	return !c.Contains(fromLatitude, fromLongitude) && !c.Contains(toLatitude, toLongitude)
}

// segmentsIntersect is the standard orientation test on the plane.
func segmentsIntersect(ax, ay, bx, by, cx, cy, dx, dy float64) bool {
	d1 := orientation(cx, cy, dx, dy, ax, ay)
	d2 := orientation(cx, cy, dx, dy, bx, by)
	d3 := orientation(ax, ay, bx, by, cx, cy)
	d4 := orientation(ax, ay, bx, by, dx, dy)

	if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
		((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
		return true
	}
	if d1 == 0 && onSegment(cx, cy, dx, dy, ax, ay) {
		return true
	}
	if d2 == 0 && onSegment(cx, cy, dx, dy, bx, by) {
		return true
	}
	if d3 == 0 && onSegment(ax, ay, bx, by, cx, cy) {
		return true
	}
	if d4 == 0 && onSegment(ax, ay, bx, by, dx, dy) {
		return true
	}
	return false
}

func orientation(ax, ay, bx, by, cx, cy float64) float64 {
	return (bx-ax)*(cy-ay) - (by-ay)*(cx-ax)
}

// onSegment reports whether c, known to be collinear with a and b, lies within
// the axis aligned box of the segment.
func onSegment(ax, ay, bx, by, cx, cy float64) bool {
	return cx >= math.Min(ax, bx) && cx <= math.Max(ax, bx) &&
		cy >= math.Min(ay, by) && cy <= math.Max(ay, by)
}

// closestPointOnSegment projects a point onto a segment in the flat
// latitude/longitude plane, which is accurate enough for the short distances a
// proximity check deals with.
func closestPointOnSegment(ax, ay, bx, by, px, py float64) [2]float64 {
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return [2]float64{ax, ay}
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return [2]float64{ax + t*dx, ay + t*dy}
}
