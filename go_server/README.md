# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的設備協議與伺服器：解碼設備幀 → 標準 `Position` → 落地（PostgreSQL），
並提供對齊母本的 REST API、告警 / 地理圍欄、通知與命令下發。

- 母本：`../traccar-master`（Java）。每個 Go 檔案的註解都標明對應的 Java 類別。
- 設計原則：協議層是**純函式**（`[]byte` → `*model.Position`），所有 I/O 集中在 `internal/gateway`。
- 需求：Go 1.22+。PostgreSQL 只在要跑 REST / 告警 / 命令歷史時才需要。

## 現況

- 12 個協議：H02、GT06、JT808、Teltonika、OsmAnd、MQTT、GPS103、TK103、Meitrack、Wondex、
  Suntech、Ruptela（後兩者 5011 / 5046 預設啟動）。純函式 codec + TCP / UDP / TLS / HTTP / poll 五種傳輸基底。
- 處理鏈：`ProcessingHandler` 宣告 15 個階段，15 個都已有實作：
  `distance`（OutdatedHandler + DistanceHandler）、`filter`（FilterHandler）、
  `defaultData`（TimeHandler + HemisphereHandler）、`position`（GeolocationHandler + GeocoderHandler +
  EngineHoursHandler）、`motion`、`mapMatcher`、`geofence`、`computedAttributes`（`internal/expr`
  的運算式求值）、`speedLimit`、`alertEvent`（含 ProximityRule）、`business`、`fuel`、`driver`、
  `maintenance`、`media`。`CopyAttributesHandler` 也已實作，由需要複製欄位的協議掛進 `position`。
  順序上 `distance` 在 `filter` 之前，與母本一致（Filter 的 distance / maxSpeed 規則要讀剛算好的里程）。
  `business` = `alert.Pipeline`（規則評估 → events 與 position 同交易落地）→
  `notify.Dispatcher`（webhook / SMTP，背景傳送）；`terminal` = `postgres.BatchSink` 批次落地。
- REST API：裝置 / 位置 / 事件（含 `GET /api/positions/{id}`、`GET /api/commands/{id}`）、
  帳號與 token、權限與分享、資源 CRUD、命令、WebSocket 推播、統計、媒體、認證與密碼自助。
- 連線狀態：`devices.status`（online / offline / unknown）、心跳逾時、`events` 表。

> **測試**：`go build ./...`、`go vet ./...`、`go test ./...` 全部通過（gateway / alert / api /
> notify 另以 `-race` 跑過）。

## 架構

```
device ──TCP/UDP/TLS/HTTP/poll──▶ gateway.Server（每連線一個 goroutine）
                  ├─ FrameDecoder   切幀（半包 / 粘包 / 轉義 / 校驗）
                  ├─ Decoder        frame → []*model.Position + 回包
                  ├─ SessionDecoder 需綁連線身分的協議（GT06 / Teltonika）
                  ├─ DeviceDirectory   uniqueId ⇄ deviceId（-auto-register）
                  ├─ LocationCache    每裝置最後一筆定位
                  ├─ session.Manager  連線登錄 → devices.status + online/offline
                  └─ Sink = handler.Chain（ProcessingHandler 處理鏈）
                       ├─ business → alert.Pipeline → notify.Dispatcher（webhook/SMTP）
                       └─ terminal → postgres.BatchSink（positions 批次落地）

client ──HTTP──▶ api.Server（RequireAuth）
                  ├─ 憑證：-api-key（admin）、api_tokens（scope+permissions）、/api/login 換 token
                  ├─ 路由總表見下方「REST API」
                  └─ /api/socket ──▶ WebSocket：位置 / 事件 / 裝置狀態
```

## 目錄

