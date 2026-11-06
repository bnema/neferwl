package domain

// InputDeviceID identifies a device for the lifetime of an input producer.
// Zero means unspecified; producers assign nonzero IDs and do not reuse them.
// Physical producers count up from 1 below VirtualPointerDeviceBase. Synthetic
// producers use the reserved namespaces below, so their IDs can coexist.
type InputDeviceID uint64

const (
	// VirtualPointerDeviceBase starts the virtual-pointer namespace [1<<62, 1<<63).
	VirtualPointerDeviceBase InputDeviceID = 1 << 62
	// ControlDeviceID identifies the shared control input device, outside both
	// physical and virtual-pointer namespaces.
	ControlDeviceID InputDeviceID = 1 << 63
)
