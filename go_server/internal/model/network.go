package model

// CellTower mirrors org.traccar.model.CellTower. Only the fields used by the
// four implemented protocols are modelled.
type CellTower struct {
	RadioType         string
	CellID            int64
	LocationAreaCode  int
	MobileCountryCode int
	MobileNetworkCode int
	SignalStrength    int
}

// NewCellTower builds a cell tower entry with the given identity.
func NewCellTower(mcc, mnc, lac int, cid int64) CellTower {
	return CellTower{
		MobileCountryCode: mcc,
		MobileNetworkCode: mnc,
		LocationAreaCode:  lac,
		CellID:            cid,
	}
}

// NewCellTowerWithRSSI builds a cell tower entry including signal strength.
func NewCellTowerWithRSSI(mcc, mnc, lac int, cid int64, rssi int) CellTower {
	tower := NewCellTower(mcc, mnc, lac, cid)
	tower.SignalStrength = rssi
	return tower
}

// WifiAccessPoint mirrors org.traccar.model.WifiAccessPoint.
type WifiAccessPoint struct {
	MacAddress     string
	SignalStrength int
}

// NewWifiAccessPoint builds one scanned access point.
func NewWifiAccessPoint(macAddress string, signalStrength int) WifiAccessPoint {
	return WifiAccessPoint{MacAddress: macAddress, SignalStrength: signalStrength}
}

// Network mirrors org.traccar.model.Network.
type Network struct {
	HomeMobileCountryCode int
	HomeMobileNetworkCode int
	RadioType             string
	Carrier               string
	CellTowers            []CellTower
	WifiAccessPoints      []WifiAccessPoint
}

// NewNetwork creates an empty network description.
func NewNetwork() *Network {
	return &Network{}
}

// NewNetworkWith creates a network holding the supplied cell towers.
func NewNetworkWith(towers ...CellTower) *Network {
	n := NewNetwork()
	n.CellTowers = append(n.CellTowers, towers...)
	return n
}

// AddCellTower appends a cell tower.
func (n *Network) AddCellTower(tower CellTower) {
	n.CellTowers = append(n.CellTowers, tower)
}

// AddWifiAccessPoint appends a scanned access point.
func (n *Network) AddWifiAccessPoint(accessPoint WifiAccessPoint) {
	n.WifiAccessPoints = append(n.WifiAccessPoints, accessPoint)
}

// IsEmpty reports whether the network carries no information at all.
func (n *Network) IsEmpty() bool {
	return n == nil || len(n.CellTowers) == 0
}