| 路徑                                      | 說明                                                                                       |
| ----------------------------------------- | ------------------------------------------------------------------------------------------ |
| `cmd/traccar-gateway`                     | 主要進入點：設定綁定、listener 啟動、API server                                            |
| `cmd/device-sim`                          | 開發用設備模擬器（Live lanes）                                                             |
| `internal/model`                          | 統一資料模型：`Position` / `Network` / `Command` / `User` / `Notification`…                |
| `internal/auth`                           | 權限模型：scope、`Principal`、token 產生 / SHA-256、密碼 bcrypt                            |
| `internal/codec`                          | 協議介面：`FrameDecoder` / `Decoder` / `SessionDecoder` / `Encoder` / `Base`               |
| `internal/protocol/*`                     | 各協議 codec（純函式），`protocaltest` 是共用測試斷言                                      |
| `internal/gateway`                        | 網關：listener、切幀、解碼、Router、Dispatcher（命令）                                     |
| `internal/handler`                        | `ProcessingHandler` 處理鏈（filter…media）                                                 |
| `internal/alert`                          | 告警規則評估（超速 / 斷電 / 復電 / 圍欄）                                                  |
| `internal/geofence`                       | 地理圍欄                                                                                   |
| `internal/notify`                         | 通知管道：webhook、SMTP，及 Telegram / Pushover / Firebase / WhatsApp / SMS 等 notificator |
| `internal/api`                            | REST API server 與各路由 handler                                                           |
| `internal/storage/postgres`               | PostgreSQL store + 17 支 migration（pgx v5）                                               |
| `internal/report` + `internal/schedule`   | 報表與排程寄送                                                                             |
| `internal/forward`                        | 位置 / 事件外送（HTTP、MQTT、Redis、Wialon…）                                              |
| `internal/media`                          | 媒體檔案上傳 / 下載                                                                        |
| `internal/config`                         | 三層設定 + flag 綁定                                                                       |
| `internal/metrics` / `internal/stats`     | Prometheus `/metrics` 與統計寫入                                                           |
| `internal/session` / `internal/websocket` | 連線登錄 / WebSocket hub                                                                   |
| `scripts/`                                | `smoke.sh`、`rest-e2e.sh` 開發腳本                                                         |

## 快速開始

需求：Go 1.22+（`make build` 只用標準工具鏈，無 cgo）。

```bash
cd go_server

make build        # 產出 bin/traccar-gateway 與 bin/device-sim
make test         # go test ./...
make smoke        # 無資料庫網關，重播四協議場景，看 position 進 sink

make run          # 記憶體模式跑網關，position 印到 log（REST 關閉）

# 接 PostgreSQL（自動建表 / migration）
make db-up        # docker run postgres:16，監聽 55432
TRACCAR_DSN='postgres://traccar:traccar@localhost:55432/traccar?sslmode=disable' make run-db

# 另一個終端重播場景並驗證 REST 位置 / 告警 / 命令
make rest-e2e
```

`make` 不帶參數會列出所有 target。`make install` 裝到 `$PREFIX/bin`（預設 `~/.local`）。

Docker 一把拉起網關 + PostgreSQL：

```bash
docker compose up --build -d
curl -H 'X-Api-Key: dev-key' http://127.0.0.1:8082/api/health
docker compose down -v
```

## 設定

三層（優先序由低到高）：內建預設 → `traccar.xml` → 環境變數；命令列 flag 在最上層。
設定檔預設讀 `./traccar.xml`，可用 `-config` 或 `TRACCAR_CONFIG` 指定。key 名照母本
`config/Keys.java`；Go 才有的 key 在 `internal/config/keys.go` 標了 `Go only`。

```xml
<properties>
    <entry key='web.address'>0.0.0.0:8082</entry>
    <entry key='database.url'>postgres://127.0.0.1:5432/traccar?sslmode=disable</entry>
    <entry key='protocol.gt06.address'>0.0.0.0:5023</entry>
    <entry key='protocol.gt06.timeout'>30s</entry>
    <entry key='protocol.h02.registerUnknown'>false</entry>
</properties>
```

- 環境變數：key 轉大寫、`.` 換 `_`、駝峰拆字、加 `TRACCAR_` 前綴，例如 `database.url` →
  `TRACCAR_DATABASE_URL`、`protocol.gt06.address` → `TRACCAR_PROTOCOL_GT06_ADDRESS`。
- per-protocol 解析順序：`protocol.<名稱>.<key>` → `<名稱>.<key>` → `protocol.<key>` → `<key>`。
- 時間值可寫 `30s` / `5m` 或純數字（秒）。`server.timeout`（預設 120s）判裝置離線。
- 舊變數仍可用：`TRACCAR_DSN` → `database.url`、`TRACCAR_API_KEY` → `api.key`、`TRACCAR_SMTP_PASSWORD` → `mail.smtp.password`。

