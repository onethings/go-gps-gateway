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

// Network mirrors org.traccar.model.Network.
type Network struct {
	HomeMobileCountryCode int
	HomeMobileNetworkCode int
	RadioType             string
	Carrier               string
	CellTowers            []CellTower
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

// IsEmpty reports whether the network carries no information at all.
func (n *Network) IsEmpty() bool {
	return n == nil || len(n.CellTowers) == 0
}
