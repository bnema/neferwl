package ports

import "os"

// LeaseConnector identifies a non-desktop connector available for DRM leasing.
type LeaseConnector struct {
	Card, Name, Description string
	ConnectorID             uint32
}

// LeaseMessage is an adapter-to-adapter DRM lease channel message.
type LeaseMessage interface{ leaseMessage() }

// LeaseConnectors replaces the available connector inventory for Card.
// Device is a non-master DRM fd owned by the receiver, which closes it.
// An empty Connectors list with nil Device removes the card's global and closes
// the display's fd. A later nonempty inventory creates a new global.
type LeaseConnectors struct {
	Card       string
	Device     *os.File
	Connectors []LeaseConnector
}

func (LeaseConnectors) leaseMessage() {}

// CloseLeaseFiles closes the fd a lease message owns, for a message that is
// dropped instead of delivered.
func CloseLeaseFiles(msg LeaseMessage) {
	switch m := msg.(type) {
	case LeaseConnectors:
		if m.Device != nil {
			m.Device.Close()
		}
	case LeaseReply:
		if m.FD != nil {
			m.FD.Close()
		}
	}
}

type LeaseRequest struct {
	ID         uint64
	Card       string
	Connectors []string
}

func (LeaseRequest) leaseMessage() {}

// LeaseReply transfers ownership of FD to Wayland.
type LeaseReply struct {
	ID      uint64
	FD      *os.File
	LeaseID uint32
	Err     error
}

func (LeaseReply) leaseMessage() {}

type LeaseRevoke struct {
	Card    string
	LeaseID uint32
}

func (LeaseRevoke) leaseMessage() {}

type LeaseFinished struct {
	Card    string
	LeaseID uint32
}

func (LeaseFinished) leaseMessage() {}