預設監聽（同名 flag 可覆寫，傳空字串關閉該協議）：

| 協議 / 服務 | Flag         | 預設位址                                |
| ----------- | ------------ | --------------------------------------- |
| H02         | `-h02`       | `0.0.0.0:5013`                          |
| GT06        | `-gt06`      | `0.0.0.0:5023`                          |
| JT808       | `-jt808`     | `0.0.0.0:8610`                          |
| Teltonika   | `-teltonika` | `0.0.0.0:5027`                          |
| OsmAnd      | `-osmand`    | `0.0.0.0:5055`（transport 預設 `http`） |
| MQTT        | `-mqtt`      | `0.0.0.0:1883`                          |
| GPS103      | `-gps103`    | `0.0.0.0:5002`                          |
| TK103       | `-tk103`     | `0.0.0.0:5001`                          |
| Meitrack    | `-meitrack`  | `0.0.0.0:5020`                          |
| Wondex      | `-wondex`    | `0.0.0.0:5006`                          |
| REST        | `-http`      | `0.0.0.0:8082`（需 `-dsn`；空 = 關閉）  |

常用 flag（預設都可先用設定檔 / 環境變數給）：`-dsn`、`-api-key`、`-allow-anonymous`（本機關
REST 認證）、`-auto-register`（預設 true）、`-batch-size`(500) / `-batch-interval`(1s)、
`-overspeed-knots` / `-power-cut-volts`（0 = 關閉規則）、`-alert-repeat`(0)、
`-geofence-refresh`(5m) / `-device-refresh`(5m)、`-login-max-failures`(5) /
`-login-lock-duration`(15m)、`-issue-token` / `-token-scopes` / `-token-ttl`、
`-command-retry`(30s) / `-command-attempts`(3) / `-command-queue`(32) /
`-command-queue-ttl`(5m) / `-command-retention`(168h)、`-disable-share`、
`-notify-webhook` / `-notify-email-smtp` 等通知 flag。

只走設定檔 / 環境變數的服務（沒有 flag，避免命令列愈長）：

| 功能 | Key | 說明 |
| --- | --- | --- |
| 位置屬性複製 | `position.copyAttributes`（逗號分隔） | 掛上 CopyAttributesHandler；空 = 關閉 |
| 帳號影像 | `deviceImage.path` | 預設 `<media.path>/devices`；空且沒有 media 時路由回 501 |
| HLS 串流 | `stream.path` | 外部產生器寫 `<stream.path>/<deviceId>/index.m3u8`；路由唯讀 |
| Redis 推播 | `redis.url`、`redis.channel`（預設 `traccar`） | 跨節點分享 WebSocket 位置 / 事件 / 裝置狀態 |
| OpenID Connect | `openid.enable`、`openid.issuer`、`openid.clientId`、`openid.clientSecret`、`openid.redirectUrl`、`openid.scopes`、`openid.createMissing`、`openid.successUrl` | 端點由 issuer 的 discovery document 取得 |
| LDAP | `ldap.url`、`ldap.user`、`ldap.password`、`ldap.base`、`ldap.userFilter`、`ldap.nameAttribute`、`ldap.mailAttribute`、`ldap.timeout` | `userFilter` 的 `{0}` 會做 RFC 4515 escaping |
| 網路外送 | `forward.network.address`、`forward.network.protocol`（`tcp` / `udp`） | 每個位置 / 事件一行 JSON |
| 地理服務 | `geocoder.type`（nominatim / google / opencage / here / locationiq / mapbox / tomtom / photon / pelias）、`geolocation.type`（google / mozilla / opencellid / unwired / here） | 其餘 provider 用 `geocoder.url` / `geolocation.url` 接 |
| 排程 | `schedule.inactive.interval`、`schedule.deviceExpiration.interval`、`schedule.events.interval` / `.retention`、`schedule.positions.interval` / `.retention`、`schedule.commands.interval` / `.retention` | retention 為 0 時不刪資料 |

## 協議支援矩陣

