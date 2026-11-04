# traccar-server-go: Go protocol gateway

Go re-implementation of Traccar's device protocols and server: device frame → standard `Position` →
SQL database (PostgreSQL / MySQL / SQLite), plus the matching REST API, alerts / geofences,
notifications, command dispatch and processing chain.

- Each Go file names the Java class it mirrors (Traccar v6.17.0).
- The protocol layer is pure functions (`[]byte` → `*model.Position`); all I/O sits in `internal/gateway`.
- Go 1.22+. A database is needed only for REST / alerts / command history: PostgreSQL, MySQL 8.0+ or
  SQLite, chosen by the scheme of `database.url`.

## Status

- **Protocols**: 12 (H02, GT06, JT808, Teltonika, OsmAnd, MQTT, GPS103, TK103, Meitrack, Wondex,
  Suntech, Ruptela) over TCP / UDP / TLS / HTTP / poll.
- **Pipeline**: all 15 stages — Time / Hemisphere, Geolocation / Geocoder / CopyAttributes /
  EngineHours, ComputedAttributes (`internal/expr`), ProximityRule.
- **REST**: devices / positions / events, accounts and tokens, TOTP, permissions and sharing, resource
  CRUD, commands, WebSocket, statistics, media, HLS, OpenID, LDAP, password self-service.
- **Output**: JSON, URL, MQTT, Redis, Wialon, Network (TCP/UDP), AMQP 0-9-1, Kafka.
- **Scheduling**: expiration, temporary, cleanup, health, reports, deviceInactive, deviceExpiration,
  plus events / positions / commands retention.

> `go build ./...`, `go vet ./...` and `go test ./...` are green; gateway / alert / api / notify also
> run under `-race`.

## Architecture

```
device ──TCP/UDP/TLS/HTTP/poll──▶ gateway.Server (one goroutine per connection)
  FrameDecoder → Decoder → DeviceDirectory → session.Manager → Sink = handler.Chain
    └ business → alert.Pipeline → notify.Dispatcher
    └ terminal → postgres.BatchSink (positions + events)

client ──HTTP/WebSocket──▶ api.Server (RequireAuth: api-key / api_tokens / login token)
```

## Quick start

```bash
cd go_server
make build        # bin/traccar-gateway, bin/device-sim
make test         # go test ./...
make smoke        # replay the bundled protocol scenes, no database
make run          # in-memory mode (REST disabled)

make db-up        # postgres:16 → 127.0.0.1:55432
TRACCAR_DSN='postgres://user:pass@127.0.0.1:55432/traccar?sslmode=disable' make run-db
make rest-e2e     # replay + verify REST positions / alerts / commands

TRACCAR_DSN='mysql://user:pass@127.0.0.1:3306/traccar' make run-db       # MySQL 8.0+
TRACCAR_DSN='sqlite:///var/lib/traccar/traccar.db' make run-db           # embedded SQLite, file created on demand

docker compose up --build -d
curl -H 'X-Api-Key: dev-key' http://127.0.0.1:8082/api/health
open http://127.0.0.1:8082/          # UI; ./web holds the in-repo traccar-web build
docker compose down -v

./bin/traccar-gateway -dsn "$TRACCAR_DSN" -api-key dev-key -web-path ./web   # serve the UI from the same process
sudo ./deploy/ubuntu/install.sh                                             # VPS: systemd + SQLite, --domain adds nginx
```

## Configuration

Three layers, low → high: built-in defaults → `traccar.xml` → environment variables; flags win over
all. Key names follow the reference `config/Keys.java`; Go-only keys are marked `Go only`.

```xml
<properties>
    <entry key='web.address'>0.0.0.0:8082</entry>
    <entry key='database.url'>postgres://127.0.0.1:5432/traccar?sslmode=disable</entry>
    <!-- or mysql://user:pass@127.0.0.1:3306/traccar -->
    <!-- or sqlite:///var/lib/traccar/traccar.db -->
    <entry key='protocol.gt06.address'>0.0.0.0:5023</entry>
    <entry key='protocol.gt06.timeout'>30s</entry>
</properties>
```

