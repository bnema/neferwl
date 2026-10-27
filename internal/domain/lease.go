package domain

// LeaseConnector identifies a non-desktop connector available for DRM leasing.
type LeaseConnector struct {
	Card, Name, Description string
	ConnectorID             uint32
}
