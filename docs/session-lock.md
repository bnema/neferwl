# Session locking

NeferWL supports `ext-session-lock-v1` with an external Wayland locker. The locker owns authentication and requests unlock after it succeeds; NeferWL owns input isolation and output protection. A fullscreen window, layer-shell cover or fade is not a session lock.

## Protection

NeferWL accepts one live lock owner. From acceptance, it refuses desktop input, binds, activation, virtual-keyboard injection, input-method grabs, clipboard requests and capture admission. Queued desktop input from an earlier transition is discarded rather than relabelled. Protected scenes contain only an opaque compositor-owned black background and the owner's validated lock surfaces. Direct scanout, overlays, tearing and hardware cursors are disabled while protected.

The locker receives `locked` only after every registered output lifetime has affirmative evidence of a protected frame or actual inactivity, the backend has revoked outstanding DRM leases, and admitted prelock captures have finished writing their destination buffers. Queued rendering, a timeout, renderer failure and loss of DRM master are not protection evidence. A lease that cannot be revoked keeps `locked` withheld.

Lock surfaces receive exact logical dimensions through configure/acknowledge. Outputs without a mapped lock surface remain black. An output added during protection starts safe-first; stale frames or proofs from an earlier lock or connector lifetime cannot authorize it. Resume and recovery require fresh output protection rather than replaying a desktop framebuffer.

## Owner failure and unlock

Destroying a lock surface, disconnecting the locker or encountering its protocol error does not unlock. NeferWL keeps protection and falls back to black. A new ordinary locker can take over after the previous owner disappears; a second live owner is refused.

Only the current owner, after `locked`, may send `unlock_and_destroy`. A locker exiting afterward must perform a Wayland roundtrip before closing its connection. Closing before the server processes unlock can be treated as owner death, which keeps the session protected. Input held across a transition is cleared or quarantined until released, so password-held keys do not enter the desktop after unlock.

Returning activity may wake powered-off outputs but never unlocks. There is no emergency unlock key or timeout-to-unlock. VT switching remains available; `Ctrl+Alt+Backspace` and the `--debug=input-keys` key log are suppressed while protected.

## Boundaries and limits

- Password/PIN verification belongs to the locker. NeferWL does not implement PAM, store credentials, or authenticate the protocol's owner.
- A same-user process that controls the session or a legitimate lock owner is outside this isolation boundary. Root, hostile kernel/driver code and another active VT are not isolated by the compositor.
- Clipboard pipes already handed to clients before acquisition cannot be recalled. Later receive/set requests are denied, including requests on old offer objects. Information a client received before locking cannot be erased from that client.
- Captures admitted before acquisition may complete; `locked` waits for their copy/FD-write completion even if the requesting protocol object has been destroyed. New captures, including private recording and hidden-workspace capture, are denied.
- The script state file is not a security API and is not scrubbed by locking. Its existing file permissions and same-user access boundary remain unchanged.
- Native KMS calls and compositor GPU work can stall. Stalls never produce a successful protection acknowledgement; bounded lock latency is not guaranteed. Protected shutdown never restores saved desktop pixels, but disabling an output is best effort if KMS authority is lost.
- Refused or transient protected KMS commits keep the output registered and dark, and retry with a 50 ms to 2 s backoff. An output stops, and the locker loses it, only when the renderer fails to clear it or no image format at all is accepted. If the driver keeps refusing to disable an output, its previous content can stay lit; `locked` is still withheld.

## Verification

Headless integration tests exercise two outputs, bufferless acquisition, locker input isolation, capture refusal, owner death/takeover and synchronized unlock. Headless inactivity means the software output is off; there is no physical display. Protected headless runs do not update debug PNG files or read back protected content; an existing PNG remains an earlier unlocked image, not a live protected screenshot.

KMS tests use generated mocks to verify detachment, black clearing, fence ordering, failure handling and shutdown. They do not prove physical behavior on every GPU. Multi-monitor hotplug, DPMS, VT/resume and suspend require separately authorized isolated hardware testing before making hardware-specific guarantees.