- Environment: `database.url` → `TRACCAR_DATABASE_URL` (upper case, `.`→`_`, camelCase split).
- Per protocol: `protocol.<name>.<key>` → `<name>.<key>` → `protocol.<key>` → `<key>`.
- Durations take `30s` or a bare number (seconds); `server.timeout` (120s) decides offline.
- Legacy variables: `TRACCAR_DSN`, `TRACCAR_API_KEY`, `TRACCAR_SMTP_PASSWORD`.

Default listeners (same-named flags override; empty string disables):

| Protocol | Flag | Address | Protocol | Flag | Address |
| --- | --- | --- | --- | --- | --- |
| H02 | `-h02` | `:5013` | GPS103 | `-gps103` | `:5002` |
| GT06 | `-gt06` | `:5023` | TK103 | `-tk103` | `:5001` |
| JT808 | `-jt808` | `:8610` | Meitrack | `-meitrack` | `:5020` |
| Teltonika | `-teltonika` | `:5027` | Wondex | `-wondex` | `:5006` |
| OsmAnd | `-osmand` | `:5055` (http) | Suntech | `-suntech` | `:5011` |
| MQTT | `-mqtt` | `:1883` | Ruptela | `-ruptela` | `:5046` |
| REST | `-http` | `:8082` (needs `-dsn`) | | | |

Frequent flags: `-dsn`, `-api-key`, `-web-path`, `-allow-anonymous`, `-auto-register` (true),
`-batch-size` (500) / `-batch-interval` (1s), `-overspeed-knots` / `-power-cut-volts` (0 = off),
`-alert-repeat` (0), `-geofence-refresh` / `-device-refresh` (5m), `-login-max-failures` (5) /
`-login-lock-duration` (15m), `-issue-token` / `-token-scopes` / `-token-ttl`, `-command-retry` (30s) /
`-command-attempts` (3) / `-command-queue` (32) / `-command-queue-ttl` (5m) / `-command-retention`
(168h), `-disable-share`, `-notify-*`.

Config / environment only:

| Feature | Keys |
| --- | --- |
| Frontend hosting | `web.path` (traccar-web build dir; also the write root of `POST /api/server/file/{path}`; empty = no hosting, no uploads) |
| Position attribute copy | `position.copyAttributes` (comma separated; empty = off) |
| Device images | `deviceImage.path` (default `<media.path>/devices`) |
| HLS streaming | `stream.path` (an external generator writes `<path>/<deviceId>/index.m3u8`) |
| JT808 command layouts | `protocol.jt808.alternative` = true sends engine control as the oil control message some terminals need; `web.url` and `jt1078.port` fill the videoStart command (the gateway runs no JT1078 listener) |
| GT06 command layouts | `protocol.gt06.alternative` = true sends the engine commands as `DYD,<password>` / `HFYD,<password>`; `protocol.gt06.language` adds the optional english field to every command frame; `protocol.gt06.devicePassword` is the password the alternative layout carries (default `123456`) |
| TK103 command layouts | `protocol.tk103.alternative` = true sends the SMS2 layout (`[begin]sms2,...[end]`) instead of the legacy parenthesised sentences; `protocol.tk103.devicePassword` fills the `*master*` SOS command (default `123456`) |
| Meitrack command layouts | `protocol.meitrack.alternative` = true sends arm/disarm as `B21,1` / `B21,0`; the default is the `C01` form |
| Wondex command password | `protocol.wondex.devicePassword` fills the `$WP+...` sentence (default `0000`) |
| Redis push | `redis.url`, `redis.channel` (default `traccar`) |
| OpenID Connect | `openid.{enable,issuer,clientId,clientSecret,redirectUrl,scopes,createMissing,successUrl}` |
| LDAP | `ldap.{url,user,password,base,idAttribute,searchFilter,nameAttribute,mailAttribute,timeout}` (`ldap.userFilter` kept as alias) |
| Network forward | `forward.network.address`, `forward.network.protocol` (`tcp` / `udp`) |
| AMQP / Kafka forward | `forward.type` (`json` / `url` / `amqp` / `kafka`), `forward.url`, `forward.exchange` (default `traccar`), `forward.topic` (default `positions`); events use `event.forward.*` (topic default `events`) |
| Geo services | `geocoder.type` (26 providers, e.g. nominatim, google, opencage, here, mapbox, photon, ban, pluscodes), `geolocation.type` (google / mozilla / opencellid / unwired / here / universal), extra options via `*.url`; nominatim, photon, ban, geocodejson and pluscodes need no `geocoder.key` (pluscodes is computed locally) |
| Scheduling | `schedule.{expiration,temporary,inactive,deviceExpiration,events,positions,commands}.interval`, `schedule.{events,positions,commands}.retention` (0 = never delete) |
| Query cap | `report.maxPositions` (positions per `/api/positions` call and export; `0` = unlimited, default `50000`, same meaning as the reference) |

