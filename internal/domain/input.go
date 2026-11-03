package domain

// InputDeviceID identifies a device for the lifetime of an input producer.
// Zero means unspecified; producers assign nonzero IDs and do not reuse them.
type InputDeviceID uint64
