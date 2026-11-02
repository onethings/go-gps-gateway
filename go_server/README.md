# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的設備協議與伺服器：設備幀 → 標準 `Position` → PostgreSQL，
並提供對齊母本的 REST API、告警 / 地理圍欄、通知、命令下發與處理鏈。

- 母本：`../traccar-master`（Java）；每個 Go 檔案都標明對應的 Java 類別。
- 協議層是純函式（`[]byte` → `*model.Position`），I/O 集中在 `internal/gateway`。
- 需求：Go 1.22+；PostgreSQL 只在 REST / 告警 / 命令歷史時需要。

## 現況

- **協議**：12 支（H02、GT06、JT808、Teltonika、OsmAnd、MQTT、GPS103、TK103、Meitrack、
  Wondex、Suntech、Ruptela），TCP / UDP / TLS / HTTP / poll 五種傳輸。
- **處理鏈**：15 個 stage 全部有實作，含 Time / Hemisphere、Geolocation / Geocoder /
  CopyAttributes / EngineHours、ComputedAttributes（`internal/expr` 求值）、ProximityRule。
- **REST**：裝置 / 位置 / 事件、帳號與 token、TOTP、權限與分享、資源 CRUD、命令、
  WebSocket、統計、媒體、HLS、OpenID、LDAP、密碼自助。
- **外送**：JSON、URL、MQTT、Redis、Wialon、Network（TCP/UDP）。
- **排程**：expiration、cleanup、health、reports、deviceInactive、deviceExpiration，
  以及 events / positions / commands retention。

> `go build ./...`、`go vet ./...`、`go test ./...` 全綠；gateway / alert / api / notify 另跑 `-race`。

## 架構

```
device ──TCP/UDP/TLS/HTTP/poll──▶ gateway.Server（每連線一個 goroutine）
  FrameDecoder → Decoder → DeviceDirectory → session.Manager → Sink = handler.Chain
    └ business → alert.Pipeline → notify.Dispatcher
    └ terminal → postgres.BatchSink（positions + events）

client ──HTTP/WebSocket──▶ api.Server（RequireAuth：api-key / api_tokens / login token）
```

## 快速開始

```bash
cd go_server
make build        # bin/traccar-gateway、bin/device-sim
make test         # go test ./...
make smoke        # 無資料庫，重播四協議場景
make run          # 記憶體模式（REST 關閉）

make db-up                                      # postgres:16 → 127.0.0.1:55432
TRACCAR_DSN='postgres://.../traccar?sslmode=disable' make run-db
make rest-e2e                                   # 重播 + 驗證 REST 位置 / 告警 / 命令

docker compose up --build -d
curl -H 'X-Api-Key: dev-key' http://127.0.0.1:8082/api/health
docker compose down -v
```

## 設定

三層（低 → 高）：內建預設 → `traccar.xml` → 環境變數；flag 在最上層。key 名照母本
`config/Keys.java`，Go 專屬 key 標了 `Go only`。

```xml
<properties>
    <entry key='web.address'>0.0.0.0:8082</entry>
    <entry key='database.url'>postgres://127.0.0.1:5432/traccar?sslmode=disable</entry>
    <entry key='protocol.gt06.address'>0.0.0.0:5023</entry>
    <entry key='protocol.gt06.timeout'>30s</entry>
</properties>
```

- 環境變數：`database.url` → `TRACCAR_DATABASE_URL`（大寫、`.`→`_`、駝峰拆字）。
- per-protocol 解析：`protocol.<名稱>.<key>` → `<名稱>.<key>` → `protocol.<key>` → `<key>`。
- 時間可寫 `30s` 或純數字（秒）；`server.timeout`（120s）判離線。
- 舊變數：`TRACCAR_DSN`、`TRACCAR_API_KEY`、`TRACCAR_SMTP_PASSWORD`。

預設監聽（flag 同名可覆寫，空字串關閉）：