## Protocols

| Protocol | Covered | Missing |
| --- | --- | --- |
| H02 | text `V1/V3/V4/V5/V6/V8/V19`, `NBR`, `LINK`, `VP1`, `HTBT`, `SMS`, heartbeat; binary 32/45-byte frames, `X` frames, `R12` replies; encoder covers the H02 downlink set (`SCF` arm/disarm, `S20` engine control, `S71` reporting interval, the `D1` interval when `protocol.h02.alternative` is set, raw custom) | — |
| GT06 | login / heartbeat / GPS-LBS, `0x2A` address, `0x8A` time sync, `0x94` voltage, CRC16/X25 ACK, `NT_MODELS` layouts; encoder covers the Concox downlink set (`WHERE`, `TIMER`, `Relay`/`DYD`/`HFYD`, `RESET`, `FACTORY`, `POWEROFF`, `STATUS`, `VERSION`, `PARAM`, `SOS`/`CENTER`/`LISTEN`, `SERVER`/`DOMAIN`, raw custom), with the G109 `DYD`/`HFYD` engine layout | WiFi scan, ADAS/DMS |
| JT808 | 2013/2019 headers, `0x7E` escaping, XOR, register / position / batch position (`0x0704`, `0x0210`), heartbeat / ICCID / time sync; all 21 downlink commands from the reference encoder, including the model specific custom and engine layouts | driver card, parameters, pass-through, ADAS/DMS/TPMS/OBD |
| Teltonika | IMEI handshake, Codec 8 / 8E / 12 / 13, variable-length elements, LAC/CID; encoder covers the reference downlink set (`setdigout` engine stop/resume, raw custom as ASCII or hex) | Codec 7/16, BLE |
| OsmAnd, MQTT | OsmAnd query string; MQTT 3.1.1 reuses the same decoder | — |
| GPS103 | `imei:` text sentence, decimal degrees + hemisphere; encoder covers the reference downlink set (`B` single, `C` periodic, `D` stop, `J`/`K` engine, `L`/`M` arm/disarm, `160` photo, raw custom) | — |
| TK103 | `imei:` text sentence, decimal degrees + hemisphere; encoder covers the reference legacy and SMS2 downlink sets (`AP`, `AR`, `AT`, `AV`, `AX` legacy; `*getposl*`, `*routetrack*`, `*sleep*`, `*sos*`, `*setip*`, `*master*` alternative) | — |
| Meitrack | `$$` + length + fields + XOR; encoder covers the reference downlink set (`A10` single, `C01` engine/arm, `B21` arm alternative, `D03` photo, `C02` SMS, raw custom) | — |
| Wondex | `#DD#` + `;`-separated fields; encoder covers the reference downlink set (`REBOOT`, `TEST`, `GSMINFO`, `IMEI`, `GETLOCATION`, `VER`) | — |
| Suntech | `STT/ALT/EMG/EVT/UEX/BLE`, universal, zip / binary; encoder covers the legacy `SA200CMD;...` downlink set (`Reboot`, `StatusReq`, `Enable`/`Disable` outputs, engine and arm/disarm) | universal command layout (needs the live decoder state) |
| Ruptela | IMEI handshake, Records / Extended, DTC, IO configuration; encoders complete | — |
| GT02 | `0x68 0x68` length frame, position, heartbeat ACK and command response; decoder only, the reference ships no encoder | — |
| GPS056 | `$$` length frame, `LOGN` / `GPSL` / `SYNC`, status fields and ACK; decoder only, the reference ships no encoder | — |

