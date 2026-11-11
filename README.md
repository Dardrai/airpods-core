# airpods-core

Go library for the Pico 2 W AirPods USB dongle. It is the shared protocol code of two apps:

- [airpods-cli](https://github.com/Dardrai/airpods-cli), the command line tool
- [airpods-gui](https://github.com/Dardrai/airpods-gui), the tray app and settings window

| Package | What it does |
|---|---|
| `hid` | Opens and lists dongles on Linux and Windows, and exchanges feature reports |
| `dongle` | Status, control commands, settings, sessions, kept reports and diagnostics |
| `dongle/sim` | A simulated dongle for tests and for trying an app without hardware |
| `disconnect` | Releases and takes back the AirPods while keeping them paired, on dongles that can |
| `model` | What each AirPods model can do, and which settings and values to offer it |
| `hearing` | Hearing modes and profile, the in-case check, and experimental requests |

It is pure Go and depends only on `golang.org/x/sys`.

## Dongle firmware

The protocol is the one documented by the [dongle firmware](https://github.com/Ripthulhu/airpods-dongle),
in its `docs/USB-CONTROL.md` and `docs/CONTROL-EXTENSIONS.md`. The library follows firmware 0.1.66.
Nothing in the status report tells firmware versions from 0.1.62 on apart; the newer requests below
are recognised by how the dongle answers them.

Each dongle lists what it can do in the capability bits of its status report. `dongle.CapabilityNames`
names them:

| Bit | Name | Meaning |
|---|---|---|
| 0x001 | `control` | Reconnect and pair (opcodes 1 and 2) |
| 0x002 | `listening` | ANC and transparency through opcode 3, while the AirPods report a mode |
| 0x004 | `pair-phone` | Phone pairing window (opcode 4) |
| 0x008 | `raw-transport` | Raw ATT and AAP requests to the AirPods |
| 0x010 | `typed-rpc` | Typed requests, settings among them |
| 0x020 | `lease` | The settings workflow lease |
| 0x040 | `per-component` | Battery presence and case source masks |
| 0x080 | `host-volume` | USB host volume authority (not used by this library) |
| 0x100 | `runtime-bootsel` | A BOOTSEL request may interrupt active audio |
| 0x200 | `routing` | Routing handover: release and take back the AirPods (0.1.59 and newer) |

Connections: since firmware 0.1.58 the dongle no longer connects to the AirPods on its own after a link
loss or at startup. An outgoing connection needs a button press, a reconnect (opcode 1) or a routing
acquire. Incoming connections from the saved pair are still accepted, so the AirPods can still
connect on their own. Since 0.1.63 this holds only under the Manual reconnect policy, the default.

Reconnect policy: firmware 0.1.63 and newer keep a reconnect policy across restarts,
`dongle.ReconnectManual` or `dongle.ReconnectAutomatic`. Under Automatic the dongle retries the saved
pair by itself, with growing pauses, while the AirPods are not connected and not released. That can
take them from another device using them, and every attempt moves the routing revision.
`Client.ReconnectPolicy` reads the policy at any time. `Client.SetReconnectPolicy` reads it first and
sends nothing when it is already saved. The dongle saves only while idle: not during playback,
microphone use, connecting or a Game Mode change, and not while any app holds the settings lease,
the caller included. A save the dongle could not verify is `Unconfirmed`, since it may have
persisted, and is never retried. The policy then reads as unsupported until a later save verifies
or the dongle restarts. Older firmware, or a saved policy the dongle cannot read, answers unsupported.

Disconnecting: `disconnect` releases the AirPods through the routing handover and keeps them paired.
It reads the routing state, sends one release quoting its revision and a fresh nonce, and waits up to
5 s for the dongle to close the connection. A release accepted but not closed by then is reported as
pending, not as an error. A release counts as done once the AirPods are released and their audio and
controls are closed; an acquire once the dongle owns the AirPods with audio and controls up and
nothing pending. While it waits, a routing read that no longer carries the request's revision, action
and nonce means another choice replaced it. If the revision moved before the request arrived, nothing
was applied. The library then reads the routing state once and sends one fresh request, but only when
the move was a takeover intent (a reconnect, a button press or an automatic attempt) that has ended.
Another app's handover is reported as a changed intent. An acquire follows a takeover already under way
instead. The dongle refuses a release while the microphone or a call is in use, during a takeover, an
audio change or a Game Mode change, and while any app holds the settings lease. After a release it
declines the AirPods' incoming connections until a reconnect, a button press, an acquire or a restart
of the dongle. Under the Automatic policy a restarted dongle then takes the AirPods back by itself.
Firmware without the `routing` bit has no way to disconnect while keeping the pairing; the pair
command erases it and is never used.

Pairing: the dongle can answer a pair request (opcode 2) as busy after it has already dropped the
AirPods and cleared their pairing, so `Client.Pair` reports a busy answer as `Unconfirmed`.

Allow Off: the dongle forgets an Allow Off write whenever the AirPods connect again. For AirPods Pro 3,
which never report Allow Off, it then refuses mode off and hold modes with off until Allow Off is sent
again. `model.View.NeedsAllowOffResend` says when a write needs that first.

Settings arrive one by one after the AirPods connect, so the first settings read can be partial. A read
refused because the AirPods controls are not ready yet is an `Unsupported` that also matches `dongle.ErrNotReady`.

Firmware 0.1.64 added two settings, `en352-protection` (AirPods Pro 3) and `gain-swipe` (AirPods Pro 2
and 3), both on/off. They appear only in the extended settings snapshot, which `SettingsClient.Read`
asks for. Older firmware calls that request malformed; the client then falls back to the original
snapshot and does not ask again. The dongle writes either setting only once the AirPods have reported it.

Ear detection and Conversation Awareness: since firmware 0.1.65 the dongle acts on these by itself.
While it plays music from USB it pauses when a bud comes out and, if it made that pause, plays again
when the bud goes back in, through the ordinary media keys of its USB interface. When Conversation Awareness starts, it lowers
the music it sends to the AirPods. A host cannot see this happen, cannot tell from the dongle whether
its firmware does it, and cannot switch it off on the dongle. The only switches are the AirPods'
own `ear-detection` and `conversation-awareness` settings.

Hearing: firmware 0.1.64 and newer keep the AirPods' last EQ report and pass an EQ change on only
while they know the model and hold that report. `hearing.ExperimentalSetEQ` changes the report's
values and counts as done only when a new EQ report carries exactly the values sent. Loud Sound
Reduction (AirPods Pro 2 and 3) is found by discovery in every request, never at a fixed handle.
Writing the hearing profile needs hearing assistance set up on the AirPods and leaves Hearing Aid
as it is. Headphone Accommodations and its presets are not supported.

## Credits

The apps and this library build on the work of [NTPods](https://github.com/Ripthulhu/ntpods) and
[LibrePods](https://github.com/Ripthulhu/librepods).

Not affiliated with or endorsed by Apple. AirPods is a trademark of Apple Inc.

## License

GPL-3.0. See [LICENSE](LICENSE).