- **H02**：文字 `V1/V3/V4/V5/V6/V8/V19`、`NBR`、`LINK`、`VP1`、`HTBT`、`SMS`、心跳；
  二進位 32/45 byte 幀、`X` 幀、`R12` 應答。待補：無。
- **GT06**：`0x01` 登入（含時區偏移）、`0x23` 心跳、GPS/LBS/狀態主線
  （`0x10/0x11/0x12/0x16/0x1A/0x1E/0x22/0x25/0x26/0x27/0x31/0x32/0x37/0x39/0xA0/0xA4`）、
  `0x2A` 位址、`0x8A` 對時、`0x94` 電壓，全部回 `CRC16/X25` ACK。`NT_MODELS`
  （`NT11/NT20/NT26/NT40/NT46/VL100/XT40`）在 `0x22` / `0x25` 走自訂版面。待補：`0x94` 子類型、
  WiFi 掃描（`0x17/0x2C/0x33/0xA2`）、`0xA3` variant、ADAS/DMS。
- **JT808**：2013 六碼 / 2019 十碼標頭、`0x7E` 轉義、XOR 校驗、`0x0100` 註冊 → `0x8100`、
  `0x0102/0x0002/0x0003/0x1007` → `0x8001`、`0x0200` 位置（含警報 / 狀態 / BCD 時間 / 附加項）、
  `0x0201`、`0x0107` ICCID、`0x0109` 對時。待補：`0x0704/0x0210` 批次、`0x5501/0x5502`、
  `0x0702` 駕駛員卡、`0x0104` 參數、`0x0900/0x8900` 透傳、ADAS/DMS/胎壓/OBD。
- **Teltonika**：IMEI 握手（回 `0x01/0x00`）、`0xFF` 心跳、Codec 8 / 8E / 12 / 13、
  1/2/4/8 byte IO 區塊、Codec 8E 可變長度元素（VIN、DTC）、LAC/CID → CellTower。待補：
  Codec 7 (GH3000)、Codec 16、BLE beacon、`io78` driver id、圖片傳輸。
- **OsmAnd**：HTTP 查詢字串 → 位置；同一支 `Decode` 也被 MQTT 重用。
- **MQTT**：3.1.1（CONNECT / SUBSCRIBE / PUBLISH / PINGREQ），publish payload 是 OsmAnd 查詢字串。
- **GPS103 / TK103**：`imei:` 文字句、十進位度 + 半球（TK103 為 `ddmm.mmmm`）。
- **Meitrack**：`$$` + 長度 + 逗號欄位 + `*` + XOR 檢查碼。
- **Wondex**：`#DD#` + `;` 分隔欄位。
- **Suntech**：`STT` / `ALT` / `EMG` / `EVT` / `UEX` / `BLE` 文字家族、universal 版面、zip 與二進位記錄、
  travel report。待補：Encoder（目前只收不能下命令）。
- **Ruptela**：IMEI 握手、心跳、Records / Extended Records（msg 68）、DTC、IO 設定與版本查詢；
  Encoder 支援 custom / engine stop / engine resume / request photo / config / get version。

## 資料庫

`internal/storage/postgres/migrations/*.sql`（17 支）由 `Store.Migrate` 依版本套用，主要表：

- `devices(id, unique_id, name, model, protocol, group_id, attributes, status, last_update, created_at)`
- `positions(id, device_id, protocol, device_time, server_time, valid, latitude, longitude, altitude, speed, course, accuracy, attributes)`
- `events(id, device_id, position_id, geofence_id, type, event_time, attributes)`
- `geofences`、`groups`、`calendars`、`drivers`、`maintenances`、`orders`、`reports`
- `users`、`user_devices`、`api_tokens`、`commands`、`password_resets`、`notifications`、
  `notification_rules`、`permissions`、`media`、`protocols`（設定表）

## REST API

認證用 `-api-key`（admin）、`api_tokens`（scope + permissions）或 `/api/login` 換發的 token；
`/api/health`、`/api/login`、密碼自助路由與 `/api/socket` 是例外。scope 控制能讀寫的資源，
非 admin 只能存取自己被授權的裝置（`user_devices`）。本機可用 `-allow-anonymous` 關閉認證。