## Database

The scheme of `database.url` picks the dialect; `Store.Migrate` applies the matching migrations:

| Dialect | DSN example | Migrations |
| --- | --- | --- |
| PostgreSQL | `postgres://user:pass@host:5432/traccar?sslmode=disable` | `internal/storage/migrations/postgres/` (20) |
| MySQL 8.0+ | `mysql://user:pass@host:3306/traccar` (`?socket=/tmp/mysql.sock` also works) | `internal/storage/migrations/mysql/` |
| SQLite | `sqlite:///var/lib/traccar/traccar.db` (`sqlite::memory:` for tests) | `internal/storage/migrations/sqlite/` |

This is the Go project's own schema, not the reference `tc_*` schema: attaching an existing Java
database still needs a mapping layer (see "Differences from the reference"). SQLite is pure Go
(`modernc.org/sqlite`, no cgo) and replaces Java's H2, which is Java-only and unsupported.

Main tables: `devices`, `positions`, `events`, `geofences`, `device_groups`, `calendars`, `drivers`,
`maintenances`, `orders`, `reports`, `users`, `api_tokens`, `commands`, `password_resets`,
`notifications`, `user_notifications`, `permissions`, `media`, `protocol_settings`, `server_settings`,
`actions`, `statistics`, `attributes`.

Integration tests read `TRACCAR_TEST_DSN` (legacy `TRACCAR_POSTGRES_DSN` still works):

```bash
TRACCAR_TEST_DSN='sqlite:///tmp/traccar-test.db' go test ./internal/storage
make db-up-mysql
DSN='mysql://root@127.0.0.1:53306/traccar' ./scripts/rest-e2e.sh
make db-down-mysql
```

## Resource requirements

| Scale | CPU | RAM | Disk |
| --- | --- | --- | --- |
| Dev / 1–20 devices | 1 vCPU | 512MB–1GB | 10GB |
| 20–500 devices (10–30s interval) | 1–2 vCPU | 1GB | 25GB with retention |
| 500+ devices | 2 vCPU+ | 2GB+ | depends on retention and report rate |

- Idle memory is tens of MB; each connection costs one goroutine (~8–16KB plus read buffer), so a few
  hundred connections stay below 100MB. Decoding is pure and handles thousands of rows per second per
  core.
- **On a 1GB host use the release zip instead of compiling** (the Go compiler peaks at 0.5–1GB). If you
  must compile, add 2GB swap first. Prefer SQLite there: MySQL 8 idles at 300–500MB, PostgreSQL at
  100–200MB.
- Disk: positions with indexes ≈ 300B/row and nothing is deleted by default.

  | Devices × interval | Per day | Per year |
  | --- | --- | --- |
  | 1 × 10s | 2.6 MB | ~1 GB |
  | 10 × 10s | 26 MB | ~9.5 GB |
  | 10 × 30s | 8.6 MB | ~3 GB |
  | 100 × 30s | 86 MB | ~31 GB |

  On a 25GB disk set retention (`0` = never delete, Go duration units only, no `d`, so 90 days = 2160h):

  ```xml
  <entry key='schedule.positions.retention'>2160h</entry>
  <entry key='schedule.events.retention'>4320h</entry>
  ```

  `schedule.positions.interval` / `schedule.events.interval` already default to `24h`; an invalid value
  only logs `schedule: ...` and keeps rows forever.

