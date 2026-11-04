# Go-Based GPS Tracking Gateway (Unofficial Traccar Core Port)

> **Disclaimer:** This project is an independent, community-driven, and AI-assisted (DeepSeek V4 Flash) rewrite of core GPS tracking protocol decoders in Go. **It is NOT an official Traccar project.** It is entirely inspired by and cross-referenced with the official [Traccar](https://github.com/traccar/traccar) repository (v6.17.0) to provide lightweight, high-performance protocol decoding and gateway capabilities in the Go ecosystem.

---

## 📌 Why rewrite  with Go Language 

I want cost down server cost as possible, after asking ai Go language is good for now.

### Supported Protocols

1. **Teltonika** (Codec 8 / 8 Extended)
2. **GT06** (Concox protocol family)
3. **JT808** (Ministry of Transport standard of China)
4. **H02** (Kingneed / GPS103 family)

---

## Install for Testing with Ubuntu
https://github.com/onethings/tra-server-go-releases-fortesting


## 📁 Repository Structure

```text
└── go_server/             # Primary Go implementation (decoders, gateway, storage)
    ├── cmd/               # traccar-gateway, device-sim
    ├── internal/          # protocol decoders, gateway, storage, api, alert, notify, ...
    ├── deploy/            # Ubuntu installer, systemd unit, nginx template
    ├── web/               # traccar-web build served by the gateway
    ├── LICENSE            # Apache License 2.0 (this directory only)
    └── README.md          # Installation, usage, and test instructions
```
