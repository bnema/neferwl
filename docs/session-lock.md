# Session locking

NeferWL supports `ext-session-lock-v1` with an external locker such as swaylock or hyprlock. The locker checks the password; NeferWL hides the desktop and blocks input.

## While locked

- Only the locker gets input. Binds, clipboard requests and new captures are refused.
- Outputs show black and the locker's surfaces only.
- Running captures stop.
- `Ctrl+Alt+Backspace` is disabled. VT switching still works.
- A locker that uses a screenshot as background (hyprlock `screenshot`, swaylock-effects `--screenshots`) must be in the [capture allowlist](capture.md#the-allowlist).

The locker is told `locked` only once every output shows a protected frame or is off.

## If the locker dies

The session stays locked and black. Start a new locker to take over. There is no emergency unlock key and no timeout.

A locker that exits right after unlocking must do a Wayland roundtrip first, or its exit counts as a crash and the session stays locked.

## Limits

- NeferWL does not authenticate: no PAM, no stored credentials.
- Any process running as your user can start its own locker and unlock.
- Clipboard data and window contents a client received before the lock stay with that client.
- The [state file](desktop.md#state-for-scripts) is not cleared while locked.