## VPS deployment (Ubuntu)

`deploy/ubuntu/install.sh` installs Go if needed, creates the `traccar` system user, builds and installs
the binary, copies the traccar-web build, writes `/opt/traccar/conf/traccar.xml` (same location as the
Java release, with a random `api.key`), installs and starts the systemd unit, and only exits once
`/api/health` returns 200.

```bash
git clone https://github.com/onethings/go-gps-gateway.git && cd go-gps-gateway
sudo ./go_server/deploy/ubuntu/install.sh        # SQLite + systemd, Web/API on 0.0.0.0:8082
# then open http://<VPS-IP>:8082/ and register the first account (becomes administrator)
```

| Need | Command |
| --- | --- |
| Domain + nginx (gateway on `127.0.0.1:8082`, nginx terminates TLS) | `sudo ./go_server/deploy/ubuntu/install.sh --domain tracker.example.com`, then `sudo apt-get install -y certbot python3-certbot-nginx && sudo certbot --nginx -d tracker.example.com` |
| MySQL / PostgreSQL (create DB and user first) | `--dsn 'mysql://traccar:secret@127.0.0.1:3306/traccar'`, `--dsn 'postgres://traccar:secret@127.0.0.1:5432/traccar?sslmode=disable'` |
| Build locally (macOS), upload only the binary | `GOOS=linux GOARCH=amd64 go build -o tg ./go_server/cmd/traccar-gateway`, `scp tg vps:`, `sudo ./go_server/deploy/ubuntu/install.sh --binary ./tg` |
| Upgrade | `git pull && sudo ./go_server/deploy/ubuntu/install.sh` (config kept; `--force` rewrites it and rotates `api.key`) |
| Config in `/etc/traccar` (legacy) | `sudo ./go_server/deploy/ubuntu/install.sh --conf-dir /etc/traccar` |

| Installed path | Contents |
| --- | --- |
| `/opt/traccar/bin/traccar-gateway` | binary, overwritten on upgrade |
| `/opt/traccar/web` | traccar-web build; also the write root of `POST /api/server/file/{path}` |
| `/opt/traccar/data/traccar.db` | default SQLite database (`--dsn` switches to PostgreSQL / MySQL) |
| `/opt/traccar/conf/traccar.xml`, `conf/api-key` | config (same location as the Java release, `--conf-dir` overrides) and the generated admin key (`0640 root:traccar`) |
| `/etc/systemd/system/traccar-gateway.service` | unit with `ProtectSystem=strict`, only `data` / `web` writable |

```bash
sudo systemctl status traccar-gateway
sudo journalctl -u traccar-gateway -f
curl -H "X-Api-Key: $(sudo cat /opt/traccar/conf/api-key)" http://127.0.0.1:8082/api/health

# open the protocol ports in ufw and in the cloud security group
sudo ufw allow 22/tcp; sudo ufw allow 80,443/tcp
sudo ufw allow 5013,5023,5027,8610/tcp        # H02 / GT06 / Teltonika / JT808
sudo ufw allow 5001,5002,5006,5011,5020,5046/tcp; sudo ufw allow 5055,1883/tcp
sudo timedatectl set-timezone Asia/Taipei     # H02 / GT06 time sync and encoding use local time

# uninstall
sudo systemctl disable --now traccar-gateway && sudo rm /etc/systemd/system/traccar-gateway.service
sudo rm -rf /opt/traccar /etc/traccar         # /etc/traccar only when upgraded from the legacy layout
sudo rm -f /etc/nginx/sites-{enabled,available}/traccar-gateway; sudo userdel traccar
```

### Manual deployment (MySQL + traccar-web)

The Go process can host the frontend itself: point `-web-path` at a directory holding the traccar-web
build (`index.html` + assets). `/api/*` is served by the same process, so cookies and WebSocket stay
same-origin.

