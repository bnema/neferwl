package xwm

import "bnema.dev/xturbo"

func (x *XWM) onFocusIn(ev *xturbo.Event) { _ = ev }

// Focus gives keyboard focus to s; 0 means a native Wayland window or none.
func (x *XWM) Focus(s Slot) { _ = s }

// CloseWindow asks s to close (WM_DELETE_WINDOW), or kills its client.
func (x *XWM) CloseWindow(s Slot) { _ = s }
