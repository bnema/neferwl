# Session locking

NeferWL supports `ext-session-lock-v1` with an external locker such as swaylock or hyprlock. The locker checks the password; NeferWL blocks input and hides the desktop. A fullscreen window or a layer-shell cover is not a lock.

## While locked

- Only the locker gets input. Binds, activation, virtual keyboards, input-method grabs, clipboard requests and new captures are refused.
- Outputs show only black and the locker's surfaces. Direct scanout, overlays, tearing and hardware cursors are off.
- Every running capture session stops and the capture indicator clears. Clients start new sessions after unlock.
- Keys held during lock or unlock do not reach the desktop afterwards.
- `Ctrl+Alt+Backspace` and the `--debug=input-keys` log are disabled. VT switching still works.
- A second locker is refused while the first is alive. Activity can wake outputs but never unlocks.
- A locker that uses a screenshot as background (hyprlock `screenshot`, swaylock-effects `--screenshots`) must be in the [capture allowlist](capture.md#the-allowlist), and capture is refused once locked.

The locker gets `locked` only once every output really shows a protected frame (or is off), DRM leases are revoked and captures started before the lock have finished. If that cannot be confirmed, `locked` is withheld.

## If the locker dies

The session stays locked and outputs stay black. Start a new locker to take over. There is no emergency unlock key and no unlock timeout.

Only the current locker, after `locked`, can unlock. A locker that exits right after unlocking must do a Wayland roundtrip first; otherwise its exit may count as a crash and keep the session locked.

## Limits

- NeferWL does not authenticate anything: no PAM, no stored credentials.
- A process running as your user can start its own locker and unlock. Root, the kernel and other VTs are outside the compositor's control.
- Clipboard data and window contents a client received before the lock stay with that client.
- The state file for scripts is not cleared while locked.
- A stalled GPU or KMS call delays `locked`; lock latency is not guaranteed.
- If KMS refuses a protected frame, the output stays registered and retries every 50 ms to 2 s. If the driver refuses to turn an output off, its old content can stay lit, and `locked` is withheld.

## Testing

Headless tests cover two outputs, input isolation, capture refusal, locker death and takeover, and unlock. KMS behaviour is tested with mocks only; hotplug, DPMS, VT switching and suspend still need testing on real hardware.