1. Create the database (MySQL 8.0+):

   ```sql
   CREATE DATABASE traccar CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
   CREATE USER 'traccar'@'127.0.0.1' IDENTIFIED BY 'change-me';
   GRANT ALL PRIVILEGES ON traccar.* TO 'traccar'@'127.0.0.1';
   ```

2. Put the traccar-web build (`web/` from the official release zip, or `npm ci && npm run build` output)
   in `/opt/traccar/web`, then start:

   ```bash
   ./bin/traccar-gateway -dsn 'mysql://traccar:change-me@127.0.0.1:3306/traccar' \
     -api-key "$(openssl rand -hex 24)" -web-path /opt/traccar/web -http 127.0.0.1:8082
   ```

3. With no accounts yet, `POST /api/users` allows creating the **first** account anonymously and makes
   it an administrator; later anonymous sign-ups need `server.registration=true`.

4. To keep the frontend in nginx instead, proxy `/api/` (with `Upgrade` / `Connection` headers for the
   WebSocket) to `127.0.0.1:8082` and serve the build with `try_files $uri /index.html`. The whole stack
   also runs in containers: `TRACCAR_API_KEY=... MYSQL_ROOT_PASSWORD=... make compose-up-mysql`.

5. Verify: `curl -H 'X-Api-Key: ...' http://127.0.0.1:8082/api/health` and
   `DSN='mysql://traccar:change-me@127.0.0.1:3306/traccar' ./scripts/rest-e2e.sh`.

## Packaging and release (zip)

`make dist` bundles linux/amd64 and linux/arm64 binaries, the traccar-web build and the Ubuntu installer
into one zip; the target host needs neither Go nor the sources.

```bash
make dist                    # → dist/traccar-server-go_<git describe>.zip (+ .sha256)
make dist VERSION=v0.1.0     # explicit version
unzip traccar-server-go_0.1.0.zip && cd traccar-server-go_0.1.0 && sudo ./install.sh
```

The zip holds `install.sh`, `LICENSE`, `bin/linux-{amd64,arm64}/traccar-gateway`, `web/`,
`traccar-gateway.service.in`, `nginx.conf.in` and `INSTALL.txt`; the architecture is picked from
`uname -m`.

To publish, either upload the assets to another public repo (recommended, keeps sources out of it):

```bash
gh auth login          # once, outside the sandbox
./scripts/publish-release.sh <account>/<public-repo> v0.1.0
```

which creates the repo with `gh repo create --public --add-readme` when missing and uploads
`dist/*.zip` + `.sha256` as release assets (`gh release upload --clobber` on an existing tag); or commit
the zip and its `.sha256` into the repo and download it from `raw.githubusercontent.com` (raw paths are
fine up to 100MB). Verify with `sha256sum -c` (`shasum -a 256 -c` on macOS) before unzipping.

Current public download: `onethings/tra-server-go-releases-fortesting`.

## REST API

Auth via `-api-key` (admin), `api_tokens` (scopes + permissions) or a `/api/login` token. `/api/health`,
`/api/login`, password self-service, OpenID and `/api/socket` are public; non-admins only see authorized
devices; `-allow-anonymous` disables auth for local use.

List endpoints (devices / positions / users / commands / notifications / resources) return **bare JSON
arrays** like the reference `openapi.yaml`, while events and alerts keep `{events|alerts, count}`. Rows
from `GET /api/positions` carry both `deviceTime` and `fixTime` (Go keeps one timestamp, so they match).

`POST /api/users` allows anonymous creation of the first account (becomes administrator) while the user
table is empty; afterwards only with `server.registration=true`. Users may `PUT /api/users/{id}` for
their own name / attributes / password but cannot grant themselves administrator or disabled;
`GET /api/users/{id}` allows admins and the owner.