| 區塊      | 路由                                                                                                                                                                  |
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| 事件      | `GET /api/events`、`GET /api/events/{id}`（`/api/alerts` 為舊名別名）                                                                                                 |
| 裝置      | `GET/POST /api/devices`、`GET/PUT/DELETE /api/devices/{id}`                                                                                                           |
| 位置      | `GET /api/positions`、`GET /api/positions/{kml,kmz,csv,gpx}`                                                                                                          |
| 命令      | `GET/POST /api/commands`（落 commands 表 → Router → encoder → 設備連線）、`GET /api/commands/send`                                                                    |
| 健康/指標 | `GET /api/health`、`GET /metrics`（Prometheus）                                                                                                                       |
| WebSocket | `GET /api/socket`（位置 / 事件 / 裝置狀態，token 走 query string）                                                                                                    |
| 帳號      | `GET/POST /api/users`、`PUT/DELETE /api/users/{id}`、`/api/users/{id}/devices`、`/api/users/{id}/tokens`、`GET/POST/PUT/DELETE /api/users/{id}/totp`、`POST /api/users/totp` |
| 認證      | `POST /api/login`（可帶 TOTP `code`）、`POST /api/password/forgot`、`POST /api/password/reset`、`POST /api/password/update`、`PUT /api/users/me/password`、`GET /api/session/openid/auth`、`GET /api/session/openid/callback` |
| 通知      | `GET/POST/PUT/DELETE /api/notifications`、`/api/users/{id}/notifications`、`GET /api/notificators`、`GET /api/notifications/types`、`POST /api/notifications/test`、`/api/notifications/send/{notificator}` |
| 權限/分享 | `/api/permissions`、`POST/DELETE /api/permissions/bulk`、`POST /api/share/device`、`POST /api/share/group`（`-disable-share` 可關）                                                                    |
| 資源 CRUD | `/api/groups`、`/api/geofences`、`/api/calendars`、`/api/drivers`、`/api/maintenance(s)`、`/api/orders`、`/api/reports`、`/api/attributes/computed`、`GET/POST /api/attributes/computed/test`           |
| 管理      | `/api/protocols`、`/api/server`（含 timezones / geocode / speedlimit / geolocation / gc / reboot / file）、`GET /api/server/cache`、`/api/statistics`、`/api/statistics/devices`、`/api/audit`          |
| 會話      | `GET/POST/DELETE /api/session`、`/api/session/{id}`、`/api/session/token`、`/api/session/token/revoke`                                                                |
| 媒體      | `GET/POST /api/media`、`GET/DELETE /api/media/{id}`                                                                                                                   |
| 裝置附屬  | `GET/PUT /api/devices/{id}/accumulators`、`GET/POST/DELETE /api/devices/{id}/image`                                                                                    |
| 串流      | `GET /api/stream/{deviceId}`、`GET /api/stream/{deviceId}/{file...}`（HLS playlist / segment）                                                                         |

資源型路由（groups / geofences / …）是同一組 Simple/ExtendedObjectResource handler 的 CRUD；
`PUT` 是整欄取代。未啟用對應 store 的路由回 501。

### 命令

- 下發：`POST /api/commands` 落 `commands` 表（`queued` 欄位）→ 透過 Router 送給設備連線。
- 佇列與重送：每裝置在記憶體佇列排隊（`-command-queue`）、依回應收尾、逾時重送
  （`-command-retry` / `-command-attempts`），重啟後靠 `pending` 復原補送。
- 配對：JT808 用流水號精確配對；GT06 / H02 / Teltonika 回應沒有識別碼，只認「該裝置最新一筆
  `sent`」。`-command-retention`（預設 168h）決定歷史保留多久。

### 通知

規則在 `alert.Pipeline` 評估後以事件驅動：webhook（`-notify-webhook`）與 SMTP email
（`-notify-email-smtp` 等）。使用者訂閱決定「類型 × 管道」（`users → notifications`）。
另有 notificator 設定 key：Telegram、Pushover、Firebase、WhatsApp、SMS HTTP / AWS SNS
（`internal/config/keys.go` 的 `notificator.*` / `sms.*`）。

## 開發

