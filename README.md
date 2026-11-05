# Go-Based GPS Tracking Gateway (Unofficial Traccar Core Port)

> **Disclaimer:** This project is an independent, community-driven, and AI-assisted (DeepSeek V4 Flash) rewrite of core GPS tracking protocol decoders in Go. **It is NOT an official Traccar project.** It is entirely inspired by and cross-referenced with the official [Traccar](https://github.com/traccar/traccar) repository (v6.17.0) to provide lightweight, high-performance protocol decoding and gateway capabilities in the Go ecosystem.

---

## 📌 Why Go?

To significantly **minimize server resource consumption and operational costs** while handling thousands of concurrent TCP/UDP device connections with Go's lightweight concurrency model (Goroutines).

### Supported Protocols

| Family | Protocols |
| --- | --- |
| Binary | **Teltonika** (Codec 8 / 8E / 12 / 13), **GT06** (Concox), **JT808**, **JT1078** video (→ HLS), **H02**, **GT02**, **GPS056**, **Gator**, **Startek**, **Ruptela**, **Suntech**, **Meitrack** |
| Text | **GPS103**, **TK103**, **Wondex** |
| IP / HTTP | **OsmAnd**, **MQTT** |

### Features

- **Gateway**: TCP / UDP / TLS / HTTP listeners, command dispatch, batch position storage.
- **REST API**: devices, positions, events, users, permissions, geofences, commands, WebSocket, media.
- **Frontend**: a traccar-web build is served same-origin by the gateway.
- **Storage**: PostgreSQL, MySQL 8.0+, SQLite; `database.schema=reference` attaches an existing
  Traccar `tc_devices` / `tc_positions` database.

See [go_server/README.md](go_server/README.md) for configuration, deployment (Ubuntu / Docker) and tests.

---

## 🚀 Installation & Testing (Ubuntu)

For quick testing and deployment on Ubuntu, check out the release repository:
👉 [tra-server-go-releases-fortesting](https://github.com/onethings/tra-server-go-releases-fortesting)

---

## 📁 Repository Structure

```text
├── LICENSE                # Apache License 2.0
└── go_server/             # Primary Go implementation (decoders, gateway, storage)
    ├── cmd/               # traccar-gateway, device-sim
    ├── internal/          # Protocol decoders, gateway, storage, api, alert, notify, etc.
    ├── deploy/            # Ubuntu installer, systemd unit, nginx template
    ├── web/               # traccar-web build served by the gateway
    └── README.md          # Installation, usage, and test instructions
```