| 協議 | Flag | 位址 | 協議 | Flag | 位址 |
| --- | --- | --- | --- | --- | --- |
| H02 | `-h02` | `:5013` | GPS103 | `-gps103` | `:5002` |
| GT06 | `-gt06` | `:5023` | TK103 | `-tk103` | `:5001` |
| JT808 | `-jt808` | `:8610` | Meitrack | `-meitrack` | `:5020` |
| Teltonika | `-teltonika` | `:5027` | Wondex | `-wondex` | `:5006` |
| OsmAnd | `-osmand` | `:5055`（http） | Suntech | `-suntech` | `:5011` |
| MQTT | `-mqtt` | `:1883` | Ruptela | `-ruptela` | `:5046` |
| REST | `-http` | `:8082`（需 `-dsn`） | | | |

常用 flag：`-dsn`、`-api-key`、`-allow-anonymous`、`-auto-register`（true）、`-batch-size`(500) /
`-batch-interval`(1s)、`-overspeed-knots` / `-power-cut-volts`（0=關）、`-alert-repeat`(0)、
`-geofence-refresh`(5m) / `-device-refresh`(5m)、`-login-max-failures`(5) / `-login-lock-duration`(15m)、
`-issue-token` / `-token-scopes` / `-token-ttl`、`-command-retry`(30s) / `-command-attempts`(3) /
`-command-queue`(32) / `-command-queue-ttl`(5m) / `-command-retention`(168h)、`-disable-share`、
`-notify-*`。

只走設定檔 / 環境變數的服務：

| 功能 | Key |
| --- | --- |
| 位置屬性複製 | `position.copyAttributes`（逗號分隔；空=關） |
| 帳號影像 | `deviceImage.path`（預設 `<media.path>/devices`） |
| HLS 串流 | `stream.path`（外部產生器寫 `<path>/<deviceId>/index.m3u8`） |
| Redis 推播 | `redis.url`、`redis.channel`（預設 `traccar`） |
| OpenID Connect | `openid.enable`、`openid.issuer`、`openid.clientId`、`openid.clientSecret`、`openid.redirectUrl`、`openid.scopes`、`openid.createMissing`、`openid.successUrl` |
| LDAP | `ldap.url`、`ldap.user`、`ldap.password`、`ldap.base`、`ldap.userFilter`、`ldap.nameAttribute`、`ldap.mailAttribute`、`ldap.timeout` |
| 網路外送 | `forward.network.address`、`forward.network.protocol`（`tcp` / `udp`） |
| 地理服務 | `geocoder.type`（nominatim / google / opencage / here / locationiq / mapbox / tomtom / photon / pelias）、`geolocation.type`（google / mozilla / opencellid / unwired / here）；其餘用 `*.url` |
| 排程 | `schedule.{inactive,deviceExpiration,events,positions,commands}.interval`、`schedule.{events,positions,commands}.retention`（0=不刪） |

## 協議

- **H02**：文字 `V1/V3/V4/V5/V6/V8/V19`、`NBR`、`LINK`、`VP1`、`HTBT`、`SMS`、心跳；
  二進位 32/45 byte 幀、`X` 幀、`R12` 應答。
- **GT06**：登入 / 心跳 / GPS-LBS 主線、`0x2A` 位址、`0x8A` 對時、`0x94` 電壓，CRC16/X25 ACK；
  `NT_MODELS` 自訂版面。待補：WiFi 掃描、ADAS/DMS。
- **JT808**：2013/2019 標頭、`0x7E` 轉義、XOR、註冊 / 位置 / 心跳 / ICCID / 對時。待補：批次、
  駕駛員卡、參數、透傳、ADAS/DMS/胎壓/OBD。
- **Teltonika**：IMEI 握手、Codec 8 / 8E / 12 / 13、可變長度元素、LAC/CID。待補：Codec 7/16、BLE。
- **OsmAnd / MQTT**：OsmAnd 查詢字串；MQTT 3.1.1 重用同一 decoder。
- **GPS103 / TK103**：`imei:` 文字句、十進位度 + 半球。
- **Meitrack**：`$$` + 長度 + 欄位 + XOR。
- **Wondex**：`#DD#` + `;` 分隔。
- **Suntech**：`STT/ALT/EMG/EVT/UEX/BLE`、universal、zip / 二進位。待補：Encoder。
- **Ruptela**：IMEI 握手、Records / Extended、DTC、IO 設定；Encoder 齊全。

## 資料庫

