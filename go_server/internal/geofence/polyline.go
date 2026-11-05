package geofence

import (
	"fmt"
	"math"
	"strings"
)

// DefaultPolylineDistance is the metres a position may sit from a LINESTRING
// and still count as inside it. It matches the default of
// Geofence.getDouble("polylineDistance", 25.0).
const DefaultPolylineDistance = 25.0

// Polyline is an open line: a position is inside when it lies within Distance
// metres of any segment. It ports GeofencePolyline, including the proximity
// rule and the segment crossing test.
type Polyline struct {
	coordinates []Coordinate
	// Distance is the width of the corridor around the line, in metres.
	Distance float64
}

// NewPolyline builds a line from at least two coordinates. A non-positive
// distance keeps the Java behaviour, where the stored value is used as is and
// nothing is ever inside the line.
func NewPolyline(coordinates []Coordinate, distance float64) (*Polyline, error) {
	if len(coordinates) < 2 {
		return nil, fmt.Errorf("geofence: polyline needs at least 2 coordinates, got %d", len(coordinates))
	}
	return &Polyline{
		coordinates: append([]Coordinate(nil), coordinates...),
		Distance:    distance,
	}, nil
}

// Coordinates returns a copy of the line, for callers that need to render or
// measure it.
func (p *Polyline) Coordinates() []Coordinate {
	return append([]Coordinate(nil), p.coordinates...)
}

// Contains reports whether the coordinate is within Distance metres of the
// line.
func (p *Polyline) Contains(latitude, longitude float64) bool {
	for i := 1; i < len(p.coordinates); i++ {
		previous := p.coordinates[i-1]
		current := p.coordinates[i]
		if distanceToLine(latitude, longitude,
			previous.Latitude, previous.Longitude,
			current.Latitude, current.Longitude) <= p.Distance {
			return true
		}
	}
	return false
}

// WKT renders the line in the dialect Parse accepts.
func (p *Polyline) WKT() string {
	var builder strings.Builder
	builder.WriteString("LINESTRING (")
	for index, coordinate := range p.coordinates {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(formatCoordinate(coordinate.Latitude))
		builder.WriteByte(' ')
		builder.WriteString(formatCoordinate(coordinate.Longitude))
	}
	builder.WriteByte(')')
	return builder.String()
}

// CrossesSegment implements SegmentCrosser. Any intersection with one of the
// line legs counts, the same rule intersectsEdges applies to the Java polyline.
func (p *Polyline) CrossesSegment(fromLatitude, fromLongitude, toLatitude, toLongitude float64) bool {
	for i := 1; i < len(p.coordinates); i++ {
		previous := p.coordinates[i-1]
		current := p.coordinates[i]
		if segmentsIntersect(
			fromLatitude, fromLongitude, toLatitude, toLongitude,
			previous.Latitude, previous.Longitude,
			current.Latitude, current.Longitude,
		) {
			return true
		}
	}
	return false
}

// DistanceTo reports the shortest distance from a coordinate to the line, in
// metres. Reports use it for the polyline attribute.
func (p *Polyline) DistanceTo(latitude, longitude float64) float64 {
	shortest := math.Inf(1)
	for i := 1; i < len(p.coordinates); i++ {
		previous := p.coordinates[i-1]
		current := p.coordinates[i]
		candidate := distanceToLine(latitude, longitude,
			previous.Latitude, previous.Longitude,
			current.Latitude, current.Longitude)
		if candidate < shortest {
			shortest = candidate
		}
	}
	return shortest
}

// distanceToLine is DistanceCalculator.distanceToLine. The point is projected
// onto the segment when it falls beside it, and the nearer endpoint is used
// when the projection falls beyond either end.
func distanceToLine(pointLatitude, pointLongitude, fromLatitude, fromLongitude, toLatitude, toLongitude float64) float64 {
	d0 := distance(pointLatitude, pointLongitude, fromLatitude, fromLongitude)
	d1 := distance(fromLatitude, fromLongitude, toLatitude, toLongitude)
	d2 := distance(toLatitude, toLongitude, pointLatitude, pointLongitude)

	d0Squared := d0 * d0
	d1Squared := d1 * d1
	d2Squared := d2 * d2
	if d0Squared > d1Squared+d2Squared {
		return d2
	}
	if d2Squared > d1Squared+d0Squared {
		return d0
	}

	halfPerimeter := (d0 + d1 + d2) * 0.5
	triangleArea := math.Sqrt(halfPerimeter * (halfPerimeter - d0) * (halfPerimeter - d1) * (halfPerimeter - d2))
	if d1 == 0 {
		return d0
	}
	return 2 * triangleArea / d1
}

// parsePolyline reads the LINESTRING text and the corridor width that the
// geofence attributes carry.
func parsePolyline(wkt string, distance float64) (*Polyline, error) {
	content, err := innerContent(wkt, "LINESTRING")
	if err != nil {
		return nil, err
	}

	parts := strings.Split(content, ",")
	coordinates := make([]Coordinate, 0, len(parts))
	for _, part := range parts {
		coordinate, err := parseCoordinate(part)
		if err != nil {
			return nil, fmt.Errorf("geofence: polyline coordinate %q: %w", strings.TrimSpace(part), err)
		}
		coordinates = append(coordinates, coordinate)
	}
	return NewPolyline(coordinates, distance)
}