| Area | Routes |
| --- | --- |
| Events / positions | `GET /api/events[/{id}]`, `/api/alerts`, `GET /api/positions[/{id}]`, `/api/positions/{kml,kmz,csv,gpx}` |
| Devices | `GET/POST /api/devices`, `GET/PUT/DELETE /api/devices/{id}`, `GET/PUT .../accumulators`, `GET/POST/DELETE .../image` |
| Commands | `GET/POST /api/commands`, `GET /api/commands/{id}`, `/api/commands/{send,bulk,types}` |
| Accounts | `/api/users[/{id}]`, `/{id}/{devices,tokens,totp}`, `POST /api/users/totp` |
| Auth | `POST /api/login` (+ optional `code`), `/api/password/{forgot,reset,update}`, `PUT /api/users/me/password`, `/api/session[...]`, `/api/session/openid/{auth,callback}` |
| Permissions / sharing | `/api/permissions`, `POST/DELETE /api/permissions/bulk`, `/api/share/{device,group}` |
| Resource CRUD | `/api/{groups,geofences,calendars,drivers,maintenance(s),orders,reports}`, `/api/attributes/computed[/test]` |
| Admin | `/api/protocols`, `/api/server[...]`, `/api/server/cache`, `/api/statistics[/devices]`, `/api/audit`, `/api/notificators` |
| Media / streaming | `/api/media[/{id}]`, `GET /api/stream/{deviceId}[/{file...}]` |
| Other | `GET /metrics`, `GET /api/socket`, `GET /api/health` |

## Development

- `make test` / `make race`: unit tests and the race detector.
- `make smoke`: replay without a database; `make rest-e2e`: full chain against `DSN` (PostgreSQL or MySQL).
- `cmd/device-sim`: device simulator.

## Known limitations

- With `-dsn` REST requires credentials; `-api-key` or the first registered account grants admin.
- Accounts: pagination only, email immutable after creation, failed logins locked out (bcrypt cost 12).
- `users` has no `device_limit` / `user_limit`, so the API always returns 0 (until the `tc_*` work).
- `PUT /api/devices/{id}` replaces all fields; `protocol` / `uniqueId` are immutable; `disabled` does not
  block ingestion yet.
- Group permissions only affect device visibility; notifications still resolve `user → device`.
- Share accounts are deleted by an admin or when `schedule.temporary.interval` expires; `/api/socket`
  accepts a query token or a session cookie.
- `suntech` receives only (no commands); in-memory mode is development only.
- MySQL needs 8.0+ (`ROW_NUMBER`); SQLite is single-node and must not be shared by several processes.
- TOTP / OpenID / LDAP / HLS / device images / Redis push are opt-in and answer 501 when unconfigured.
- Event `position_id` is usually NULL (position batches do not return ids), so events and positions are
  not linked in reports.

## Differences from the reference

| Area | Go version | Gap |
| --- | --- | --- |
| Protocol decoders | 14 | 254 |
| Command encoders | 10 | 49 |
| Pipeline / events | 15 stages / 12 rules | 0 |
| OpenAPI paths | 76 | 0 |
| Geo providers | geocoder 26, geolocation 6 | — |
| Output | JSON, URL, MQTT, Redis, Wialon, Network, AMQP, Kafka | — |
| Database | PostgreSQL, MySQL 8.0+, SQLite (replaces H2) | H2 (Java only); `tc_*` schema not yet compatible |
| Auth | password, token, TOTP, OpenID, LDAP | 0 |
| Frontend | hosts a traccar-web build directly (same-origin) | no bundled build; bring your own `web/` |
| Horizontal scale | Redis push | multicast; cross-node device sessions |
| Video | upload / download, HLS endpoint | JT1078 decoding and segmentation |
| Scheduling | 10 | — |

Next: add protocols and encoders for customer devices → map the reference `tc_*` schema so an existing
Java database can be attached → add JT1078 for live video.

## License

Apache License, Version 2.0 — see [LICENSE](../LICENSE).
