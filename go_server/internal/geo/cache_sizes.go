package geo

// Size reports the number of live entries in the service cache. The server
// cache route publishes these counts so an operator can see a cache warming up.

// Size implements the geocoder cache accessor.
func (g *Geocoder) Size() int {
	if g == nil {
		return 0
	}
	return g.cache.Size()
}

// Size reports the cell tower cache size.
func (l *Locator) Size() int {
	if l == nil {
		return 0
	}
	return l.cache.Size()
}

// Size reports the speed limit cache size.
func (s *SpeedLimits) Size() int {
	if s == nil {
		return 0
	}
	return s.cache.Size()
}

// Size reports the map matcher cache size.
func (m *Matcher) Size() int {
	if m == nil {
		return 0
	}
	return m.cache.Size()
}
