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
in its `docs/USB-CONTROL.md` and `docs/CONTROL-EXTENSIONS.md`. The library follows firmware 0.1.62.

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
connect on their own.

Disconnecting: `disconnect` releases the AirPods through the routing handover and keeps them paired.
It reads the routing state, sends one release quoting its revision and a fresh nonce, and waits up to
5 s for the dongle to close the connection. A release accepted but not closed by then is reported as
pending, not as an error. If another choice moved the routing intent first, nothing is sent again. The
dongle refuses a release while the microphone or a call is in use, during a takeover, an audio change or
a Game Mode change, and while any app holds the settings lease. After a release it declines the AirPods'
incoming connections until a reconnect, a button press or an acquire. Firmware without the `routing`
bit has no way to disconnect while keeping the pairing; the pair command erases it and is never used.

Allow Off: the dongle forgets an Allow Off write whenever the AirPods connect again. For AirPods Pro 3,
which never report Allow Off, it then refuses mode off and hold modes with off until Allow Off is sent
again. `model.View.NeedsAllowOffResend` says when a write needs that first.

Settings arrive one by one after the AirPods connect, so the first settings read can be partial. A read
refused because the AirPods controls are not ready yet is an `Unsupported` that also matches `dongle.ErrNotReady`.

## Credits

The apps and this library build on the work of [NTPods](https://github.com/Ripthulhu/ntpods) and
[LibrePods](https://github.com/librepods-org/librepods).

Not affiliated with or endorsed by Apple. AirPods is a trademark of Apple Inc.

## License

GPL-3.0. See [LICENSE](LICENSE).
