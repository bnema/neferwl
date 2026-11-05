package ports

// CaptureIDInternal is the bit set in the ID of captures requested by the
// compositor itself (hint scanning) rather than by a Wayland client. Wayland
// IDs start at 1 and increment, so they never set it. Replies carrying it are
// demultiplexed away from the Wayland consumer.
const CaptureIDInternal uint64 = 1 << 63

// CaptureIDControl marks captures requested over the control socket. It is
// distinct from hint captures and Wayland IDs; replies go to the control sink.
const CaptureIDControl uint64 = 1 << 62