- `make smoke`：無資料庫網關重播四協議場景。
- `make rest-e2e`（`scripts/rest-e2e.sh`）：接 PostgreSQL 重播場景，驗證位置 / 告警 / 命令整條鏈。
- `cmd/device-sim`：Live lanes 設備模擬器。
- `make race`：以 race detector 跑 `internal/gateway` `internal/alert` `internal/api` `internal/notify`。

## 已知限制

- 接了 `-dsn` 後 REST 預設全關：沒設 `-api-key` 又無有效 token 時，除 `health` / `login` / 密碼自助
  / `socket` 外都回 401。首次使用先帶 `-api-key` 建立管理員並發 token，或直接用 `-issue-token`。
- 帳號 API 只有分頁，無搜尋 / 排序；email 建立後不可改。密碼自助與登入失敗鎖定（bcrypt cost 12，
  一次登入約 0.6 秒）見「認證」。
- `PUT /api/devices/{id}` 可改 `name` / `model` / `phone` / `contact` / `category` / `disabled` /
  `expirationTime` / `calendarId` / `attributes`（整筆取代語意，省略即清空）；`protocol` 與 `uniqueId`
  建立後不可改。`disabled` 目前只儲存與回傳，尚未用來擋資料。
- `alert` 引擎只算超速 / 斷電 / 復電 / 圍欄進出；點火、行為、油量、駕駛、保養、媒體、命令結果
  由 `handler` 的規則產生。群組授權只影響「看得到哪些裝置」，通知收件者仍只認 `user → device`
  直接授權（未展開群組）。
- 分享帳號只能靠 admin 事後刪除撤銷（無到期自動清理）；`expiration` 只作用在 token 上。
- `/api/socket` 的 token 放 query string（瀏覽器無法對 WebSocket 設 header），會出現在存取紀錄裡。
- `suntech` / `ruptela` 已納入預設啟動清單；`suntech` 沒有 Encoder，只能收不能下命令。
- 記憶體目錄（無 `-dsn`）僅適合開發；正式環境請用 PostgreSQL。
- TOTP / OpenID / LDAP / HLS / 裝置影像 / Redis 推播都要在設定檔或環境變數開啟；沒設定的路由回
  501，不會默默使用不安全預設。

## 與母本差距

母本 `../traccar-master` 的規模與 Go 版現況（`0e35d75` 之後實測）：

| 面向 | 母本 | Go 版 | 缺口 |
| --- | --- | --- | --- |
| 協議解碼器 | 268 | 12 | 256 |
| 命令 Encoder | 65 | 5 | 60（gps103 / tk103 / meitrack / wondex / suntech 只收不下） |
| 位置處理 handler | 19 | 19（Go 用 15 個 stage 表達，另有 CopyAttributesHandler） | 0 |
| 事件處理器 | 12 | 12（Proximity 已接進 `alertEvent`） | 0 |
| OpenAPI 路徑 | 76 | 76（另有 Go 專屬別名） | 0 |
| 地理服務 provider | geocoder 24 / geolocation 4 / speedlimit 1 / mapmatcher 1 | 9 / 5 / 1 / 1 | geocoder 15 |
| 外送 | JSON、URL、MQTT、Redis、Wialon、AMQP、Kafka、NetworkForwarder | JSON、URL、MQTT、Redis、Wialon、NetworkForwarder | AMQP、Kafka |
| 資料庫 | PostgreSQL / MySQL / H2 | PostgreSQL | 兩種 DB；schema 自成一格，不能接母本資料庫 |
| 認證 | 密碼、TOTP、OpenID Connect、LDAP | 密碼、token、scope、分享、TOTP、OpenID Connect、LDAP | 0 |
| 水平擴展 | Redis broadcast / multicast | Redis pub/sub 跨節點推播；裝置 session 仍為單節點 | multicast；跨節點 session 檢視 |
| 影像 | JT1078 協議 + HLS 串流 | 媒體上傳 / 下載、HLS 端點 | JT1078 解碼與切片 |
| 排程任務 | 8 | 9（cleanup / expiration / health / reports / inactive / deviceExpiration / events / positions / commands） | temporary 帳號到期刪除 |

### 1. 協議層（最大缺口）