`internal/storage/postgres/migrations/*.sql`（18 支）由 `Store.Migrate` 套用；主要表：
`devices`、`positions`、`events`、`geofences`、`groups`、`calendars`、`drivers`、
`maintenances`、`orders`、`reports`、`users`、`user_devices`、`api_tokens`、`commands`、
`password_resets`、`notifications`、`notification_rules`、`permissions`、`media`、`protocols`。

## REST API

`-api-key`（admin）、`api_tokens`（scope + permissions）或 `/api/login` token；`/api/health`、
`/api/login`、密碼自助、OpenID 與 `/api/socket` 例外。非 admin 只能存取授權裝置；
本機可用 `-allow-anonymous` 關閉認證。

| 區塊 | 路由 |
| --- | --- |
| 事件 / 位置 | `GET /api/events[/{id}]`、`/api/alerts`、`GET /api/positions[/{id}]`、`/api/positions/{kml,kmz,csv,gpx}` |
| 裝置 | `GET/POST /api/devices`、`GET/PUT/DELETE /api/devices/{id}`、`GET/PUT .../accumulators`、`GET/POST/DELETE .../image` |
| 命令 | `GET/POST /api/commands`、`GET /api/commands/{id}`、`/api/commands/send`、`/api/commands/bulk`、`/api/commands/types` |
| 帳號 | `/api/users[/{id}]`、`/{id}/devices`、`/{id}/tokens`、`/{id}/totp`、`POST /api/users/totp` |
| 認證 | `POST /api/login`（可帶 `code`）、`/api/password/{forgot,reset,update}`、`PUT /api/users/me/password`、`/api/session[...]`、`/api/session/openid/{auth,callback}` |
| 權限 / 分享 | `/api/permissions`、`POST/DELETE /api/permissions/bulk`、`/api/share/{device,group}` |
| 資源 CRUD | `/api/{groups,geofences,calendars,drivers,maintenance(s),orders,reports}`、`/api/attributes/computed[/test]` |
| 管理 | `/api/protocols`、`/api/server[...]`、`/api/server/cache`、`/api/statistics[/devices]`、`/api/audit`、`/api/notificators` |
| 媒體 / 串流 | `/api/media[/{id}]`、`GET /api/stream/{deviceId}[/{file...}]` |
| 其他 | `GET /metrics`、`GET /api/socket`、`GET /api/health` |

## 開發

- `make test` / `make race`：單元測試與 race detector。
- `make smoke`：無資料庫重播；`make rest-e2e`：PostgreSQL 全鏈驗證。
- `cmd/device-sim`：設備模擬器。

## 已知限制

- 有 `-dsn` 時 REST 預設要求憑證；先帶 `-api-key` 建立管理員，或 `-issue-token`。
- 帳號 API 只有分頁；email 建立後不可改；登入失敗鎖定（bcrypt cost 12）。
- `PUT /api/devices/{id}` 是整欄取代；`protocol` / `uniqueId` 不可改；`disabled` 尚未擋 ingest。
- 群組授權只影響可見裝置；通知收件者仍只認 `user → device`。
- 分享帳號只能由 admin 刪除；`/api/socket` token 在 query string。
- `suntech` 只能收不能下命令；記憶體模式僅適合開發。
- TOTP / OpenID / LDAP / HLS / 裝置影像 / Redis 推播都要設定後才啟用，未設定回 501。

## 與母本差距

| 面向 | Go 版 | 缺口 |
| --- | --- | --- |
| 協議解碼器 | 12 | 256 |
| 命令 Encoder | 5 | 60 |
| 處理鏈 / 事件 | 15 stage / 12 規則 | 0 |
| OpenAPI 路徑 | 76 | 0 |
| 地理 provider | geocoder 9、geolocation 5 | geocoder 15 |
| 外送 | JSON、URL、MQTT、Redis、Wialon、Network | AMQP、Kafka |
| 資料庫 | PostgreSQL | MySQL / H2；schema 與母本不相容 |
| 認證 | 密碼、token、TOTP、OpenID、LDAP | 0 |
| 水平擴展 | Redis 推播 | multicast；跨節點 device session |
| 影像 | 上傳 / 下載、HLS 端點 | JT1078 解碼與切片 |
| 排程 | 9 | temporary 帳號到期刪除 |

後續順序：依客戶設備補協議與 Encoder → 需要時補 AMQP/Kafka → 要接母本資料庫再處理
storage / schema → 要即時影像再做 JT1078。
