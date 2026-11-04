# airpods-core

Go library for the Pico 2 W AirPods USB dongle. It is the shared protocol code of two apps:

- [airpods-cli](https://github.com/Dardrai/airpods-cli), the command line tool
- [airpods-gui](https://github.com/Dardrai/airpods-gui), the tray app and settings window

| Package | What it does |
|---|---|
| `hid` | Opens and lists dongles on Linux and Windows, and exchanges feature reports |
| `dongle` | Status, control commands, settings, sessions, kept reports and diagnostics |
| `dongle/sim` | A simulated dongle for tests and for trying an app without hardware |
| `disconnect` | Ends the AirPods connection while keeping them paired, on dongles that can |

It is pure Go and depends only on `golang.org/x/sys`.

## Credits

The apps and this library build on the work of [NTPods](https://github.com/Ripthulhu/ntpods) and
[LibrePods](https://github.com/librepods-org/librepods).

Not affiliated with or endorsed by Apple. AirPods is a trademark of Apple Inc.

## License

GPL-3.0. See [LICENSE](LICENSE).