- 268 支裡做了 12 支：h02、gt06、jt808、teltonika、gps103、tk103、meitrack、wondex、osmand、
  mqtt、suntech、ruptela。
- 其餘 256 支未實作。常見且值得排的：Queclink、Meiligao、Concox 系列、Eelink、Xexun、Gt02、
  Pt502 / Pt3000 / Pt60、Jt600、GoSafe、Bce、Cellocator、Castel、GpsGate、Cradlepoint、Wialon、
  Watch、Eseal、Topflytech。
- 已做的協議仍有子功能缺口，逐條列在「協議支援矩陣」的「待補」。

### 2. 處理鏈（已完成）

TimeHandler、HemisphereHandler、ComputedAttributesHandler、GeolocationHandler、GeocoderHandler、
CopyAttributesHandler、EngineHoursHandler 都已實作並接進對應 stage；`computedAttributes` 由
`internal/expr` 求值，支援算術、比較、邏輯、三元運算與 `abs` / `min` / `max` / `pow` / `coalesce`
等函式。position 的欄位與屬性都可當變數使用。`GET /api/attributes/computed/test` 走同一個求值器。

仍與母本不同的地方：

- `CopyAttributesHandler` 只有在 `position.copyAttributes` 列出 key 時才掛進鏈；要複製哪些 key
  是協議決定的，所以沒有預設值。
- 事件規則的歷史各自保存（每條規則一份 last position），母本共用一個 handler state；結果相同，
  記憶體用量略高。
- 地址與 cell tower 查詢在 ingest 路徑上同步執行，設 3 秒上限；母本用 cache + 非同步佇列。

### 3. REST API（已完成）

上一輪列出的 10 條路徑都已補上：`/api/attributes/computed/test`、`/api/devices/{id}/accumulators`、
`/api/devices/{id}/image`、`/api/permissions/bulk`、`/api/users/{id}/totp` 與 `/api/users/totp`、
`/api/session/openid/auth`、`/api/session/openid/callback`、`/api/server/cache`、
`/api/stream/{deviceId}` 與 `/api/stream/{deviceId}/{file...}`。`POST /api/password/update` 也已補上，
官方 Web 介面不必改路徑。

- `accumulators` 存在裝置的 `attributes.totalDistance` / `attributes.hours`，PUT 只改有帶的欄位。
- `image` 支援 multipart 的 `file` 欄位或直接 body，內容必須是 `image/*`，上限 5 MiB。
- HLS 是唯讀視圖：外部產生器（例如 MediaMTX）寫檔，Go 只做授權與 content type。
- TOTP secret 存在 `users.totp_key`，`totp_enabled` 要驗過一次 code 才會打開；登入時 `/api/login`
  與 `/api/session` 都可帶 `code`。

### 4. 其他基礎設施

- 地理服務：geocoder 9 個 provider、geolocation 5 個，其餘用 `geocoder.url` / `geolocation.url`
  接自訂端點。
- 外送：NetworkForwarder（TCP / UDP JSON line）已補；AMQP 與 Kafka 仍未實作。
- 只有 PostgreSQL，schema 與母本不相容。
- 水平擴展：`redis.url` 開啟跨節點 WebSocket 推播；認證 token 本來就在資料庫，所以登入狀態已共享。
  裝置 session 清單仍是單節點記憶體。
- JT1078 影像協議未實作；HLS 端點與媒體上傳已完成。
- TOTP、OpenID Connect、LDAP 都已完成；LDAP 走 BER，不引入外部 driver。
- 排程補了 `deviceInactive`、`deviceExpiration`、events / positions / commands retention；
  `queuedCommandSent` 由 dispatcher 在「排隊後第一次送出」時產生。

### 建議順序

1. 依實際客戶設備清單補協議（不要追 268 支），每支補完順手補 Encoder。
2. 需要第三方訊息匯流排時再補 AMQP / Kafka producer；目前 HTTP、MQTT、Redis、Wialon、
   NetworkForwarder 已足夠單一服務消費。
3. 要接母本既有資料庫或 MySQL / H2 時，先做 storage 介面層與 schema 對照，不要直接改 PostgreSQL
   的 migration。
4. 需要即時影像時再實作 JT1078 解碼與切片；HLS 端點已可接外部產生器。
