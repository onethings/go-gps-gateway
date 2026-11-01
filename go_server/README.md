# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的主力設備協議（H02 / GT06 / JT808 / Teltonika），
只做「解碼 → 標準 Position → 落地」，網路層與業務層之後再分段替換。

- 母本：`../traccar-master`（Java），每個 Go 檔案的註解都標明了對應的 Java 類別。
- 設計原則：協議層是**純函式**（`[]byte` → `*model.Position`），所有 I/O 集中在 `internal/gateway`。
- 現況：四個協議的 codec + TCP 網關 + PostgreSQL 落地 + 三層設定（`traccar.xml` / 環境變數 / flag）
  + `ProcessingHandler` 處理鏈 + 連線狀態（`devices.status`、online / offline、心跳逾時）+ `events` 表
  （告警規則 / 地理圍欄 / 通知 webhook / email，含使用者訂閱）
  - REST（裝置管理、位置查詢、事件查詢（`/api/events`，`/api/alerts` 為別名）、健康檢查、命令下發、
    `/metrics`、使用者 token / 裝置擁有權、帳號與 token 管理、密碼登入、通知訂閱）全部可編譯、
    `go test ./...` 全綠。

## 架構

```
device ──TCP──▶ gateway.Server (每連線一個 goroutine)
                   │
                   ├─ FrameDecoder   切幀（半包 / 粘包 / 轉義 / 校驗）
                   ├─ Decoder        frame → []*model.Position + [][]byte 回包
                   ├─ SessionDecoder 需要綁連線身分的協議（GT06 / Teltonika）
                   └─ Result.Responses 原樣寫回設備（ACK / 註冊回應 / 對時）
                   │
                   ├─ DeviceDirectory   uniqueId ⇄ deviceId
                   ├─ LocationCache    每個裝置最後一筆定位（供心跳/參數訊息補齊）
                   ├─ session.Manager  連線登錄表 → devices.status + deviceOnline / deviceOffline
                   └─ Sink = handler.Chain   ProcessingHandler 的處理鏈
                        ├─ 14 個宣告階段（filter … media）；已實作：motion、business，其餘留 Placeholder
                        ├─ business          = alert.Pipeline（規則評估 → events 與 position 同交易落地）
                        │    └─ notify.Dispatcher  webhook / SMTP 通知（背景傳送）
                        └─ terminal          = postgres.BatchSink（positions 批次落地）

client ──HTTP──▶ api.Server (RequireAuth)
                   ├─ 憑證：-api-key（admin）、api_tokens（scope + permissions）或 /api/login 換發的 token
                   ├─ GET /api/devices   ──▶ devices 查詢 / 新增 / 修改 / 刪除
                   ├─ GET /api/positions ──▶ positions 查詢（自動加裝置條件）
                   ├─ GET /api/events    ──▶ events 查詢（/api/alerts 是別名；自動加裝置條件）
                   ├─ GET /api/health
                   ├─ POST /api/login    ──▶ email + 密碼換發一組 token（唯一不用帶憑證的路由）
                   ├─ GET /metrics       ──▶ Prometheus 文字格式
                   ├─ POST /api/commands ──▶ commands 落一列 ──▶ gateway.Router ──▶ 該協議 encoder ──▶ 設備連線
                   ├─ GET /api/commands  ──▶ commands 查詢（自動加裝置條件）
                   ├─ /api/users/*       ──▶ 帳號 / token / 裝置授權 / 通知訂閱（admin 或本人）
                   ├─ /api/notifications ──▶ 通知範本（admin 維護，任何帳號可讀）
                   ├─ /api/{groups,geofences,calendars,drivers,maintenance,orders,reports}、/api/attributes/computed
                   │                      ──▶ 設定型資源 CRUD（參數照母本 Simple / ExtendedObjectResource）
                   └─ /api/socket        ──▶ WebSocket：位置 / 事件 / 裝置狀態（token 走 query string）
```

## 目錄

| 路徑                             | 說明                                                                                                                                                                                                                                                                   |
| -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/model`                 | 統一資料模型：`Position` / `Network` / `CellTower` / `Command` / `User` / `Notification`                                                                                                                                                                               |
| `internal/auth`                  | 權限模型：scope、`Principal`、token 產生 / SHA-256 雜湊、密碼 bcrypt 雜湊                                                                                                                                                                                              |
| `internal/codec`                 | 協議介面：`FrameDecoder`、`Decoder`、`SessionDecoder`、`Encoder`、`Base`                                                                                                                                                                                               |
| `internal/helper`                | Java helper 的移植：`Buffer`(Netty ByteBuf)、`PatternBuilder`、`Parser`、BCD、DateBuilder、CRC16/X25、CRC16/IBM、XOR、Luhn                                                                                                                                             |
| `internal/protocol/h02`          | H02 / TK102 系                                                                                                                                                                                                                                                         |
| `internal/protocol/gt06`         | GT06 / Concox / Jimi 系                                                                                                                                                                                                                                                |
| `internal/protocol/jt808`        | JT/T 808（中國交通部標準）                                                                                                                                                                                                                                             |
| `internal/protocol/teltonika`    | Teltonika FMxxx / FMB（Codec 8 / 8E / 12 / 13）                                                                                                                                                                                                                        |
| `internal/protocol/protocaltest` | 共用測試斷言（`ProtocolTest` 的移植）                                                                                                                                                                                                                                  |
| `internal/gateway`               | TCP 網關、協議註冊表、裝置目錄、位置快取、Sink 介面、連線表與命令路由                                                                                                                                                                                                  |
| `internal/geofence`              | 圍欄幾何：Traccar WKT（`POLYGON` / `CIRCLE`）→ 內含判定（含反子午線）                                                                                                                                                                                                  |
| `internal/session`               | 連線登錄表（`devices.status`、心跳超時、`deviceOnline` / `deviceOffline`）與 moving / stopped 狀態機（`motion` 階段）                                                                                                     |
| `internal/handler`               | 位置處理鏈：母本 `ProcessingHandler` 的 14 個階段順序、`PositionHandler` 介面、`BusinessHandler`（一次跑一個 `Processor`）與 terminal store                                                                                                                                  |
| `internal/alert`                 | 告警規則（overspeed / powerCut / geofenceEnter / geofenceExit）+ 非阻塞寫入管線；在處理鏈上是 `business` 這一段                                                                                                                                                          |
| `internal/notify`                | 告警通知：webhook / SMTP email、訊息格式化、裝置 attributes 覆寫、使用者訂閱路由、非阻塞傳送佇列                                                                                                                                                                       |
| `internal/api`                   | REST：`GET/POST/PUT/DELETE /api/devices`（含 `GET /{id}`、`uniqueId` / `id` / `keyword` / `excludeAttributes`、可寫 `attributes`）、`GET /api/positions`（`geofenceId`）與 `kml` / `kmz` / `csv` / `gpx` 匯出、`GET /api/events`（`/api/alerts` 為別名）與 `GET /{id}`、`GET /api/health`、`POST /api/login`、`GET /metrics`、`GET/POST /api/commands`、`/api/users`（帳號 / token / 裝置授權 / 通知訂閱）、`/api/notifications`、資源 CRUD（`/api/groups`、`/api/geofences`、`/api/calendars`、`/api/drivers`、`/api/maintenance(s)`、`/api/orders`、`/api/reports`、`/api/attributes/computed`）、`GET /api/socket`（WebSocket 推播）；認證 / scope / 裝置擁有權 |
| `internal/websocket`             | RFC 6455 伺服器端子集：握手、文字 / 二進位訊息與 fragmentation、ping / pong、close（無 extension）  |
| `internal/metrics`               | Prometheus 文字格式的計數器 / gauge / histogram（零依賴）                                                                                                                                                                                                              |
| `internal/storage/postgres`      | PostgreSQL schema/migration、`DeviceDirectory` 與批次 `Sink`                                                                                                                                                                                                           |
| `cmd/traccar-gateway`            | 啟動四個監聽埠與 REST 的 CLI                                                                                                                                                                                                                                           |

## 快速開始

需求：Go 1.22 以上（`make build` 只用標準工具鏈，沒有 cgo）。PostgreSQL 只有要跑 REST /
告警 / 命令歷史時才需要。

下面的區塊帶 `#` 註解，貼進互動式 zsh 前先 `setopt interactive_comments`，否則註解會被當成參數
（zsh 預設只在腳本裡認註解）。

```bash
cd go_server

# 0) 先確認這一版在你機器上能跑：編譯、測試、四協議實跑
make build        # 產出 bin/traccar-gateway 與 bin/device-sim
make test         # go test ./...
make smoke        # 起一個沒有資料庫的網關，重播四個協議的場景，看 position 有沒有進 sink

# 1) 只跑網關，position 以 JSON 印到 log（不需要資料庫）
make run          # 等於 ./bin/traccar-gateway（記憶體模式，REST 關閉）

# 2) 接 PostgreSQL（自動建表 / 套用 migration）
make db-up        # docker run postgres:16，監聽 55432
TRACCAR_DSN='postgres://postgres:traccar@localhost:55432/traccar?sslmode=disable' make run-db

# 3) 另一個終端：把裝置幀打進去，再用 REST 驗證位置、告警、命令整條鏈
make rest-e2e
```

`make` 不帶參數會列出所有 target。要裝進 PATH：

```bash
make install                    # 裝到 ~/.local/bin
make install PREFIX=/usr/local  # 或系統目錄（需要寫入權限）
make uninstall
```

只想用 Go 原生的方式裝：

```bash
go install ./cmd/...            # 裝到 GOBIN（預設 $(go env GOPATH)/bin）
```

Docker 一把拉起網關 + PostgreSQL：

```bash
docker compose up --build -d
curl -H 'X-Api-Key: dev-key' http://127.0.0.1:8082/api/health
docker compose down -v
```

### 設定

三層，優先序由低到高：內建預設 → `traccar.xml` → 環境變數；命令列 flag 在最上層。每個 flag 的
預設值就是前三層算出來的結果，所以不帶 flag 時就是設定檔說的值。

設定檔預設讀 `./traccar.xml`（不存在就用內建預設），路徑可用 `-config` 或 `TRACCAR_CONFIG`
指定；檔案裡的 `config.default` 會先載入另一個檔案，再被自己覆寫（跟母本一樣）。

```xml
<properties>
    <entry key='web.address'>0.0.0.0:8082</entry>
    <entry key='database.url'>postgres://127.0.0.1:5432/traccar?sslmode=disable</entry>
    <entry key='protocol.gt06.address'>0.0.0.0:5023</entry>
    <entry key='protocol.gt06.timeout'>30s</entry>
    <entry key='protocol.h02.registerUnknown'>false</entry>
</properties>
```

- key 名照母本 `config/Keys.java`；Go 才有的 key（批次寫入、命令佇列、通知管道）在
  `internal/config/keys.go` 標了 `Go only`。
- 環境變數名是 key 轉大寫、`.` 換成 `_`、駝峰拆字，前面加 `TRACCAR_`：
  `database.url` → `TRACCAR_DATABASE_URL`、`alert.overspeedKnots` →
  `TRACCAR_ALERT_OVERSPEED_KNOTS`、per-protocol 是 `TRACCAR_PROTOCOL_GT06_ADDRESS`。
- per-protocol 值解析順序：`protocol.<名稱>.<key>` → `<名稱>.<key>`（母本 `default.xml` 的寫法）
  → `protocol.<key>`（協議共用預設）→ `<key>`（全域）。第一個有值的 key 勝出，所以分段覆寫
  不受層級影響。
- 時間值可以寫 `30s`、`5m`，也可以只寫數字（母本的時間 key 是秒）。
- `server.timeout`（預設 `120s`，母本同名）：裝置多久沒送資料就判離線、寫 `deviceOffline`；
  `protocol.<名稱>.timeout` 是單一協議的連線讀取逾時。

舊環境變數照舊可用：`TRACCAR_DSN` → `database.url`、`TRACCAR_API_KEY` → `api.key`、
`TRACCAR_SMTP_PASSWORD` → `mail.smtp.password`；兩種寫法同時出現時以新的 key 名為準。

REST 預設需要憑證（`-api-key` 或資料庫 token）；本機想直接 curl 可加 `-allow-anonymous`
（見「認證與權限」）。

預設監聽（可用同名 flag 覆寫，傳空字串可關閉該協議）：

| 協議 / 服務 | Flag         | 預設位址                                 |
| ----------- | ------------ | ---------------------------------------- |
| H02         | `-h02`       | `0.0.0.0:5013`                           |
| GT06        | `-gt06`      | `0.0.0.0:5023`                           |
| JT808       | `-jt808`     | `0.0.0.0:8610`                           |
| Teltonika   | `-teltonika` | `0.0.0.0:5027`                           |
| REST        | `-http`      | `0.0.0.0:8082`（需要 `-dsn`；空 = 關閉） |

以下 flag 的預設值都可以先用上面的設定檔或環境變數給，flag 只是最後一層覆寫：
`-batch-size`（預設 500）、`-batch-interval`（預設 1s）、
`-dsn`（預設讀 `TRACCAR_DSN`；空 = 記憶體模式）、
`-overspeed-knots`（預設 0 = 關閉規則）、`-power-cut-volts`（預設 0 = 關閉規則）、
`-overspeed-prefer-lowest`（多個圍欄重疊時取最低限速；預設取最高）、
`-alert-repeat`（預設 0 = 不抑制重複告警）、`-geofence-refresh`（預設 5m，0 = 只在啟動時載入一次）、
`-device-refresh`（預設 5m，0 = 只在啟動時載入一次；每裝置門檻用的 attributes）、
`-api-key`（預設讀 `TRACCAR_API_KEY`；空 = 只用資料庫 token 認證）、
`-issue-token` / `-token-scopes` / `-token-ttl`（發 token 後結束，需要 `-dsn`）、
`-login-max-failures`（預設 5，0 = 關閉登入失敗鎖定）與 `-login-lock-duration`（預設 15m）、
`-command-retry`（預設 30s：寫進連線後等回應多久才重送）、
`-command-attempts`（預設 3：同一筆命令最多寫幾次）、
`-command-queue`（預設 32：每台裝置最多排幾筆未送出的命令）、
`-command-queue-ttl`（預設 5m：命令最多等裝置重連多久）、
`-command-retention`（預設 168h：命令歷史保留多久，0 = 永久保留；見「命令保留期限」）、
`-allow-anonymous`（關閉 REST 認證，只給本機開發用）、
`-disable-share`（對應母本的 `disableShare`：關掉 `POST /api/share/*`；見「權限 API」）、
`-auto-register`（預設 true：未知識別碼的第一幀就建裝置列；false = 丟掉那幀）、
`-notify-webhook` / `-notify-email-smtp` 等通知 flag（見「通知管道」）。

## 協議支援矩陣

### H02

- 文字：`V1/V3/V4/V5/V6/V8/V19`、`NBR`、`LINK`、`VP1`、`HTBT`、`SMS`、心跳。
- 二進位：32 byte 短幀與 45 byte 長幀、`X` 幀、`R12` 應答（`Base.Ack`）。
- 待補：無（以 Java 測試向量覆蓋）。

### GT06

- 已支援：`0x01` 登入（含時區偏移）、`0x23` 心跳、GPS/LBS/狀態主線
  （`0x10/0x11/0x12/0x16/0x1A/0x1E/0x22/0x25/0x26/0x27/0x31/0x32/0x37/0x39/0xA0/0xA4`）、
  `0x2A` 位址查詢、`0x8A` 對時、`0x94` 電壓、所有訊息回 `CRC16/X25` ACK。
- `NT_MODELS`（`NT11` / `NT20` / `NT26` / `NT40` / `NT46` / `VL100` / `XT40`）在 `0x22` 與 `0x25`
  走自己的版面：幀首多一段定位來源＋IMEI＋裝置時鐘，狀態區先讀電壓（伏特）再讀電池（十分之一），
  之後是里程（3 bytes）與引擎時數（3 bytes，毫秒），`0x25` 最後再帶一張駕駛卡。`0x25` 的
  cell id 也比其他訊息寬（4 bytes）。同一幀在沒有 NT model 時不會走這條路 — 這是 Java `modelNT`
  的行為，靠 `devices.model` 選路，改名後要重連才生效。
- 待補：`0x94` 子類型（ADC/告警字串）、WiFi 掃描（`0x17/0x2C/0x33/0xA2`）、`0xA3` variant 判定、
  ADAS/DMS。

### JT808

- 已支援：標頭（2013 六碼 / 2019 十碼 + 版本號）、`0x7E` 轉義、XOR 校驗、
  `0x0100` 註冊 → `0x8100`、`0x0102/0x0002/0x0003/0x1007` → `0x8001`、
  `0x0200` 位置（警報 32 bit、狀態 32 bit、BCD 時間、常用附加項）、`0x0201`、`0x0107` ICCID、`0x0109` 對時。
- 待補：`0x0704`/`0x0210` 批次定位、`0x5501/0x5502`、`0x0702` 駕駛員卡、`0x0104` 參數查詢、
  `0x0900/0x8900` 透傳、ADAS/DMS/胎壓/OBD 區塊。

### Teltonika

- 已支援：IMEI 握手（回 `0x01/0x00`）、`0xFF` 心跳、Codec 8 / 8E / 12 / 13、
  1/2/4/8 byte IO 區塊、Codec 8E 可變長度元素（VIN、DTC、未知項 hex）、LAC/CID → CellTower。
- 待補：Codec 7 (GH3000)、Codec 16 完整支援、BLE beacon、`io78` driver id（LE）、車型專屬參數表、圖片傳輸。

## 資料庫

`internal/storage/postgres/migrations/*.sql` 由 `Store.Migrate` 依版本套用：

- `devices(id, unique_id, name, model, protocol, group_id, attributes, status, last_update, created_at)`
  （`status` 由 `0010_events.sql` 補上：online / offline / unknown；`group_id` 由 `0012_resources.sql` 補上，
  指向 `groups`）
- `positions(id, device_id, protocol, device_time, server_time, valid, latitude, longitude, altitude, speed, course, accuracy, attributes)`
- `events(id, device_id, position_id, geofence_id, type, event_time, attributes)`（`0010_events.sql`；
  `0011_events_replace_alerts.sql` 把舊的 `alerts` 資料搬進來後把該表刪掉）
- `geofences(id, calendar_id, name, description, area, attributes, created_at)`（`0002_alerts.sql`；
  `calendar_id` / `description` 由 `0012_resources.sql` 補上）
- `groups(id, name, group_id, attributes)`、`calendars(id, name, data, attributes)`、
  `drivers(id, name, unique_id, attributes)`、`maintenances(id, name, type, start, period, attributes)`、
  `orders(id, unique_id, description, from_address, to_address, attributes)`、
  `reports(id, calendar_id, type, description, attributes)`、
  `statistics(id, capture_time, active_users, ..., protocols, attributes)`、
  `attributes(id, description, attribute, expression, type, priority)`（皆為 `0012_resources.sql`）
- `users(id, email, name, admin, disabled, created_at, password_hash)`（`0003_users.sql`；`disabled` 由
  `0004_users_disabled.sql` 補上，`password_hash` 由 `0006_users_password.sql` 補上，預設 NULL =
  只能用 token 登入）
- `permissions(owner_type, owner_id, property_type, property_id, created_at)`（`0013_permissions.sql`：
  一張通用多對多表，取代原本的 `user_devices`。母本是一種配對一張 `tc_user_device` /
  `tc_group_geofence` / ...，這裡把配對型別當資料，新增資源不必再加 migration）
- `users.temporary`（`0013_permissions.sql`）：分享連結建出來的唯讀帳號
- `api_tokens(id, user_id, token_hash, scopes, expires_at, created_at)`（`0003_users.sql`）
- `notifications(id, type, notificators, description, created_at)`（`0005_notifications.sql`）
- `user_notifications(user_id, notification_id)`（`0005_notifications.sql`，多對多）
- `commands(id, device_id, type, attributes, status, result_code, sent_at, done_at, created_at)`
  （`0007_commands.sql`；`status` 只接受 pending / sent / done / failed）
- `schema_migrations(version, applied_at)`

寫入路徑：`BatchSink`（非阻塞佇列 + 每批一個 multi-row `INSERT`）。
佇列滿時 `Handle` 回傳 `ErrSinkFull` 並累計 `Dropped()`，不會拖慢 device 連線。

`devices.protocol` 在設備首次登入（`Register`）時寫入，供 `POST /api/commands` 選擇 encoder；
已存在且協議為 NULL 的舊資料會在下一次登入時補上（`COALESCE`，不會覆寫已知值）。
`devices.last_update` 由 `InsertPositions` 在每批寫入後以單一
`UPDATE ... FROM jsonb_to_recordset(...)` 推進，只前進不倒退（補傳舊資料不會讓它倒退）。

## 告警與地理圍欄

`alert.Pipeline` 包在 `BatchSink` 前面，每個 position 先過規則再進批次寫入：

| 規則            | 觸發條件                                                                    | `type`                           |
| --------------- | --------------------------------------------------------------------------- | -------------------------------- |
| 超速            | `Speed`（**節**，decoder 已從 km/h 換算）高於解析後的門檻（見下）           | `deviceOverspeed`                |
| 斷電            | `attributes.power`（**伏特**，GT06 `0x94` 是 12.5）低於解析後的門檻（見下） | `powerCut`                       |
| 復電            | 同一裝置上一個 `power` 低於門檻、這一筆回到門檻以上                         | `powerRestored`                  |
| 進入 / 離開圍欄 | `geofences` 表的幾何內含判定變化（每個裝置各自記狀態）                      | `geofenceEnter` / `geofenceExit` |

門檻可以全域設定，也可以依裝置 / 圍欄覆寫。超速的解析順序（後者覆蓋前者）：

1. `-overspeed-knots`：伺服器預設，`0` = 停用。
2. `devices.attributes.speedLimit`（**節**）：`0` 表示該裝置停用超速規則。
3. `positions.attributes.speedLimit`（**節**）：協議或後處理帶來的位置門檻，`> 0` 才生效。
4. 包含該位置的 `geofences.attributes.speedLimit`（**節**）：`> 0` 才生效；多個圍欄同時包含時
   預設取**最大**值（與 Java `event.overspeed.preferLowest` 未設定時一致），
   加上 `-overspeed-prefer-lowest`（Java 的 `event.overspeed.preferLowest=true`）則取**最小**值。

斷電只有前兩層：`-power-cut-volts` → `devices.attributes.powerCutVolts`（**伏特**，
`0` = 該裝置停用）。`devices.attributes` 仍以 SQL 維護，啟動時載入、之後每 `-device-refresh`
重新載入（預設 5m；`0` 表示只載入一次）。

```sql
-- 單一裝置限速 45 節、斷電門檻 10 V
UPDATE devices SET attributes = '{"speedLimit": 45, "powerCutVolts": 10}'::jsonb WHERE id = 7;

-- 工業區內限速 20 節；高度低於 0 m 或高於 200 m 不算進入
UPDATE geofences SET attributes = '{"speedLimit": 20, "floor": 0, "ceiling": 200}'::jsonb WHERE id = 3;
```

JSONB 的數字與字串形式都接受（`"speedLimit": 45` 或 `"speedLimit": "45"`），寫錯型別會被忽略，
沿用下一層門檻。

`-alert-repeat`（例如 `-alert-repeat 10m`）對超速與斷電做去重：同一裝置、同一類型在視窗內
只寫第一筆。圍欄進出與復電是狀態轉換，不受影響。`0`（預設）維持舊行為，每筆 position 都寫。

`type` 字串沿用 Java `Event.TYPE_*`，之後要跟母本合併時不用改前端。

幾何格式沿用 Traccar 的 WKT 方言，**緯度在前**（與 WKT 標準相反）：

```sql
INSERT INTO geofences (name, area, attributes) VALUES
  ('倉庫', 'POLYGON ((25.20 55.10, 25.20 55.20, 25.30 55.20, 25.30 55.10, 25.20 55.10))',
   '{"floor": 0, "ceiling": 200}'::jsonb),
  ('公司', 'CIRCLE (25.25 55.15, 500)', '{}'::jsonb);
```

`CIRCLE` 的半徑單位是公尺；`floor` / `ceiling` 是公尺高度限制（兩者為 0 或不存在時忽略）。
圍欄已可用 `/api/geofences` 維護（見「資源管理 API」），`-geofence-refresh` 會週期性地重新載入
API 寫進去的內容；仍支援上面的 SQL 寫法。支援的幾何只有 `POLYGON` 與 `CIRCLE`
（母本的 `LINESTRING` 還沒移植）。圍欄對**所有裝置**生效。

查詢：

```bash
curl 'localhost:8082/api/events?deviceId=7&type=deviceOverspeed,powerCut&limit=20'
```

```json
{
  "events": [
    {
      "id": 1,
      "deviceId": 7,
      "positionId": 4211,
      "type": "deviceOverspeed",
      "eventTime": "2026-10-31T07:32:10Z",
      "attributes": { "limit": 30, "protocol": "gt06", "speed": 31.5 }
    }
  ],
  "count": 1
}
```

`GET /api/events` 與 `GET /api/alerts` 打的是同一個查詢：`/api/events` 回
`{"events": [...], "count": n}`（欄位 `eventTime`），`/api/alerts` 是舊名字的別名，回
`{"alerts": [...], "count": n}`（欄位 `alertTime`），兩邊的列完全相同。

參數：`deviceId`、`groupId`（該群組與子群組的裝置）、`type`（可重複或逗號分隔）、
`from` / `to`（RFC3339 或 epoch 毫秒）、`limit`（1–1000，預設 100）、`offset`。
預設依 `event_time DESC, id DESC` 排序。`GET /api/events/{id}` 回單筆：不存在回 404，
事件所屬裝置碰不到回 403（和 `/api/devices/{id}` 一樣不洩漏 id 是否存在）。

已知取捨：

- **`events.position_id` 現在會填**。會產生事件的定位由 `alert.Pipeline` 組成一份 report
  （位置 + 事件），`Store.InsertReports` 在同一個交易裡用 `INSERT ... RETURNING id` 寫位置、
  再帶著那個 id 寫事件；沒有事件的定位照舊走 `BatchSink`，所以批次吞吐不受影響。
  代價是「有事件的那一筆」走的是同步交易（罕見），而且兩條路徑的寫入順序不保證。
- **去重與圍欄狀態都在記憶體**：重啟後去重視窗重新計時、圍欄只做「播種」。
- **門檻 / 圍欄變更最多延遲一個 refresh 週期**：`devices.attributes` 依 `-device-refresh`、
  `geofences` 依 `-geofence-refresh` 重新載入（兩者預設 5m）；設 `0` 表示只在啟動時載入一次。
  調整 SQL 後若要立刻生效，重啟程式即可。
- **圍欄門檻只影響超速**：斷電是裝置狀態，只有全域與 per-device 兩層。
- **去重視窗比的是伺服器時間**，不是裝置時間；設備一次補傳大量歷史資料時只會留下第一筆
  （這正是避免告警風暴的目的，但會看不到歷史區間內的每一筆）。
- 圍欄狀態存在記憶體，重啟後每個裝置的第一筆 position 只做「播種」，不會補發 enter / exit。

## 通知管道

告警除了寫進 `events` 表，還可以推到 webhook 與 email。兩個管道共用同一份格式化內容：
`alert.Pipeline` 每產生一筆告警就丟進 `notify.Dispatcher` 的佇列，傳送在背景 goroutine 進行，
device 連線與資料庫寫入都不等網路。沒有設定任何 `-notify-*` 時整條路徑不會啟動；
通知需要 `-dsn`（告警規則本身就要資料庫），記憶體模式只會印出「ignoring -notify-\* flags」。

路由分三層，依序決定：

1. `-notify-types` 先決定伺服器要不要送這個告警類型。
2. 裝置的 `notifyTypes` 再收窄；裝置的 `notifyEmail` / `notifyWebhook` 是該管道的**絕對覆寫**，
   設了就不會再加上任何使用者訂閱。
3. 沒有裝置覆寫時，若這台裝置的這筆告警有帳號訂閱，**訂閱者決定哪些管道要送**；
   完全沒有訂閱者時才用伺服器 flag 的預設收件者。

伺服器層級設定：

| Flag                                                | 說明                                                                                              |
| --------------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `-notify-webhook`                                   | webhook URL；空 = 關閉 webhook                                                                    |
| `-notify-webhook-authorization`                     | 每個請求帶的完整 `Authorization` 值；空 = 不帶                                                    |
| `-notify-email-smtp`                                | SMTP `host:port`；空 = 關閉 email                                                                 |
| `-notify-email-from`                                | 寄件者；`-notify-email-smtp` 有值時必填                                                           |
| `-notify-email-to`                                  | 預設收件者（逗號分隔）；空 = 只用裝置的 `notifyEmail`                                             |
| `-notify-email-username` / `-notify-email-password` | SMTP AUTH；密碼預設讀 `TRACCAR_SMTP_PASSWORD`，使用者名稱空 = 不做 AUTH                           |
| `-notify-email-tls`                                 | `auto`（預設：465 用 implicit TLS，其餘伺服器有 STARTTLS 就升級）、`starttls`、`implicit`、`none` |
| `-notify-types`                                     | 只送這些告警類型（逗號分隔）；空 = 全部；`none` = 全關                                            |
| `-notify-timeout`                                   | 單次裝置查詢 / 傳送的上限，預設 10s                                                               |
| `-notify-queue`                                     | 等待傳送的告警上限，預設 256；滿了就丟棄並計數                                                    |

每個裝置可以用 `devices.attributes` 覆寫（與門檻共用 `-device-refresh` 的更新週期）：

| Attribute       | 說明                                                                          |
| --------------- | ----------------------------------------------------------------------------- |
| `notifyTypes`   | 這個裝置要送的類型（逗號分隔或 JSON 陣列）；`none` = 全關，`all` / `*` = 全部 |
| `notifyWebhook` | 覆寫 webhook URL（可多個）；`false` 或空字串 = 這個裝置不送 webhook           |
| `notifyEmail`   | 覆寫收件者（可多個）；`false` 或空字串 = 這個裝置不寄 email                   |

`-notify-types` 先過濾，裝置的 `notifyTypes` 再收窄，所以裝置沒辦法打開全域關掉的類型。
`notifyTypes` 寫成空字串與 `none` 同義。

```sql
-- 這個裝置只送斷電 / 復電，webhook 走另一個 endpoint，email 關掉
UPDATE devices SET attributes = attributes || '{
  "notifyTypes": "powerCut,powerRestored",
  "notifyWebhook": "https://ops.example.com/hooks/traccar",
  "notifyEmail": false
}'::jsonb WHERE id = 7;
```

### 使用者訂閱

`notifications` 是公用範本（告警類型 + 管道），`user_notifications` 是帳號的訂閱。
帳號只會收到 `permissions` 直接授權的裝置的告警（群組授權目前不展開，見「已知限制」）；
`users.disabled = true` 的帳號不送。
同一個帳號有多個符合的範本時，管道取聯集，同一個收件者只送一次。

- `notifications.type`：`deviceOverspeed` / `powerCut` / `powerRestored` / `geofenceEnter` /
  `geofenceExit`，或 `all` / `*`（全部類型）。
- `notifications.notificators`：`mail`（寄到帳號的 `users.email`，`email` 是別名）與
  `webhook`（打 `-notify-webhook` 的 URL）。至少一個，未知值會被 API 拒絕。

`mail` 的收件者固定是帳號 email；`webhook` 沒有 per-user URL，訂閱只決定「這台裝置的這種
告警要不要打伺服器 webhook」。兩個管道仍要先用 `-notify-email-smtp` / `-notify-webhook`
啟用，否則沒有 notifier 可以送。

| 路由                                                    | 說明                                                            |
| ------------------------------------------------------- | --------------------------------------------------------------- |
| `GET /api/notifications`                                | 列出所有範本（任何已驗證帳號）                                  |
| `POST /api/notifications`                               | 建立範本（admin），body `{"type","notificators","description"}` |
| `PUT /api/notifications/{id}`                           | 局部更新範本（admin），省略的欄位不動                           |
| `DELETE /api/notifications/{id}`                        | 刪除範本（admin），`user_notifications` 由 FK cascade           |
| `GET /api/users/{id}/notifications`                     | 列出帳號的訂閱（本人或 admin）                                  |
| `PUT /api/users/{id}/notifications/{notificationId}`    | 訂閱（本人或 admin；重複呼叫不會出錯）                          |
| `DELETE /api/users/{id}/notifications/{notificationId}` | 取消訂閱（本人或 admin；原本沒訂閱也回 `204`）                  |

```bash
# admin 建立「超速寄 email」範本
curl -X POST localhost:8082/api/notifications -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' \
  -d '{"type":"deviceOverspeed","notificators":["mail"],"description":"overspeed mail"}'

# Bob(2) 用自己的 token 訂閱（發 token 見「帳號管理 API」）；admin 也可代為操作。
# 使用者 token 可以放在 X-Api-Key 或 Authorization: Bearer。
curl -X PUT localhost:8082/api/users/2/notifications/1 \
  -H "X-Api-Key: $TRACCAR_TOKEN"
```

等價的 SQL：

```sql
INSERT INTO notifications (type, notificators, description)
VALUES ('deviceOverspeed', ARRAY['mail'], 'overspeed mail');

INSERT INTO user_notifications (user_id, notification_id) VALUES (2, 1);
```

Webhook 的 body 固定為一份 JSON：

```json
{
  "type": "deviceOverspeed",
  "deviceId": 7,
  "uniqueId": "864717003283581",
  "deviceName": "van-7",
  "protocol": "gt06",
  "time": "2026-10-31T07:32:10Z",
  "subject": "[traccar] deviceOverspeed: van-7 (864717003283581)",
  "message": "Device: van-7 (864717003283581)\nType: deviceOverspeed\n...",
  "attributes": { "speed": 31.5, "limit": 30 }
}
```

只有 2xx 算成功；其餘狀態、連線錯誤或 SMTP 失敗都會寫進 log 並累計
`traccar_notification_errors_total`。Email 是 `text/plain; charset=utf-8`，body 以 base64
編碼、主旨依 RFC 2047 編碼，多個收件者共用同一次 SMTP 交易。

已知取捨：

- 使用者訂閱的 `mail` 收件者固定是 `users.email`；`webhook` 仍共用伺服器的
  `-notify-webhook` URL，沒有 per-user / per-notification URL。
- 裝置的 `notifyEmail` / `notifyWebhook` 是絕對覆寫：設了就不會再加上訂閱者。
- 有訂閱者時，該裝置 / 該告警類型的訂閱集合取代伺服器預設收件者；沒人訂閱的管道不送。
- 裝置查詢失敗（資料庫暫時不可用）時仍會送出，只是主旨 / body 只帶 device id；訂閱查詢失敗
  也一樣退回伺服器預設收件者，不會因為一次 DB hiccup 靜默丟掉告警。
- 佇列滿時直接丟棄，**不做重試**；`-notify-queue` 要留得下告警尖峰。
- `-notify-email-tls auto` 遇到沒有 STARTTLS 的伺服器會以明文繼續（與 `net/smtp.SendMail`
  相同）；正式環境請用 `starttls` 或 `implicit`。

## 認證與權限

除了 `/api/health`、`POST /api/login`、兩支密碼自助路由（`POST /api/password/forgot`、
`POST /api/password/reset`）與自己驗 `?token=` 的 `/api/socket`，REST 每個請求都要帶憑證，放在 `X-Api-Key` 或
`Authorization: Bearer <token>`（兩者等價）：

| 憑證         | 來源                                  | 權限                         |
| ------------ | ------------------------------------- | ---------------------------- |
| 共用 API key | `-api-key` / `TRACCAR_API_KEY`        | admin：所有 scope、所有裝置  |
| 使用者 token | `api_tokens` 表，用 `-issue-token` 發 | 依 `scopes` + `permissions`  |
| 密碼登入     | `POST /api/login`（email + 密碼）     | 換發一組帶 `*` 的新 token    |

密碼只以 bcrypt（cost 12）雜湊存在 `users.password_hash`，登入請求和任何 user JSON 都不會出現它。

憑證以常數時間比較；token 只存 SHA-256（`api_tokens.token_hash`），資料庫外洩也拿不到原值。
缺憑證或不符回 `401`（帶 `WWW-Authenticate: Bearer`），scope 不足或裝置未授權回 `403`。
接了 `-dsn` 之後 token 認證一律啟用：**沒設 `-api-key` 又沒有任何有效 token 時，REST 是全關的**
（只有 `/api/health`、`/api/login`、`/api/password/forgot` 與 `/api/password/reset` 例外）。舊的 `-api-key` 仍然可用，等於 admin。
本機開發若不想每次帶憑證，可以加 `-allow-anonymous`（啟動會印警告，不要用在對外環境）。

```bash
# 共用 API key
curl -H 'X-Api-Key: please-change-me' 'localhost:8082/api/positions'
# 使用者 token
curl -H "Authorization: Bearer $TOKEN" 'localhost:8082/api/alerts'
```

### Scope

| scope            | 端點                                                                     |
| ---------------- | ------------------------------------------------------------------------ |
| `read:positions` | `GET /api/positions`                                                     |
| `read:devices`   | `GET /api/devices`                                                       |
| `read:alerts`    | `GET /api/events`、`GET /api/alerts`（同一個查詢）                        |
| `read:metrics`   | `GET /metrics`                                                           |
| `read:commands`  | `GET /api/commands`                                                      |
| `write:devices`  | `POST/PUT/DELETE /api/devices`                                           |
| `write:commands` | `POST /api/commands`                                                     |
| `read:permissions` | `GET /api/permissions`                                                 |
| `write:permissions` | `POST` / `DELETE /api/permissions`                                    |
| `write:share`    | `POST /api/share/device`、`POST /api/share/group`                        |
| `read:groups` / `write:groups`               | `/api/groups`                                 |
| `read:geofences` / `write:geofences`         | `/api/geofences`                              |
| `read:calendars` / `write:calendars`         | `/api/calendars`                              |
| `read:drivers` / `write:drivers`             | `/api/drivers`                                |
| `read:maintenances` / `write:maintenances`   | `/api/maintenance`、`/api/maintenances`        |
| `read:orders` / `write:orders`               | `/api/orders`                                 |
| `read:reports` / `write:reports`             | `/api/reports`                                |
| `read:attributes` / `write:attributes`       | `/api/attributes/computed`                    |
| `*`              | 全部（給機器用，等同舊 API key），另外是列出 / 撤銷自己 token 的必要條件 |

`*` 不等於 admin：帳號管理（建 / 改 / 刪帳號、發 token）仍然只認 `users.admin = true`，
所以帶 `*` 的 token 不會提權，只能管自己的 token。

### 使用者與裝置擁有權

`users.admin = true` 的使用者不受 scope 與裝置限制；其他使用者只看得到 `user_devices`
有授權的裝置。所有查詢（devices / positions / events）都會自動加上裝置條件，**沒有授權任何裝置
就回空結果，而不是全部**。修改 / 刪除裝置與下命令也要擁有該裝置，否則 `403`。
`users.disabled = true` 的帳號會立刻失效（token 查詢直接回 `401`），但 token 與授權都保留，
之後重新啟用就照舊可用。

非 admin token 用 `POST /api/devices` 建檔時，會自動寫入一筆 `user → device` 授權，
建立者立刻看得到；admin key 建的裝置不會自動綁定，要另外授權（見下節）。

授權可以下在裝置上，也可以下在群組上：

- `user → device`：直接看得到這一台。
- `user → group`：看得到該群組、它的子群組、以及再下一層的子群組內的裝置
  （`MAX_GROUP_DEPTH = 3`，群組樹最多三層：根、子、孫；母本同名常數）。
- `group → 任何資源`、`device → 任何資源`：模型與儲存層都支援，REST 端點跟著 M2-3 的資源
  CRUD 一起開。

token 認證時會把群組授權展開成裝置清單（每個請求一次），所以後面所有查詢的裝置條件跟
直接授權完全一樣。群組授權目前只影響「看得到哪些裝置」，不影響通知收件者（見「已知限制」）。

## 權限 API

`/api/permissions` 是母本 `PermissionsResource` 的對應端點：一筆權限就是一個兩個鍵的物件，
**第一個鍵是 owner、第二個是 property**，跟母本一樣照 body 的順序解讀：

```json
{"userId": 2, "deviceId": 7}
{"groupId": 1, "geofenceId": 5}
```

| 路由                     | 說明                                                                        |
| ------------------------ | --------------------------------------------------------------------------- |
| `GET /api/permissions`   | 查詢，形如 `?userId=2&deviceId=7`；其中一個可以省略表示不限定。回傳**裸陣列**（母本格式，`traccar-web` 直接吃） |
| `POST /api/permissions`  | 新增一筆或一批（陣列），全部同型別；成功回 `204`，重複新增不會出錯           |
| `DELETE /api/permissions`| 刪除一筆或一批；成功回 `204`，原本沒有也回 `204`                              |

- 需要 scope `read:permissions` / `write:permissions`。
- admin 可以查、改任何配對。非 admin 只能碰自己碰得到的兩端（自己 → 自己的裝置），
  否則 `403`；`?userId=<別人的 id>` 這種不限定查詢也是 admin 專屬，避免列舉別人的授權。
- 兩端 id 必須是正整數，型別名稱必須是已知物件（`user` / `device` / `group` / `geofence` /
  `driver` / `calendar` / `maintenance` / `order` / `report` / `notification` / `command` /
  `attribute`），owner 只能是 `user` / `group` / `device`。不合法一律 `400`。

```bash
# 把 device 7 授權給 Bob(2)
curl -X POST -H "Authorization: Bearer $ADMIN" -d '{"userId":2,"deviceId":7}' localhost:8082/api/permissions
# Bob 現在拿得到哪些裝置
curl -H "Authorization: Bearer $ADMIN" 'localhost:8082/api/permissions?userId=2&deviceId=7'
# 授權整個群組（含子群組，最多三層）
curl -X POST -H "Authorization: Bearer $ADMIN" -d '{"userId":2,"groupId":1}' localhost:8082/api/permissions
```

### 分享連結

`POST /api/share/device` 與 `POST /api/share/group`（form-urlencoded）替一台裝置或一個群組
開一個**唯讀**連結：伺服器建一個沒有密碼的臨時帳號（email 是 `<你的 email>:<uniqueId>` 或
`<你的 email>:group:<id>`）、把裝置 / 群組授權給它，再發一組只有 `read:positions` /
`read:devices` / `read:alerts` 的 token 回給你。這是母本 `ShareResource` 的行為。

```bash
curl -X POST -H "Authorization: Bearer $TOKEN"      -d 'deviceId=7&expiration=2026-12-31T00:00:00Z' localhost:8082/api/share/device
# {"token":"trc_...","userId":9,"expiresAt":"2026-12-31T00:00:00Z"}
```

- 需要 scope `write:share`，而且呼叫者要碰得到該裝置 / 群組，否則 `403`。
- `expiration` 省略表示不過期；格式是 RFC3339 或 `YYYY-MM-DD`（當天 00:00 UTC），必須在未來。
- 同一個裝置 / 群組只建一次帳號，第二次回 `409`（母本行為）。要重新發一組 token 請用
  `POST /api/users/{分享帳號}/tokens`。
- 分享帳號是 `temporary`，沒有密碼、不能登入，也不能再拿去分享（`403`）。
- `-disable-share`（設定檔 key `disableShare`）關掉這兩支路由，回 `403`。

### 帳號管理 API

帳號相關路由全部需要 **admin 憑證**（`-api-key`，或 `users.admin = true` 的使用者 token），
不看 scope：即使 token 帶 `*` 也不能建 / 改 / 刪帳號或發 token，避免提權。沒有配置 `-dsn` 時這些
路由回 `501`；`-allow-anonymous` 時也會一併開放，只適合本機。
三處例外，都只碰「自己的」資料：
token 的列出與撤銷對帳號本人開放，但本人手上那組 token 必須帶 `*`（見「密碼登入」）；
通知訂閱的 `/api/users/{id}/notifications/...` 對帳號本人開放，不看 scope（見「使用者訂閱」）；
`PUT /api/users/me/password` 由帳號本人改自己的密碼（見「密碼登入」）。

| 路由                                                    | 說明                                                                                    |
| ------------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `GET /api/users`                                        | 列出帳號（`limit` 1–1000、`offset`），含 `deviceCount` / `tokenCount`                   |
| `POST /api/users`                                       | 建立帳號，body `{"email","name","admin"}`；email 重複回 `409`                           |
| `PUT /api/users/{id}`                                   | 局部更新 `name` / `admin` / `disabled` / `password`，省略的欄位不動                     |
| `DELETE /api/users/{id}`                                | 刪除帳號（token cascade；授權由儲存層一併清掉）                                          |
| `GET /api/users/{id}/devices`                           | 列出已授權的裝置（格式同 `GET /api/devices`）                                           |
| `PUT /api/users/{id}/devices/{deviceId}`                | 授權裝置（重複呼叫不會出錯）                                                            |
| `DELETE /api/users/{id}/devices/{deviceId}`             | 撤銷授權（原本就沒有也回 `204`）                                                        |
| `GET /api/users/{id}/tokens`                            | 列出 token 中繼資料，**不含**明文與雜湊（admin，或本人帶 `*` 的 token）                 |
| `POST /api/users/{id}/tokens`                           | 發 token，body `{"scopes":[...],"expiresIn":"720h"}`，明文只回這一次（僅 admin）        |
| `DELETE /api/users/{id}/tokens/{tokenId}`               | 撤銷 token（admin，或本人帶 `*` 的 token）                                              |
| `PUT /api/users/me/password`                            | 本人改密碼，body `{"currentPassword","newPassword"}`（需要使用者 token，共用 key 不行） |
| `POST /api/password/forgot`                             | 寄出重設碼，body `{"email"}`；不論帳號是否存在都回 `202`（免憑證）                      |
| `POST /api/password/reset`                              | 用重設碼換新密碼，body `{"token","password"}`；用過即失效（免憑證）                     |
| `GET /api/users/{id}/notifications`                     | 列出通知訂閱（本人或 admin）                                                            |
| `PUT /api/users/{id}/notifications/{notificationId}`    | 訂閱通知範本（本人或 admin）                                                            |
| `DELETE /api/users/{id}/notifications/{notificationId}` | 取消訂閱（本人或 admin）                                                                |

`expiresIn` 是 Go duration（`720h`、`30m`），空字串表示永不過期；scopes 依上表驗證，
未知 scope 回 `400`。停用自己、撤掉自己的 admin、刪除自己都回 `409`，避免把唯一的
管理員鎖在門外。

### 密碼登入

`POST /api/login` 收 `{"email","password"}`，換一組新的 token（回應格式同發 token，
`token` 只在這個回應出現一次）。除了 `/api/health` 與兩支密碼自助路由，其他路由都要帶憑證；
登入是拿到第一組憑證的入口。

- 密碼由 admin 用 `PUT /api/users/{id}` 的 `password` 欄位設定。帳號本人**不能**替沒有密碼的帳號
  建立第一組密碼，否則任何一支被偷的窄 scope token 都能把自己升級成密碼登入；第一組密碼只能由
  admin 設定，或走忘記密碼流程（擁有信箱就等於擁有帳號）。admin 設密碼會同時解除登入鎖定並作廢
  未使用的重設碼，所以那也是帳號被鎖住時的救援路徑。
- 本人改密碼用 `PUT /api/users/me/password`，要帶使用者 token 與目前的密碼；成功後其他 token
  全部撤銷（這次請求用的那一支保留）、登入失敗次數歸零、未使用的重設碼作廢。共用 `-api-key`
  沒有對應帳號，回 `403`。
- 忘記密碼：`POST /api/password/forgot` 寄出一次性的 `prt_` 重設碼到帳號 email，30 分鐘內有效，
  用 `POST /api/password/reset` 換新密碼。回應一律是 `202`，查無帳號、帳號停用、同一分鐘內重複
  要求都不會改變回應或延遲太多，不能用來探測帳號。重設成功會撤銷該帳號所有 token。
- 重設碼只存 SHA-256，且只能用一次；同一帳號一分鐘內只寄一封。email 走既有的 SMTP 設定
  （`-notify-email-smtp` / `-notify-email-from` 等）；沒有設 SMTP 時 `forgot` 回 `501`，
  其他密碼功能不受影響。
- 連續 5 次密碼錯誤會鎖帳號 15 分鐘，鎖住期間連正確密碼也回 `429`（帶 `Retry-After` 秒數）。
  可用 `-login-max-failures`（0 關閉）與 `-login-lock-duration` 調整。登入成功會把次數歸零，
  鎖到期後從零開始，不會一次失敗就再鎖。
- 沒設密碼（`password_hash` 是 NULL）和密碼打錯都回 `401`，連花的時間也一樣（沒設密碼仍跑一次 bcrypt），
  所以回應碼與延遲都不會洩漏哪些帳號存在、哪些還沒有密碼。
- 停用帳號在密碼正確時回 `403`；密碼打錯時仍是 `401`，狀態碼不能用來探測哪些帳號存在。
- email 要與 `users.email` **完全相同**（大小寫敏感）。查無此帳號時仍會跑一次 bcrypt，
  讓回應時間不洩漏帳號是否存在。
- 換發的 token 帶 `*`、不過期，可以直接列出與撤銷同帳號的其他 token；發新 token 仍然只有
  admin 能做，所以帶 `*` 的 token 提不了權。
- 密碼長度上限 72 bytes（bcrypt 的輸入上限），空字串與超長都在 `PUT` 回 `400`，不會靜默截短。
- 密碼以 bcrypt cost 12 儲存，一次登入就是一次 bcrypt：本機 i5-4200U 量到 p50 594 ms、
  p95 740 ms。登入、admin 設密碼的 `PUT` 與本人改密碼（驗舊密碼再雜湊新密碼，共兩次 bcrypt）
  都要當成慢端點看待，前面不要放太短的 timeout。

```bash
# 設定密碼（admin）
curl -X PUT localhost:8082/api/users/2 -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' -d '{"password":"s3cret"}'

# 用密碼換 token
curl -X POST localhost:8082/api/login \
  -H 'Content-Type: application/json' -d '{"email":"bob@example.com","password":"s3cret"}'
# {"id":7,"userId":2,"scopes":["*"],"createdAt":"2026-11-01T...","token":"trc_..."}

# 之後就能拿它列出 / 撤銷自己的 token
curl -H "Authorization: Bearer $TOKEN" localhost:8082/api/users/2/tokens
```

下面這些密碼自助動作都用一般使用者 token（或登入換到的 token）：

```bash
TOKEN='trc_...'

# 本人改密碼：要帶目前的密碼；成功後其他 token 全部失效
curl -X PUT localhost:8082/api/users/me/password \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"currentPassword":"s3cret","newPassword":"n3w-s3cret"}'

# 忘記密碼：寄出 30 分鐘有效的 prt_ 重設碼（一律 202）
curl -X POST localhost:8082/api/password/forgot \
  -H 'Content-Type: application/json' -d '{"email":"bob@example.com"}'
# {"status":"if the address has an account, a reset code has been sent"}

# 用信裡的重設碼換新密碼；成功後該帳號所有 token 失效
curl -X POST localhost:8082/api/password/reset \
  -H 'Content-Type: application/json' \
  -d '{"token":"prt_...","password":"n3w-s3cret"}'
```

第一次使用（資料庫還沒有任何帳號）先用共用 key 引導：

```bash
# 1) 建立管理員
curl -X POST localhost:8082/api/users -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' \
  -d '{"email":"alice@example.com","name":"Alice","admin":true}'

# 2) 替管理員發 token（明文只印一次）
curl -X POST localhost:8082/api/users/1/tokens -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' -d '{"scopes":["*"]}'

# 3) 業務帳號 + 授權裝置（deviceId 7）
curl -X POST localhost:8082/api/users -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' -d '{"email":"bob@example.com","name":"Bob"}'
curl -X PUT localhost:8082/api/users/2/devices/7 -H 'X-Api-Key: please-change-me'

# 4) 給業務帳號一個密碼，之後他自己換 token（`*` 讓它能管理自己的 token）
curl -X PUT localhost:8082/api/users/2 -H 'X-Api-Key: please-change-me' \
  -H 'Content-Type: application/json' -d '{"password":"s3cret"}'
curl -X POST localhost:8082/api/login -H 'Content-Type: application/json' \
  -d '{"email":"bob@example.com","password":"s3cret"}'
```

之後就能用 Alice 的 token 取代 `-api-key`。沒有 HTTP 存取的環境仍可從 CLI 發 token：

```bash
go run ./cmd/traccar-gateway -dsn "$DSN" \
  -issue-token alice@example.com \
  -token-scopes read:positions,read:devices,read:alerts \
  -token-ttl 720h
# trc_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
```

`-token-ttl 0`（預設）表示永不過期。撤銷走 `DELETE /api/users/{id}/tokens/{tokenId}`，
或直接 `DELETE FROM api_tokens WHERE id = ...`，下一個請求就失效
（每個請求查一次 `api_tokens` + `user_devices`，沒有快取）。

下面 REST 範例省略了這個 header。

## 裝置管理

`GET /api/devices` 讀 `devices` 表，預設依 `last_update DESC NULLS LAST, id DESC`：

```bash
curl 'localhost:8082/api/devices?protocol=gt06&limit=20'
```

```json
{
  "devices": [
    {
      "id": 7,
      "uniqueId": "864717003283581",
      "name": "",
      "protocol": "gt06",
      "attributes": {},
      "lastUpdate": "2026-10-31T07:32:10Z",
      "createdAt": "2026-10-31T06:00:00Z"
    }
  ],
  "count": 1
}
```

參數：`protocol`（精確比對）、`uniqueId`（可重複，精確比對）、`id`（可重複，精確比對；
與 `uniqueId` 一起出現時是 OR）、`keyword`（對 `name` / `unique_id` / `model` 做 `ILIKE`）、
`excludeAttributes`（不回 `attributes` 欄位）、`limit`（1–1000，預設 100）、`offset`。
`uniqueId` / `id` 同時給了就忽略 `keyword`，和母本 `DeviceResource` 的分支一致。

`GET /api/devices/{id}` 回單筆；不存在回 404，token 碰不到該裝置回 403。
`all` / `userId` 也接受：`userId` 讓 admin 列出另一個帳號**直接**授權的裝置（不展開群組），
非 admin 只能填自己。舊的共用 API key 與未認證模式沒有帳號，這兩個參數不改變結果。
`lastUpdate` 只在裝置至少上報過一筆位置時出現（不會是 `0001-01-01`），`model` 為空時同樣省略；
`lastUpdate` 取的是**裝置時間**的最大值。

寫入端點（用來預先建檔或改名，不必等裝置上線）：

```bash
# 新增（uniqueId 必填；protocol 建議一併給，裝置登入後不會被覆寫）
curl -i -X POST localhost:8082/api/devices \
  -H 'Content-Type: application/json' \
  -d '{"uniqueId":"864717003283581","name":"van-7","model":"GT06N","protocol":"gt06"}'
# 201 + 裝置 JSON；uniqueId 重複回 409

# 修改：name / model 是整欄取代，省略等於清空
curl -i -X PUT localhost:8082/api/devices/7 \
  -H 'Content-Type: application/json' \
  -d '{"name":"van-7","model":"GT06N"}'

# 刪除：positions / events 由 FK CASCADE 一併刪除
curl -i -X DELETE localhost:8082/api/devices/7
# 204；不存在回 404
```

非 admin token 需要 `write:devices`，而且只能修改 / 刪除 `user_devices` 內的裝置，否則回 403；
`POST /api/devices` 建檔後會自動把新裝置綁給該 token 的使用者。

`uniqueId`、`name`、`model`、`protocol` 兩端都會 trim；空白的 `uniqueId` 回 400。
id 不是正整數回 400。`PUT` 現在可以寫 `attributes`（超速 / 斷電門檻、`polylineDistance` 等）：

```bash
# 只改名，attributes 保持原樣
curl -i -X PUT localhost:8082/api/devices/7 -d '{"name":"van-7","model":"GT06N"}'
# 連 attributes 一起整欄取代
curl -i -X PUT localhost:8082/api/devices/7 \
  -H 'Content-Type: application/json' \
  -d '{"name":"van-7","model":"GT06N","attributes":{"speedLimit":45,"floor":0}}'
```

`name` / `model` 是整欄取代，省略等於清空；`attributes` 省略表示**保留原值**，給了就整欄取代
（給 `{}` 就是清空），所以只改名的請求不會把門檻洗掉。`protocol` 建立後仍不可改。

## 資源管理 API

M2-3 把母本 `SimpleObjectResource` / `ExtendedObjectResource` 的設定型資源補齊，全部共用
`internal/api/resources.go` 的一套 handler：

| 路徑                        | 資源                      | 寫入時的額外檢查             |
| --------------------------- | ------------------------- | ---------------------------- |
| `/api/groups`               | 群組（最多三層）          | 父群組要碰得到、樹不能超過三層 |
| `/api/geofences`            | 圍欄（Traccar WKT）       | `calendarId` 要碰得到          |
| `/api/calendars`            | iCalendar 原文            |                              |
| `/api/drivers`              | 駕駛                      |                              |
| `/api/maintenance`（`/api/maintenances` 為別名） | 保養週期 |                          |
| `/api/orders`               | 訂單                      |                              |
| `/api/reports`              | 報表排程                  | `calendarId` 要碰得到          |
| `/api/attributes/computed`  | 計算屬性                  | 只有 admin 能寫                |

每個路徑都是 `GET`（清單）、`GET /{id}`、`POST`、`PUT /{id}`、`DELETE /{id}`。
清單回**裸陣列**（traccar-web 直接讀），`POST` 回 201、`PUT` 回 200、`DELETE` 回 204；
不存在回 404，碰不到的一律回 403（不洩漏 id 存不存在）。

參數照母本：

| 參數                | 語義                                                                 |
| ------------------- | -------------------------------------------------------------------- |
| `all`               | 只有 admin 有效：`true` 回全部；省略時回呼叫者自己的授權              |
| `userId`            | 指定帳號的授權；非 admin 只能填自己，否則 403（`all=true` 時忽略，與母本相同） |
| `groupId` / `deviceId` | 只看授權給該群組 / 裝置的資源；呼叫者碰不到該群組 / 裝置回 403    |
| `excludeAttributes` | 不回 `attributes` 欄位                                               |
| `keyword`           | 對該表的文字欄位做 `ILIKE`                                           |
| `limit` / `offset`  | 分頁；storage 夾在 1–1000 與 `>= 0`，預設 100                        |

授權展開跟母本 `Condition.Permission` 一致：裝置與**群組本身**會從授權的群組往子群組展開
（最多三層），其餘資源（圍欄、日曆、駕駛、…）只認 `user → 資源` 的直接授權。
非 admin 的 token 寫入成功後會自動拿到 `user → 新資源`（母本 `BaseObjectResource.add`）；
舊的共用 API key 不是帳號，所以不寫授權，也看得到全部。

```bash
# 建一個圍欄：樓層限制放在 attributes，不用再碰 SQL
curl -i -X POST localhost:8082/api/geofences \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"depot","description":"main yard","area":"CIRCLE (51.5 0.0, 100)","attributes":{"floor":10,"ceiling":120}}'
# 201 {"id":5,"calendarId":0,"name":"depot","description":"main yard","area":"CIRCLE (51.5 0.0, 100.0)","attributes":{"floor":10,"ceiling":120}}

# 清單是裸陣列；excludeAttributes 讓列表不要帶 attributes
curl 'localhost:8082/api/geofences?all=true&keyword=depot&excludeAttributes=true'

# 群組：父群組要碰得到，整棵樹最多三層
curl -i -X POST localhost:8082/api/groups \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"North","groupId":2}'
# 太深或父群組不存在回 400
```

`area` 只接受 `POLYGON` / `CIRCLE` 的 Traccar WKT，寫入時就 parse，壞掉回 400；寫進去的
`floor` / `ceiling` 直接由圍欄引擎使用。`calendars.data` 沿用母本 `byte[]` 的 JSON 表示，
所以是 base64 字串。`PUT` 是整欄取代：省略的欄位會變成零值 / 空 `attributes`，和母本的
update 相同。

## 位置查詢

`GET /api/positions` 讀 `positions` 表，`from` / `to` 比對的是**裝置時間**（`device_time`），
排序固定 `device_time DESC, id DESC`：

```bash
curl 'localhost:8082/api/positions?deviceId=7&limit=2'
```

```json
{
  "positions": [
    {
      "id": 42,
      "deviceId": 7,
      "protocol": "gt06",
      "deviceTime": "2026-10-31T07:32:10Z",
      "serverTime": "2026-10-31T07:32:11Z",
      "valid": true,
      "latitude": 54.943,
      "longitude": 25.206,
      "altitude": 100,
      "speed": 27,
      "course": 180,
      "accuracy": 5,
      "attributes": { "sat": 9 }
    }
  ],
  "count": 1
}
```

參數：`deviceId`、`geofenceId`（只回圍欄內的定位，`floor` / `ceiling` 一併生效）、
`from` / `to`（RFC3339 或 epoch 毫秒）、`limit`（1–1000，預設 100，超過會回 400）、`offset`。
`attributes` 永遠是物件（沒有額外欄位時是 `{}`，不是 `null`）。`geofenceId` 指到不存在的圍欄回
404，沒有配置圍欄儲存層回 501（母本會默默忽略不存在的圍欄，這裡選擇明講）。

匯出（都需要 `read:positions`，只能匯出自己碰得到的裝置；`deviceId` 必填）：

| 端點                     | Content-Type                           | 內容                             |
| ------------------------ | -------------------------------------- | -------------------------------- |
| `GET /api/positions/kml` | `application/vnd.google-earth.kml+xml` | 一條 `LineString` 軌跡           |
| `GET /api/positions/kmz` | `application/vnd.google-earth.kmz`     | 同上的 KML 壓成 `doc.kml`        |
| `GET /api/positions/csv` | `text/csv`                             | 固定欄位 + 每個 attribute 一欄   |
| `GET /api/positions/gpx` | `application/gpx+xml`                  | `trkpt` 軌跡                     |

```bash
curl -OJ 'localhost:8082/api/positions/csv?deviceId=7&from=2026-11-01T00:00:00Z&to=2026-11-01T23:59:59Z'
curl -o track.kmz 'localhost:8082/api/positions/kmz?deviceId=7&geofenceId=3&from=2026-11-01T00:00:00Z'
```

軌跡依 `device_time` **由舊到新**，最多 50000 筆（母本 `report.maxPositions` 的預設值）；
storage 單次查詢上限 1000 筆，所以匯出用 offset 分頁讀完整個區間。
CSV 欄位順序是 `id,deviceId,protocol,serverTime,deviceTime,valid,latitude,longitude,altitude,
speed,course,accuracy`，後面接區間內出現過的 attribute（依名稱排序）；開頭是 `= + - @` 的文字
會留空，避免試算表當成公式執行（母本 `CsvExportProvider` 的做法）。時間一律 UTC。

## 即時推播（WebSocket）

`GET /api/socket?token=<token>` 是母本 `AsyncSocket` 的對應實作：瀏覽器建立一條 WebSocket，
之後收到位置、事件與裝置狀態的增量。**憑證走 query string**（瀏覽器不能對 WebSocket 設 header），
所以這條路徑不受 `RequireAuth` 管，改由 handler 自己用 `-api-key` 或 `api_tokens` 驗，驗證語義
與 header 版相同。

```js
const socket = new WebSocket(`ws://localhost:8082/api/socket?token=${token}`);
socket.onmessage = (event) => {
  const data = JSON.parse(event.data);
  // 每次只有一個鍵：data.positions / data.events / data.devices
};
```

連上後第一則固定是 `{"positions":[...]}`（該帳號看得到的每台裝置的最新一筆，`DISTINCT ON
(device_id)`），之後每次變更送一則，形狀與 REST 相同：

| 訊息        | 何時                                               |
| ----------- | -------------------------------------------------- |
| `positions` | 每筆處理過的定位（`alert.Pipeline` 的位置 hook）    |
| `events`    | 規則產生的每一個事件（含寫入佇列漏掉的）            |
| `devices`   | 裝置上線 / 離線（`session.Manager` 的狀態轉換）     |

每 30 秒送一次 `{}` 當 keepalive（母本 `onKeepalive` 的行為）。用戶端送 `{"logs":true}` 會被
接受但忽略：這裡沒有 log 串流。訊息依 token 的 scope 分流——`read:positions` / `read:alerts` /
`read:devices` 分別決定收不收得到該類訊息，三者都沒有回 403；裝置範圍沿用 token 的授權
（含群組展開），admin 收全部。

推播是非阻塞的：每個連線有 64 則的佇列，跟不上就丟掉新的那則，不讓裝置的讀取迴圈等瀏覽器；
沒有連線時 publisher 連 JSON 都不編。連線本身是 hijack 出來的，`http.Server.Shutdown` 不會關它，
行程結束時才一起收掉。

## 命令下發

`POST /api/commands` 把命令編碼成該協議的位元組，先落一列 `commands`，再交給派送器寫進裝置目前
那條 TCP 連線（需要 `-dsn`，因為要靠 `devices.protocol` 選 encoder 與寫紀錄）。裝置不在線上時
命令留在佇列裡，等它重連再送出：

```bash
curl -i -X POST localhost:8082/api/commands \
  -H 'Content-Type: application/json' \
  -d '{"deviceId":7,"type":"engineStop","attributes":{}}'

# HTTP/1.1 202 Accepted
# {"id":41,"deviceId":7,"type":"engineStop","protocol":"gt06","queued":false}
```

`queued` 是這筆命令當下有沒有等到裝置：`false` 表示位元組已寫進連線，`true` 表示還在佇列裡，
裝置重連後會送出。

| 狀態  | 意義                                                                    |
| ----- | ----------------------------------------------------------------------- |
| `202` | 已記錄，且已寫入連線（`queued:false`）或已排入派送佇列（`queued:true`） |
| `400` | body 不合法，或該協議不支援這個命令（都不會留下 `commands` 列）         |
| `403` | 缺少 `write:commands`，或該裝置不在 `user_devices` 內                   |
| `404` | 裝置不存在                                                              |
| `409` | `devices.protocol` 還是 NULL（讓設備重連一次），或沒有可用的協議伺服器  |
| `429` | 該裝置的派送佇列已滿（`-command-queue`），這筆直接記成 `failed`         |
| `501` | 沒有配置命令路由                                                        |

各協議支援的命令以 encoder 為準：H02 `alarmArm` / `alarmDisarm` / `engineStop` / `engineResume` /
`positionPeriodic`；GT06 `engineStop` / `engineResume` / `custom`；JT808 `positionSingle` /
`positionStop` / `rebootDevice` / `positionPeriodic` / `alarmArm` / `alarmDisarm` / `alarmDismiss` /
`engineStop` / `engineResume` / `custom`；Teltonika `engineStop` / `engineResume` / `custom`。
`custom` 的內容放 `attributes.data`（JT808 需要十六進位字串）。

寫入是序列化的：解碼執行緒的 ACK 與 REST 的命令共用同一把連線鎖，不會把兩個幀交錯。
同一裝置重新登入時舊連線會被關閉，命令只送到最新那條。

裝置與連線的綁定時機：GT06 / JT808 / Teltonika 在握手（登入 / IMEI 封包）時綁定；
H02 沒有握手，第一筆能解析出裝置的訊息就會綁定。因此對「從未上報過」或「尚未登入」的裝置
下命令會排在派送佇列裡，第一次握手成功後送出。

### 命令狀態

每一筆命令在 `commands` 表裡走四種狀態之一：

| status    | 何時                                             | `result_code`                       |
| --------- | ------------------------------------------------ | ----------------------------------- |
| `pending` | 已記錄，還沒寫進連線（在派送佇列裡等裝置）       | NULL                                |
| `sent`    | 已寫進連線，等裝置回應                           | NULL                                |
| `done`    | 裝置回應且結果是 0（GT06 只要有回應就算）        | 0                                   |
| `failed`  | 佇列已滿、排隊逾時、重送次數用完，或裝置回報非 0 | 沒送到是 NULL；裝置拒絕時是它回的值 |

`attempts` 這欄是**位元組實際寫進連線的次數**：1 表示只送過一次，大於 1 表示重送過。

`GET /api/commands` 列出命令，參數是 `deviceId`、`limit`（1–1000，預設 100）與 `offset`，
排序是 `id DESC`。需要 `read:commands`；非 admin 只看得到自己授權裝置的命令：

```bash
curl -s 'localhost:8082/api/commands?deviceId=7&limit=2' -H 'Authorization: ******'
# {"commands":[{"id":41,"deviceId":7,"type":"engineStop","attributes":{},"status":"done",
#               "resultCode":0,"serial":7,"attempts":1,
#               "createdAt":"2026-11-01T01:02:03.123Z",
#               "sentAt":"2026-11-01T01:02:03.130Z","doneAt":"2026-11-01T01:02:05.402Z"}],
#  "count":1}
```

`resultCode` / `sentAt` / `doneAt` / `serial` 沒有值時不會出現。用
`psql -c 'select * from commands'` 看到的內容與這個端點一致。

### 派送佇列與重送

派送器（`internal/gateway/dispatch.go`）是命令唯一的狀態寫入者，規則有三條：

- **寫不進去就排隊。** 裝置沒有連線時命令留在佇列（狀態 `pending`），裝置一握手就照 `id` 順序
  送出。佇列是每台裝置一份，一台裝置離線不會擋住別台；同時最多 `-command-queue` 筆，
  滿了回 `429` 並把該筆記成 `failed`。
- **等不到回應就重送。** 寫進連線後等 `-command-retry`，沒有回應就再寫一次同一份位元組，
  最多寫 `-command-attempts` 次，用完記成 `failed`。重送只重寫同一份 payload，不重新編碼，
  所以 JT808 的流水號在重送之間不變。
- **排隊有期限。** 從沒送出去的命令最多等 `-command-queue-ttl`，逾時記成 `failed` 並離開佇列。
  已經送出的命令只受重送次數限制，因為回應隨時可能到。

gateway 重啟時記憶體佇列是空的，但**裝置重連**會把 `pending` 的列重新編碼再送一次。代價是
`MarkCommandSent` 寫失敗時那列會停在 `pending`，重啟後可能重送一次，裝置會看到重複的命令。
這是刻意的取捨：寧可重複，不要遺失。

### 回應配對

JT808 終端用 `0x0001` 終端一般回應回答下行訊息，payload 是「被回應的流水號、被回應的訊息 ID、
結果碼」；GT06 用 `0x80` / `0x81` / `0x82` 命令回應回答，沒有結果碼，有回應就算成功。這兩個
回應都不會被回覆，否則裝置會收到「回應的回應」。

JT808 的 encoder 給**每一筆命令一個流水號**（`codec.SerialSource`，1–0xffff），號碼記在
`commands.serial`，終端回應裡的流水號就對回那一列，多筆在等回應時不會張冠李戴。重新編碼同一筆
命令（例如重連後從 `pending` 復原）沿用原本的號碼，所以那一列仍然對得上。

GT06 / H02 / Teltonika 的回應沒有等價欄位，`serial` 是 NULL，gateway 收到回應時把**該裝置最新
一列 `sent`** 收成 `done` 或 `failed`。連續送出時同一個裝置可能有好幾列在等回應，一次回應只
收掉最新那列；若前一筆一直沒回應，後送的那筆被回應時，被蓋掉的是後送的那列。

### 命令保留期限

`commands` 會長大，所以 gateway 每小時清一次：`done` / `failed` 的列以 `done_at` 為準，
從未結束的列以 `created_at` 為準，超過 `-command-retention`（預設 168 小時 = 7 天）就刪掉。
`-command-retention 0` 關閉清理，歷史永久保留。清理是一次 `DELETE`，刪掉幾列才寫一行 log：

```text
commands: purged 42 rows older than 168h0m0s
```

## 開發

```bash
go build ./...
go vet ./...
go test ./...                 # 全部（含 gateway 的 net.Pipe 端到端）
go test ./internal/protocol/gt06 -run TestDecodeGPS -v
go test ./internal/auth ./internal/geofence ./internal/alert ./internal/api ./internal/notify -v
go test -race ./internal/gateway ./internal/alert ./internal/api ./internal/notify
```

- 加測試向量：把 Java `*ProtocolDecoderTest` 的 `binary("...")` 貼進來，
  GT06 / Teltonika / JT808 的測試檔都有 `mustHex`；H02 用 `Hex(t, "...")`。
- 每個協議都有「CRC/XOR 對真機幀」的測試（`TestChecksum`），改動 helper 時會立刻反映。
- gateway 測試用 `net.Pipe()`，不需要開 port、不需要外部服務。
  `startConn` 會在測試結束時 join `ServeConn` goroutine，log 則走 `newTestLogger` 的緩衝區再輸出；
  不要把 `t.Logf` 直接傳給 `WithLogger`（背景 goroutine 可能在測試結束後才寫 log 而 panic）。
  圍欄與告警的測試是純函式（`POLYGON` 內含、跨反子午線、規則門檻、佇列滿載），一樣不需要資料庫。
- 告警門檻請用真機向量的數值：速度是**節**（GT06 `0x10` 的 50 km/h → 27 節）、
  電壓是**伏特**（GT06 `0x94` 的 12.5 V）。單位寫錯不會報錯，只是規則永遠不觸發。

### Live lanes（device-sim）

`cmd/device-sim` 把錄下來的裝置幀回放到正在跑的網關，live 驗證不需要真機。

```bash
go run ./cmd/traccar-gateway -dsn "$DSN" -api-key test-admin &
go run ./cmd/device-sim -addr 127.0.0.1:5023 -scene gt06          # 內建場景
go run ./cmd/device-sim -addr 127.0.0.1:5023 -scene ./my-scene.json  # 自帶檔案
```

場景檔在 `internal/devicescene/scenes/`，用 `go:embed` 打包，所以 `-scene` 可以直接吃場景名稱。
每個檔案帶 `protocol`、`uniqueId`、`handshake`、`positions`。`gateway` 套件的
`TestScenesReachTheSink` 會把每個場景經 `net.Pipe` 灌進真的 `gateway.Server`，斷言 sink 收到的
position 數；`handshake` 為 false 的場景（H02）不需要握手，裝置要先用 `uniqueId` 註冊。
場景跟協議一起漂掉時，測試會先紅，不必等 lane。

- `-mode single|split|batch`：`single` 每幀一次寫，`split` 把每幀拆兩次寫（測半包），
  `batch` 把所有幀併成一次寫（測粘包）。三種模式的位元組流相同，只有切法不同。
- `-repeat N` 同一條連線重播 N 次，`-connections N` 開 N 條並行連線。
- `-read-wait` 是最後一次寫完後等回應的空閒時間，四個協議的回應模式不同，所以用閒置視窗收斂。

`scripts/live-lanes.sh <lane>` 跑單一條 lane，log 寫到 `/tmp/swarm-pr1/lane-<n>.log`。
數資料表的 lane 需要 `PSQL_DSN`，讀 `/metrics` 的 lane 需要 `API_URL`（預設 `http://127.0.0.1:8082`），
缺環境變數時回 `skip` 與 exit code 3，跟失敗的 exit code 1 分開。

### REST 端到端（`scripts/rest-e2e.sh`）

`make smoke` 只證明解碼器到 sink；它不碰資料庫，也看不到 REST。`make rest-e2e` 補上另一段：
用 `-dsn` 起網關 → `device-sim` 回放四個場景 → 帶 `X-Api-Key` 依序斷言

- `GET /api/devices`：`-auto-register` 建出來的裝置（用場景的 `uniqueId` 對回 device id）
- `GET /api/positions`：每個場景至少一列，最新一列的 `protocol` 與場景一致
- `GET /api/events`（或 `GET /api/alerts`）：`-overspeed-knots 1` 讓回放的位置觸發 `deviceOverspeed`
- `POST /api/commands` → `GET /api/commands`：裝置離線時回 `queued:true`、狀態 `pending`；
  再回放同一場景讓裝置重連，狀態要變 `sent` 且 `attempts` ≥ 1

```bash
make db-up                                # 或自備 PostgreSQL
make rest-e2e                             # DSN=... 可指向別的庫
REST_E2E_SCENES="gt06 h02" make rest-e2e  # 只跑兩個場景
```

斷言比的是「跑之前最新一筆 id」與「跑之後最新一筆 id」，所以同一個庫可以重複跑。資料表由
`Store.Migrate` 建立，不需要先手動建 schema。macOS 用 Homebrew 的 PostgreSQL 時，`initdb` /
`pg_ctl` 要帶 `LC_ALL=en_US.UTF-8`，否則會撞到 `postmaster became multithreaded during startup`。

腳本要綁 TCP port 也要連資料庫，沙箱裡跑不動（啟動失敗會印提示），請在一般終端執行。

效能量測（本機 i5-4200U，`-benchtime 2s`）：

```bash
go test ./internal/gateway -bench 'BenchmarkGatewayIngest|BenchmarkDecodeGT06' -benchtime 5s -count 5
```

```text
BenchmarkGatewayIngest-4   	  190680	     10764 ns/op	   3.34 MB/s	     92901 positions/s	     848 B/op	       9 allocs/op
BenchmarkDecodeGT06-4      	 1000000	      2253 ns/op	     800 B/op	       9 allocs/op
```

benchmark 走 `net.Pipe`，跟其他 gateway 測試一樣不開 port。

資料庫整合測試（沒設環境變數會 skip）：

```bash
TRACCAR_POSTGRES_DSN='postgres://postgres:traccar@localhost:5432/postgres?sslmode=disable' \
  go test ./internal/storage/postgres -run 'TestStoreIntegration|TestAlertStoreIntegration|TestTokenStoreIntegration|TestUserAdminStoreIntegration|TestNotificationStoreIntegration' -v
```

## 監控（開發期）

目前可觀察的輸出：

| 來源                     | 內容                                                                                                                                                                                                                                                                                |
| ------------------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gateway.Server` log     | `<protocol>: listening on <addr>`、`<protocol>: decode: ...`、`<protocol>: read: ...`、`<protocol>: frame buffer exceeded N bytes`、`<protocol>: acknowledge command: <err>`（裝置回應已到但 `commands` 寫不回去，連線不受影響）                                                    |
| `gateway.Dispatcher` log | `dispatch: give up on command <id> of device <id> after <n> attempts`、`dispatch: load pending commands of device <id>: <err>`、`dispatch: re-encode command <id> of device <id>: <err>`、`dispatch: mark command <id> sent/failed: <err>`                                          |
| `postgres.BatchSink` log | `postgres: batch write (<n> positions): <err>`                                                                                                                                                                                                                                      |
| `alert.Pipeline` log     | `alert: write <n> reports: <err>`、`alert: queue full, dropped <n> events for device <id>`                                                                                                                                                                                             |
| `alert` 啟動 log         | `alerts: load geofences: ...`、`alerts: reload geofences: ...`（含單一圍欄解析失敗）、`alerts: load device attributes: ...`、`alerts: reload device attributes: ...`                                                                                                                |
| `notify.Dispatcher` log  | `notify: webhook, mail enabled`、`notify: <channel>: <err>`、`notify: lookup device <id>: <err>`、`notify: lookup subscriptions for device <id>: <err>`、`notify: queue full, dropped <type> for device <id>`                                                                       |
| `api.Server` log         | `api: listening on <addr>`、`api: lookup token: <err>`、`api: query events: <err>`、`api: query positions: <err>`、`api: query devices: <err>`、`api: query commands: <err>`、`api: record command <type> for device <id>: <err>`、`api: send command <type> to device <id>: <err>` |
| CLI 關閉時               | `dropped <n> positions because the queue was full`、`alert: queue full, dropped <n> events for device <id>``、`dropped <n> notifications because the queue was full`                                                                                                                         |
| `GET /api/health`        | `200 {"status":"ok","database":"ok"}`；資料庫 ping 失敗回 `503 {"status":"unavailable","database":"unavailable"}`（driver 錯誤只進 log，不外洩）                                                                                                                                    |
| 記憶體模式 sink          | 每筆 position 一行 JSON（`log.Printf("position %s", ...)`）                                                                                                                                                                                                                         |

排查順序建議：

1. **設備連上來了但沒有 position** → 看 `decode:` 或 `sink:` 訊息；
   GT06/Teltonika 若未完成握手，之後的資料幀會被忽略（log 不會報錯，這是協議行為）。
2. **同一台設備一直重連** → 通常是握手回包不對（Teltonika 回 `0x00`、JT808 未註冊成功），
   檢查 `DeviceDirectory` 是否有該 `unique_id`（`-dsn` 模式下看 `devices` 表）。
3. **position 掉資料** → 關閉前看 `dropped`，或把 `-batch-size` 調小 / `-batch-interval` 調短。
4. **有 position 但沒有告警** → 先確認 `-overspeed-knots` / `-power-cut-volts` 不是 0，
   再看規則的單位（節 / 伏特）；圍欄告警需要 `geofences` 表有資料且 `area` 能解析
   （解析失敗會印 `alerts: load geofences: ...`，該筆圍欄會被跳過，其他圍欄照常運作）。
5. **有告警但沒收到通知** → 看 `notify: <channel>: ...` 與 `traccar_notification_errors_total`；
   再確認 `-notify-types` 與裝置的 `notifyTypes` 沒有把它濾掉，裝置的 `notifyWebhook` /
   `notifyEmail` 沒有被設成 `false`；若有帳號訂閱，確認訂閱的 `notificators` 含該管道，
   且 `user_devices` 有授權該裝置（`notify: lookup subscriptions...` 是查詢失敗）。
6. **命令一直停在 `pending` 或 `sent`** → `pending` 表示紀錄寫了但 payload 沒送到（gateway 在
   中間掛掉或磁碟滿），`sent` 表示送出去了但裝置沒回：看裝置是否回 `0x80` / `0x0001`，
   以及 `acknowledge command:` 有沒有寫入錯誤。用
   `select id, status, result_code, sent_at, done_at from commands order by id desc limit 5` 看現況；
   gateway 重啟不會重送，也不會補收尾。

`GET /metrics` 以 Prometheus 文字格式輸出（手寫實作，沒有額外依賴）：

```
traccar_connections_active{protocol="gt06"} 2
traccar_frames_total{protocol="gt06"} 1520
traccar_decode_errors_total{protocol="gt06"} 0
traccar_positions_total{protocol="gt06"} 1498
traccar_position_dropped_total 0
traccar_db_write_seconds_bucket{le="0.05"} 431
traccar_db_write_seconds_bucket{le="+Inf"} 431
traccar_db_write_seconds_sum 3.21
traccar_db_write_seconds_count 431
traccar_db_write_errors_total 0
traccar_alerts_total{type="deviceOverspeed"} 4
traccar_alert_dropped_total 0
traccar_notifications_total{channel="webhook"} 2
traccar_notification_errors_total{channel="mail"} 1
traccar_notification_dropped_total 0
```

`/metrics` 需要 `read:metrics`（舊的 `-api-key` 是 admin，自然可讀；只有 `/api/health` 免憑證），
Prometheus 的 scrape 設定要帶 header：

```yaml
scrape_configs:
  - job_name: traccar
    authorization:
      credentials: please-change-me
```

語義：`connections_active` 在連線建立 / 關閉時增減；`frames` / `positions` / `decode_errors`
在解碼執行緒累加（無鎖，per-protocol 計數器在建構時就解析好）；`db_write_seconds`
每次真正寫入觀測一次；`alerts_total` 在告警進入佇列時累加，佇列滿或已關閉時計入
`alert_dropped_total`；`notifications_total` / `notification_errors_total` 在每個管道成功 / 失敗時累加，
`notification_dropped_total` 是通知佇列滿或 dispatcher 已關閉時丟棄的筆數。

## 已知限制

- 接了 `-dsn` 之後 REST 預設是**全關**的：沒設 `-api-key` 又沒有有效 token 時，除了
  `/api/health`、`/api/login`、密碼自助路由與 `/api/socket`（自己驗 query token）以外的請求都會回 401。第一次使用先帶 `-api-key` 啟動，
  用 `POST /api/users` 建立管理員並發 token（見「帳號管理 API」），或直接用 `-issue-token`；
  `-allow-anonymous` 只適合本機。對外仍建議加 TLS / 反代。
- 帳號 API 只有分頁，沒有搜尋／排序；email 建立後不可修改。第一組密碼只能由 admin 設定；
  本人改密碼、忘記密碼（email 重設碼）與登入失敗鎖定見「密碼登入」。
  鎖定是 per-account：知道 email 的人可以故意打錯密碼把該帳號鎖 15 分鐘，這是防暴力破解換來的
  拒絕服務面。忘記密碼端點沒有 IP 速率限制，只有每帳號一分鐘的節流；重設信能不能寄出取決於
  SMTP，寄失敗只留在 log。bcrypt cost 12 讓一次登入約 0.6 秒（見「密碼登入」）。
  每個請求都會查一次 token 與裝置清單，撤銷立即生效但也多一次 DB round trip。
- `PUT /api/devices/{id}` 只改 `name` / `model` / `attributes`；`protocol` 建立後不可改，
  母本其餘欄位（phone / contact / category / disabled / expirationTime）還沒建模。
  裝置仍會由 `-auto-register` 在第一個認得的幀建立（GT06 登入、Teltonika IMEI、JT808
  `0x0100`，以及沒有握手幀的 H02 第一筆位置），與 `POST /api/devices` 併用；關掉之後
  未知裝置的幀直接丟掉（H02 就只能先在資料庫建好列）。
- 命令會落地（`commands` 表）、排隊、重送並依裝置回應收尾。JT808 已經用流水號精確配對；
  GT06 / H02 / Teltonika 的回應沒有識別碼，配對仍是「該裝置最新一列 `sent`」，連續送出多筆時
  回應只收掉最新那列。重送是同一份位元組，所以對「重送一次就會執行兩次」的命令（例如
  `rebootDevice`）來說，沒有回應就重送的代價是多做一次，而不是伺服器端的去重。佇列在記憶體裡，
  gateway 重啟後靠裝置重連時的 `pending` 復原補送。`devices.protocol` 為 NULL 的舊裝置會回
  `409`，讓它重連一次即可。
- 告警只有超速 / 斷電 / 復電 / 圍欄四類；通知只有 webhook 與 email 兩種。使用者訂閱只決定
  類型與管道：email 收件者是帳號 email，webhook 仍共用 `-notify-webhook` 的 URL。門檻可用
  裝置 / 圍欄 attributes 覆寫，但只能以 SQL 維護。
- 群組授權只影響「看得到哪些裝置」，通知收件者仍只認 `user → device` 直接授權
  （`DeviceSubscriptions` 沒有展開群組）。這是 M4 通知改由事件驅動時要一起處理的缺口。
- 非 admin 只能把裝置授權給**自己**：母本用 `managedUser` 權限讓管理員代管下屬帳號，這裡還沒建
  那個模型，所以「A 把裝置給 B」目前需要 admin。
- 分享帳號只能靠 admin 事後刪除來撤銷（沒有到期自動清理）；`expiration` 只作用在 token 上。
- 圍欄已經能用 `/api/geofences` 維護（見「資源管理 API」），不必再以 SQL 寫；但圍欄仍對所有
  裝置生效，只有帶 `floor` / `ceiling` 的圍欄才會過濾高度。`/api/maintenance` 與
  `/api/maintenances` 是同一組 handler；母本還有 `/api/attributes/computed/test`（拿最新位置
  試算）還沒做，計算屬性目前只有 CRUD。
- `/api/socket` 的 token 放在 query string（瀏覽器不能對 WebSocket 設 header），所以它會出現在
  proxy 與瀏覽器的存取紀錄裡；對外請用 TLS。連線是 hijack 出來的，`http.Server.Shutdown` 不會關它。
  `{"logs":true}` 只被解析、不會有 log 串流；REST 修改裝置不會即時推給前端（母本也只在狀態轉換時推）。
- 資源清單固定依 `id` 排序（母本依 `name` / `description`），因為 storage 的共用查詢只保證
  這個順序。`statistics` 的 CRUD 在儲存層，但 REST 端點屬 M3-4。
- Calendar 只存 iCalendar 原文，VEVENT / RRULE 的重複規則還沒評估，所以 `calendarId` 目前
  不會讓排程或「行事曆內才通知」生效。`devices.group_id` 仍只讀不寫：群組 CRUD 有了，把
  裝置放進群組的端點屬 M3-2。
- `devices.model` 會被讀取，而且決定解碼路徑。GT06 的 `LW*` / `SEEWORLD` / `VL103` `LL303` `VL512` `G18`
  換掉告警碼的意義，`NT_MODELS`（`NT11` `NT20` `NT26` `NT40` `NT46` `VL100` `XT40`）換掉 `0x22` /
  `0x25` 整段欄位佈局；JT808 的 `G-360P` `G-508P` `AL300` `GL100` `VL300` 各走自己的報警字串分支，
  其餘照通用規則；比對前統一轉大寫。model 在裝置登入解析 session 時讀出並跟著連線走，所以改 model
  要重連才生效。沒有 model 的舊資料列一律走通用規則，NT 裝置未設 model 時會把幀首當成位置讀，
  位置會是垃圾值 — 這是 Java 也有的行為。
- 報表等其餘業務層仍是 Java 版負責。
- 記憶體目錄僅適合開發；正式環境請用 `-dsn`。

# 下一步

1. **命令回應精確配對延伸**：JT808 已透過流水號完成精確配對與重試機制，後續可評估 H02 / GT06 / Teltonika 是否需要或支援在回應中加入對應的識別欄位。
2. **GT06 剩餘功能補齊**：已完成 `NT_MODELS` 基礎與狀態分支，接下來可繼續補齊 `0xA3` variant 判定、WiFi 掃描（`0x17/0x2C/0x33/0xA2`）以及 `0x94` 的完整子類型（ADC/告警字串）。
3. **使用者與裝置權限設計**：引入 `users` 與 `user_devices` 關聯表，將目前「全有全無」的全域 API Key 升級為支援多租戶的細粒度權限控制。
4. **每裝置獨立門檻與通知管道**：將全域的超速／斷電門檻下放到 `devices.attributes`，並加入 Webhook 或 Email 等告警外部通知管道。

上面四條到此為止，不再往下追。三條已經被後來的程式追上，一條是母本自己也做不到的：

- 第 1 條：JT808 已用流水號精確配對；H02 / GT06 / Teltonika 的回應裡沒有可用的識別欄位
  （GT06 的 `0x80` 指令索引固定寫 0、H02 的 `R12` 帶的是應答序號、Teltonika 的 `0x00 N` 是筆數），
  母本同樣只認「該裝置最新一筆 `sent`」。這條關閉。
- 第 2 條：`NT_MODELS` 已完成，剩下的 `0xA3` / WiFi 掃描 / `0x94` 子類型是 GT06 長尾，
  併入 M6 跟其他協議一起排。
- 第 3 條：`users` / `user_devices` / `api_tokens`（scope）已完成；剩下的群組與其他資源權限併入 M2。
- 第 4 條：裝置 / 圍欄 attributes 門檻與 webhook / email 已完成；剩下的新管道併入 M4。

真正擋住「用 Go 重寫整個 Traccar」的是下面幾層，不是那四條。以下以 `../traccar-master` 為母本逐層比對。

## 架構比對

| 層       | traccar-master（Java）                                                                              | go_server 現況                                               | 差距                                                                                                                   |
| -------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------ | ---------------------------------------------------------------------------------------------------------------------- |
| 協議     | `protocol/` 272 個 `*ProtocolDecoder`，共用 HTTP / MQTT / poller 基底                               | 4 個（H02 / GT06 / JT808 / Teltonika），純函式               | 268 個；三種非 TCP 基底都沒有                                                                                          |
| 傳輸     | `ServerManager` + `TrackerServer`：TCP / UDP、TLS、每協議一個埠、`TrackerConnector` 主動連線        | `gateway.Server` 每協議一個 TCP listener                     | 無 UDP、無 TLS、無 client 模式                                                                                         |
| 連線狀態 | `session/ConnectionManager`、`session/state`（motion / overspeed 狀態機）、`session/cache` 圖狀快取 | `gateway.DeviceDirectory` + `LocationCache`                  | 無 online / offline / timeout 與事件、無狀態機、無快取圖                                                               |
| 處理管線 | `ProcessingHandler`：19 段 position handler + 12 段 event handler + `PostProcessHandler`            | 固定兩段：`alert.Pipeline`（4 條規則）→ `postgres.BatchSink` | position 只對應到 `GeofenceHandler` 的一部分，event 只對應到 Overspeed / Alarm / Geofence 的一部分，且管線寫死不可插拔 |
| 資料模型 | `model/` 40 個類別                                                                                  | `internal/model` 18 檔 33 個型別（含 Group / Geofence / Calendar / Driver / Maintenance / Order / Report / Statistics / Attribute） | 缺 media / geocoder / forward 等週邊型別                                                                               |
| 儲存     | `Storage` 泛型 + `QueryBuilder`，50 張 `tc_*` 表                                                    | 手寫 SQL + `resourceSpec` 共用 CRUD，19 張表                 | 缺的 31 張以 M4–M6 的週邊為主，且沒有通用查詢建構器                                                                     |
| 權限     | `Permission` + `PermissionsService` + `tc_user_*` / `tc_device_*` 多對多，group 最多三層            | scope + 通用 `permissions` 表（user / group / device → 任何資源），group 最多三層 | 缺 `managedUser`（管理員代管下屬）與分享帳號到期自動清理                                                               |
| REST     | 25 個 resource、81 個端點                                                                           | 110+ 條路由（資源 CRUD、單筆裝置 / 事件、四種位置匯出、WebSocket、protocol / server / session / statistics / audit、commands 下發） | 報表、媒體與 stream 端點見 M5 / M7-3                                                                                  |
| 事件     | `Event.TYPE_*` 23 種，寫在 `tc_events`                                                              | 25 種，寫在 `events`，路由是 `/api/events`（`/api/alerts` 為別名） | 日曆限制與 media 檔案欄位待 M7-3                                                                                       |
| 通知     | 9 個 notificator + `TextTemplateFormatter`                                                          | webhook + email + telegram / pushover / sms（HTTP / SNS）/ firebase / traccar push / whatsapp + Velocity 子集模板 | `command` notificator（告警轉命令）還沒接                                                              |
| 外送     | `forward/` 4 個 event + 7 個 position forwarder                                                     | 無                                                           | 全部                                                                                                                   |
| 報表     | 8 個 report provider + CSV / GPX / KML 匯出                                                         | 無                                                           | 全部                                                                                                                   |
| 排程     | `schedule/` 8 個 task                                                                               | 無                                                           | 全部                                                                                                                   |
| 地理加值 | 28 個 geocoder 檔、`speedlimit/`、`mapmatcher/`、`geolocation/`                                     | 無                                                           | 全部                                                                                                                   |
| 設定     | `Config` + 267 個 `Keys`，支援 `protocol.<name>.<key>`                                              | 38 個 command line flag                                      | 無設定檔、無 per-protocol、無動態重載                                                                                  |
| 即時推播 | `api/AsyncSocket`（WebSocket）                                                                      | `internal/websocket` + `/api/socket`：位置 / 事件 / 裝置狀態  | 沒有 log 串流與 multi-node broadcast（母本 `BaseBroadcastService`）                                                    |
| 安全     | token、OIDC、LDAP、`api/signature`                                                                  | token、密碼登入                                              | OIDC / LDAP / 簽章                                                                                                     |
| 媒體     | `media/`、`MediaEventHandler`、`VideoStreamResource`                                                | 無                                                           | 全部                                                                                                                   |
| 歷史統計 | `tc_statistics` + `StatisticsManager`                                                               | 只有 `/metrics`（即時）                                      | 全部                                                                                                                   |

Go 這邊已經贏母本、後面計畫不准動到的四件事：codec 是純函式（`[]byte` → `*model.Position`，可用 Java 測試向量直接斷言）、
`BatchSink` 非阻塞批次寫入、`/metrics` 零依賴、`cmd/device-sim` 能重播場景。任何一個里程碑都不准把它們換掉。

## 還需要做

規模用 S / M / L 標示（一個工作天為 S，一週內 M，超過一週 L）。每個里程碑的完成定義都一樣：
`go vet ./...`、`go build ./...`、`go test ./...` 全綠，加上對應的 live lane（`scripts/live-lanes.sh`），
以及 `decisions.tsv` 一列。

### M1 地基：設定、處理鏈、連線狀態、事件表（L）

後面每一段都要掛在這四件事上，所以先做。

- [x] **M1-1 `internal/config`**
  - 母本：`config/Config.java`、`config/Keys.java`（267 個 key）、`config/ProtocolConfig.java`。
  - 做什麼：`traccar.xml` → 環境變數 → flag 三層覆寫，key 名照 `Keys`，支援 `protocol.<name>.<key>` 的 per-protocol 值；先移植真的會用到的 40 個 key，其餘用到再加。
  - 驗收：現有 38 個 flag 全部改由 config 提供且行為不變；`go test ./internal/config`。
  - 落地：35 個全域 key + 4 個 per-protocol key（`internal/config/keys.go`），38 個 flag 的預設值全部改成
    「設定檔 + 環境變數算出來的值」，所以不帶 flag 時就是設定檔說的值；`-config`/`TRACCAR_CONFIG`
    指定檔案，`config.default` 照母本鏈式載入。`go test ./internal/config` 18 個測試全綠，
    `./bin/traccar-gateway -h` 的預設值逐個對過（見 README「設定」）。
- [x] **M1-2 `internal/handler` 處理鏈**
  - 母本：`ProcessingHandler`、`handler/BasePositionHandler`、`handler/PostProcessHandler`。
  - 做什麼：`PositionHandler` 介面 + 有序鏈，順序照 `ProcessingHandler` 的宣告；`alert.Pipeline` 降級成 `BusinessHandler` 一段，鏈在 `internal/handler` 組裝，`cmd` 只負責接線。
  - 驗收：`TestHandlerOrder` 斷言順序與母本一致；`alert` 不再是 `gateway` 與 `PostgreSQL` 之間唯一的一段。
  - 落地：`internal/handler.Order` 宣告 14 個階段（filter / defaultData / position / motion / geofence /
    computedAttributes / speedLimit / alertEvent / business / distance / fuel / driver / maintenance / media），
    還沒實作的留 `Placeholder`，之後要填只要在 `Config.Stages` 指定同名階段，位置不會跑掉。
    `alert.Pipeline` 現在只做規則評估（`next` 傳 nil），落地由鏈的 terminal（`BatchSink`）負責，
    所以 `business` 之後的階段和 terminal 的錯誤都會回傳給 gateway 記錄。`go test ./internal/handler` 7 個測試。
- [x] **M1-3 `internal/session`**
  - 母本：`session/ConnectionManager`、`DeviceSession`、`session/state/{Motion,Overspeed}Processor`。
  - 做什麼：連線登錄表（deviceId ⇄ conn、online / offline / timeout）、`devices` 補 `status` / `last_update` 以外的狀態欄位、motion 與 overspeed 狀態機（母本用位置間距與時間判定 `deviceMoving` / `deviceStopped`）。
  - 驗收：裝置斷線後 `status` 轉 `offline`；心跳超時產生 `deviceOffline`；`device-sim` 的斷線 / 重連腳本能重現整段。
  - 落地：`internal/session.Manager` 是連線登錄表：gateway 用 `WithSessionTracker` 回報開連線、每一幀、關連線，
    manager 以不阻塞讀取迴圈的方式把 `devices.status` 與 `deviceOnline` / `deviceOffline` 寫進 DB；
    `server.timeout`（預設 120s）沒收到資料就判離線，並透過 `Router.Disconnect` 把連線收掉。
    `internal/session.Motion` 是 motion 狀態機（速度門檻 0.01 kn、距離門檻 500 m、靜止 60s），
    掛在處理鏈的 `motion` 階段，轉換時寫 `deviceMoving` / `deviceStopped`。
  - 偏差：overspeed 狀態機沒做。`alert` 引擎目前是 `deviceOverspeed` 的唯一寫入者，若同時再加一個
    狀態機，`/api/alerts`（之後是 `/api/events`）會出現兩列同一次超速。等 M4 把告警改成事件、
    通知改成由事件驅動時一起做，見 `decisions.tsv` 2026-11-01 18:2x 那列。
- [x] **M1-4 `events` 表取代 `alerts`**
  - 母本：`model/Event`、`handler/events/*`、`tc_events`。
  - 做什麼：`events(id, device_id, position_id, geofence_id, type, event_time, attributes)`，寫入時用 `INSERT ... RETURNING id` 回填 `positions` 與 `events` 的雙向關聯；`/api/alerts` 保留為 `/api/events` 的別名一段時間。
  - 驗收：新舊路由回同一批資料；`alerts.position_id` 恒為 NULL 的已知限制消失（`TestEventLinksPosition`）。
  - 落地：`model.Alert` 整個換成 `model.Event`（型別名、`alert_time` → `event_time`、儲存層
    `InsertEvents` / `QueryEvents`），`0011_events_replace_alerts.sql` 把舊資料搬進 `events` 後刪表。
    會產生事件的定位改成 report：`alert.Pipeline` 把「位置 + 事件」排進佇列，`Store.InsertReports`
    在同一個交易裡寫位置（`RETURNING id`）再寫事件，所以 `events.position_id` 一定有值；
    沒有事件的定位照舊走 `BatchSink`。`GET /api/events` 與 `GET /api/alerts` 共用同一個查詢。
  - 測試：`TestEventLinksPosition`（含同裝置同時刻兩筆）、`TestEventsAndAlertsReturnTheSameRows`、
    `TestInsertReportsIntegration`（`TRACCAR_POSTGRES_DSN` 有設才跑）。

### M2 資料模型與權限（L）

- [x] **M2-1 補模型**：Group / Geofence（完整欄位）/ Calendar / Driver / Maintenance / Order / Report / Statistics / Attribute；`ExtendedModel` 的 attributes 存取子（`getString` / `getDouble` / `getBoolean` / `getInteger`）照母本語義，含型別錯誤時回預設值。
  - 驗收：每個型別都有 table 與 CRUD 測試；attributes 的型別轉換測試對齊 `ExtendedModelTest`。
  - 落地：`internal/model/attributes.go` 的 `Attributes` 有 `Has` / `Set` / `GetString` / `GetDouble` / `GetInteger` / `GetLong` / `GetBoolean` / `Remove`。
    `Set` 跟母本一樣丟掉 nil 與空字串；唯一刻意偏差是**解析失敗回預設值**（母本 `parseAsDouble` 會拋
    `NumberFormatException`），因為一個壞掉的裝置屬性不該讓整幀或整列失敗。`Device` / `Position` /
    `Event` / `Command` / `geofence.Geofence` 的 attributes 全部換成這個型別，`geofence` 原本的
    `numberAttribute` 刪掉，`floor` / `ceiling` 改用 `GetDouble`。
    模型：`Group`（`GroupID` 指父群組）、`Calendar`（只存 iCalendar 原文）、`Driver`（uniqueId 去空白）、
    `Maintenance`、`Order`、`Report`（`tc_reports` 的排程定義）、`Statistics`（`protocols` 存 JSONB）、
    `Attribute`（computed attribute）。原本「位置＋事件」的聚合改名 `PositionReport`，把 `Report` 讓給母本
    的 `tc_reports`。`Geofence` 補 `CalendarID` / `Description`，以及把 WKT 印回去的 `WKT()`。
    `0012_resources.sql` 建 calendars / groups / drivers / maintenances / orders / reports / statistics /
    attributes 八張表，並替 `geofences` 補 `calendar_id`、`description`，替 `devices` 補 `group_id`。
    儲存層用 `resourceSpec` 把「欄位順序、關鍵字欄、掃描」跟模型放在一起，CRUD 共用一套
    create / update / delete / list，找不到一律 `model.ErrResourceNotFound`。
  - 測試：`TestAttributes*`（10 個，型別轉換與 Set/Remove 語義）、`TestResourceQuery*`（分頁夾取、id 集合、
    關鍵字）、`TestResourcesMigration`；`resources_integration_test.go` 對九種資源各跑一次
    create → list → update → delete（`TRACCAR_POSTGRES_DSN` 有設才跑）。這台機器跑不了 PostgreSQL
    （sandbox 擋 `shmget`），所以整合測試在此未執行。
  - 已知限制：Calendar 的 recurrence（VEVENT / RRULE）還沒實作，目前只有 CRUD；`devices.group_id`
    只讀不寫，寫入留給 M2-2 的權限指派。
- [x] **M2-2 權限**：`tc_user_*` / `tc_device_*` 的通用多對多表、`Permission`、`PermissionsService`（`MAX_GROUP_DEPTH = 3`）、`/api/permissions`、`/api/share/{device,group}`。
  - 驗收：group 三層展開的查詢測試；越權一律 403；現有 `permissions_test.go` 擴充到 group 與共享屬性。
  - 落地：母本是一種配對一張表（`tc_user_device`、`tc_group_geofence`、…），這裡合成一張
    `permissions(owner_type, owner_id, property_type, property_id)`，把配對型別當資料，新增資源不必
    再加 migration；`user_devices` 的資料由 `0013_permissions.sql` 搬進去後刪表。
    `internal/permissions` 有 `Object` 註冊表、`Permission`（JSON 依 body 順序決定 owner，
    所以用 token 串流解，不用 map）、`Reach` / `Depth` / `CheckHierarchy`（純函式，`MAX_GROUP_DEPTH = 3`，
    含循環與自我父節點防護）與 `Service`（`Store` 介面背後是 postgres，測試用記憶體實作）。
    token 認證時把 `user → group` 展開成裝置清單（`Reach` + `devices.group_id`），
    所以 `Principal.DeviceIDs()` 已經是「含群組繼承」的完整集合，後面所有 REST 查詢不必再改。
    `/api/permissions` 與 `/api/share/{device,group}` 照母本語義（裸陣列、單筆／批次、
    第一鍵為 owner、分享帳號 `temporary` 且唯讀、第二次分享 `409`），並補了 scope
    `read:permissions` / `write:permissions` / `write:share` 與 `-disable-share`。
    儲存層在刪帳號 / 刪裝置時清掉相關配對（通用表沒有 FK）。
  - 測試：`internal/permissions` 29 個（物件名稱、JSON 順序、拒絕形狀、`Reach` 三層與第四層、循環、
    `CheckHierarchy`、`DeviceIDs` 群組展開、`CanAccess` / `Check`）；`internal/api` 新增 21 個
    （列舉、萬用字元僅 admin、授權 / 撤銷、批次、壞 body、非 admin 越權 403、分享建帳號 / 409 /
    範圍外的裝置 403 / `-disable-share` / 只有 api-key 不能分享 / 到期時間），放在
    `internal/api/object_permissions_test.go`；原本的 `permissions_test.go` 仍管 token 的 scope 與
    裝置範圍，另外在 `internal/auth` 加了一個測試確認三個新 scope 進得了 `KnownScopes`。
  - 偏差：非 admin 不能把裝置授權給別人（母本的 `managedUser` 還沒移植）；通知收件者仍是直接授權。
  - 未驗證：`0013_permissions.sql` 與 postgres 實作只做到單元測試與編譯，這台機器起不了
    PostgreSQL（sandbox 擋 `shmget`），`TRACCAR_POSTGRES_DSN` 的整合測試會 skip。
- [x] **M2-3 資源 CRUD**：`/api/groups`、`/api/geofences`、`/api/calendars`、`/api/drivers`、`/api/maintenances`、`/api/orders`、`/api/reports`、`/api/attributes/computed`。
  - 參數照母本 `SimpleObjectResource` / `ExtendedObjectResource`：`all`、`userId`、`groupId`、`deviceId`、`excludeAttributes`、`limit`、`offset`、`keyword`。
  - 驗收：圍欄不用再以 SQL 維護；`alerts` 的圍欄失效條件（`floor` / `ceiling`）改由 API 寫入並經 `TestGeofenceCRUD`。
  - 落地：`internal/api/resources.go` 用一個泛型 `resource[T]` 描述「storage 呼叫 + 權限物件 + JSON 形狀」，
    list / get / create / update / delete 只寫一次，九個路徑（含 `/api/maintenance` 與
    `/api/maintenances` 兩種拼法）共用。清單回裸陣列、`POST` 201、`PUT` 200、`DELETE` 204；
    `excludeAttributes` 用 `*model.Attributes` 的 `omitempty` 拿掉欄位。`area` 在寫入時就
    `geofence.Parse`，`calendars.data` 直接用 `[]byte` 讓 encoding/json 與 Jackson 一樣走 base64。
    母本的 `checkEdit`（readonly / 額度）與計算屬性試算 `/test` 沒移植；其餘權限語義照搬：
    `all` 只對 admin 放寬、`userId` 非 admin 只能填自己、`groupId` / `deviceId` 先檢查再過濾、
    非 admin 寫入後自動加 `user → 資源`、只有 admin 能寫計算屬性。群組的父節點與深度用
    `permissions.CheckHierarchy`（三層、防循環）；`geofence` / `report` 的 `calendarId` 需碰得到。
  - 儲存層：刪除資源時一併 `ClearPermissions`（通用配對表沒有 FK），所以孤兒授權不會留下來。
    `internal/permissions` 新增 `Service.ObjectIDs`（裝置 / 群組會展開子群組，其餘只認直接授權）
    並讓 `Check` 對群組也吃父群組授權，對齊母本 `Condition.Permission`。
  - 測試：`internal/api/resources_test.go` 17 個測試函式（未配置 501、scope 403、參數轉發與壞參數 400、
    `all` / `userId` / `groupId` / `deviceId`、`excludeAttributes`、建立後授權與授權失敗回滾、
    群組深度 400、日曆存取 403、`TestGeofenceCRUD` 的 create → get → update → delete、base64
    日曆、maintenance 別名、計算屬性只有 admin 能寫）；`internal/permissions` 新增
    `TestServiceObjectIDs` 與 `TestServiceCheckGroupInheritsFromGrantedParent`。
  - 未驗證：沒有 PostgreSQL 可用（sandbox 擋 `shmget`），`TRACCAR_POSTGRES_DSN` 的整合測試仍 skip；
    實際 SQL 只做到編譯與單元測試，`TestGeofenceCRUD` 用的是記憶體 store。

### M3 REST 對齊與即時推播（M）

- [x] **M3-1 `/api/events`**：`GET`（`deviceId` / `type` / `from` / `to` / `groupId`）、`GET /{id}`。
  - 落地：`groupId` 先用 `permissions.Service.GroupDeviceIDs` 展開成「該群組 + 子群組的裝置」，
    再和 token 的裝置範圍取交集；群組碰不到回 403，群組底下沒有裝置時送的是**空集合**而不是
    nil（nil 在 storage 代表「不限」，這個坑有測試釘住）。`GET /api/events/{id}` 用
    `EventFilter.ID` 查單筆，不存在回 404、裝置碰不到回 403。
  - 測試：`TestEventSingle`、`TestEventsGroupFilterExpandsToChildGroups`、`TestEventQueryByID`、
    `TestServiceGroupDeviceIDs`。
- [x] **M3-2 既有端點補參數**：`/api/devices` 加 `uniqueId` / `id` / `all` / `keyword` / `excludeAttributes`，`PUT` 可寫 `attributes`（現在只能 SQL），`GET /{id}` 單筆；`/api/positions` 加 `geofenceId`、`KMZ` / `CSV` / `GPX` 匯出。
  - 落地：`DeviceFilter` 多了 `UniqueIDs` / `IDs` / `Keyword`；`uniqueId` 與 `id` 是精確比對、
    兩組之間 OR，同時出現時忽略 `keyword`（照母本 `DeviceResource` 的分支）。`excludeAttributes`
    用 `*model.Attributes` + `omitempty` 拿掉欄位。`PUT /api/devices/{id}` 的 `attributes` 用
    指標區分「沒給」與「給空物件」，SQL 端 `COALESCE($4::jsonb, attributes)` 保留原值；只改名的
    請求不會洗掉門檻。`GET /api/devices/{id}` 走 `Store.Device`。`userId` 讓 admin 列出另一個
    帳號的**直接**授權（母本 `.excludeGroups()`），非 admin 只能填自己；舊 API key 與未認證
    模式沒有帳號，忽略這個參數。`all` 收下但不改變結果，因為這裡的 admin 本來就看得到全部。
  - 匯出：`/api/positions/{kml,kmz,csv,gpx}` 用母本的四條路徑與參數；軌跡改成
    `device_time ASC`（`PositionFilter.Ascending`），storage 單次查詢上限 1000 筆，所以用 offset
    分頁讀到 50000 筆（母本 `report.maxPositions` 預設）。KML / GPX 用 `encoding/xml`，KMZ 是
    內含 `doc.kml` 的 zip，CSV 依母本欄位順序並套用 `= + - @` 的公式防護。
  - 測試：`TestDevicesLookupAndKeywordParameters`、`TestDevicesExcludeAttributes`、
    `TestDeviceSingle`、`TestUpdateDeviceAttributes`、`TestDevicesUserIdFilter`、
    `TestDevicesUserIdIgnoredWithoutAccounts`、`TestPositionsGeofenceFilter`、
    `TestPositionsCSVExport`、`TestPositionsGPXExport`、`TestPositionsKMLExport`、
    `TestPositionsKMZExport`、`TestPositionsExportValidation`、`TestExportPositionsPagesTheTrack`，
    加上 storage 的 `TestDeviceQueryLookups` / `TestDeviceQueryKeyword` /
    `TestPositionQueryAscending` 與 permissions 的 `TestServiceDirectIDsSkipsGroupExpansion`。
  - 偏差：母本 `PositionResource` 的 `id`（多筆位置）、`DELETE /api/positions/{id}` 與整段刪除
    沒做；`geofenceId` 指到不存在的圍欄回 404（母本默默忽略）。CSV 沒有 address / fixTime 欄位，
    因為這裡的 model 只有一個 `deviceTime`；時間固定 UTC，沒有使用者時區。
- [x] **M3-3 WebSocket**：`/api/socket`，推送 position / event / device 變更（母本 `api/AsyncSocket`）。
  - 落地：新增 `internal/websocket`，自己實作 RFC 6455 的伺服器端子集（握手、文字 / 二進位訊息與
    fragmentation、ping / pong、close handshake、控制幀規則、client 必須遮罩、1 MiB 訊息上限、
    非 UTF-8 文字拒絕），沒有新增任何外部依賴。`/api/socket` 因為瀏覽器不能設 header 而列進
    `exemptPaths`，由 handler 自己用 `?token=` 驗 `-api-key` 或 `api_tokens`，驗證失敗一樣回 401。
    連上先送 `{"positions":[...]}`（`Store.LatestPositions` 的 `DISTINCT ON (device_id)`），
    之後由三個 publisher 推：`alert.WithPositionHook`（每筆處理過的定位）、alert hook（事件，
    與通知 dispatcher 串在一起）、`session.Config.OnStatus`（上線 / 離線，回查整列裝置）。
    每個連線只收 token scope 允許的訊息種類（`read:positions` / `read:alerts` / `read:devices`），
    裝置範圍沿用 `user_devices` 展開；每 30 秒送 `{}` keepalive。扇出是非阻塞的（64 則佇列、
    滿了就丟新的），沒有連線時 publisher 直接跳過 JSON 編碼。
  - 測試：`internal/websocket` 10 個（RFC 範例 accept key、拒絕壞握手、文字往返、fragmentation、
    ping→pong、close echo、未遮罩拒絕、過大訊息、非 UTF-8、重複 Close）；`internal/api` 6 個
    （未配置 501、缺 token / 未知 token 401、沒有讀取 scope 403、seed、依裝置範圍與 scope 過濾、
    忽略 `{"logs":true}` 與斷線後移除連線）；`internal/alert` 的
    `TestPipelinePositionHookSeesEveryPosition`、`internal/session` 的
    `TestStatusHookSeesEveryTransition`、storage 的 `TestLatestPositionsQuery`。
  - 偏差：沒有 log 串流（母本 `logs` 選項只解析不送）、沒有 multi-node `BaseBroadcastService`、
    hijack 出來的連線不受 `http.Server.Shutdown` 管。REST 改裝置不會推（母本也只在狀態轉換時推）。
  - 風險：token 在 query string 會進 proxy / 瀏覽器紀錄，和母本相同，對外請走 TLS。
- [x] **M3-4 其餘資源**：`/api/protocols`、`/api/server`、`/api/session`、`/api/statistics`、`/api/audit`、`/api/commands/send|bulk`、`/api/notificators`。
  - 落地：新增 `protocol_settings`、`server_settings`、`actions` 三張表與 `users.attributes`（`0014_admin.sql`）。
    `GET/PUT /api/protocols` 讀寫每個協議的 port / transport；`PUT` 由 `listenerSet`
    即時停掉舊 listener、改綁新 port，綁不起來會把舊地址接回去，重啟後也以資料表為準。
    `GET /api/server` 免認證（登入頁要用），回傳 `server_settings` 加 `emailEnabled` /
    `geocoderEnabled` / `textEnabled` / `newServer` / `version`；`PUT` 是 admin 且為部分更新。
    另有 `/api/server/timezones`（讀系統 tz 表，沒有就回內建清單）、`/api/server/geocode`（M6-3
    沒接就 501）、`/api/server/gc`、`/api/server/reboot`（呼叫 process 的 cancel）、
    `POST /api/server/file/{path...}`（`web.path` 底下的路徑，逃出根目錄回 400）。
  - Session：`POST /api/session` 收 form 或 JSON，成功後把 bearer token 寫進 `traccar_session`
    cookie（HttpOnly / SameSite=Lax，24h），`GET /api/session` 讀 cookie 或 `?token=`（QR / native
    登入）回目前帳號，`DELETE` 撤銷該 token 並清 cookie，`GET /api/session/{id}` 讓 admin 切換帳號、
    `POST /api/session/token`（回純文字 token）與 `POST /api/session/token/revoke`。
    `RequireAuth` 現在也會讀 cookie；免認證路徑若帶了憑證仍會驗，所以 `PUT /api/server` 在同一個
    路徑上還是 admin-only。
  - Commands：`GET /api/commands/types?deviceId&textChannel` 由協議 encoder 支援清單過濾
    （`gateway.Protocol.TextCommands/DataCommands`），`POST /api/commands/send` 支援 `groupId`
    一次送群組（回 `{commands,count,failed}`），`POST /api/commands/bulk` 收 `commands[]` 或
    `deviceIds[]` + `type`。`GET /api/commands/send` 回空陣列：這裡的 `commands` 表是歷史，沒有
    母本的 tc_commands 範本表。
  - 統計 / 稽核：`GET /api/statistics`（admin，`from`/`to`）與 `/api/statistics/devices`（admin，
    區間上限 24h，逐裝置數 `positions`）；`GET /api/audit`（admin）回 `actions` 並帶上 email。
    M3-4 起登入 / 登出 / 失敗登入、devices、users、tokens、notifications、資源 CRUD、
    permissions link/unlink、command 都會寫一列 action。
  - Notificator：`GET /api/notificators` 與 `/api/notifications/notificators` 回可用的通道
    （命令列配置的 + 內建 mail/webhook），`/api/notifications/types` 回事件型別加 `all`。
  - 測試：`internal/api/admin_test.go` 覆蓋 protocols CRUD 與非 admin 403、server 匿名讀 /
    部分更新 / admin 寫、statistics 與 24h 上限、audit 權限、cookie 登入 / 登出 / `?token=`、
    token 發行、`commands/types` 與群組下發、notificators。
  - 偏差：沒有 tc_commands 範本（只存歷史）、`storageSpace` 未回報、reboot 走 process 取消而非
    `System.exit`、`/api/session` 不回 JSESSIONID（cookie 內是 bearer token）。
- [ ] 驗收：`traccar-web` 的請求逐條打過（母本 81 個端點的清單當對照表），缺的列在 `docs/`。

### M4 事件與通知補齊（M）

- [x] **M4-1 event handlers**：Alarm / Ignition / Motion / Geofence（含 `geofenceCrossed`）/ Fuel / Driver / Maintenance / Media / Behavior / Proximity / CommandResult，逐支掛到 M1-2 的鏈上。
  - 落地：`model.Event` 補到 25 種型別（母本 23 種加既有的 `powerCut` / `powerRestored`），
    `EventTypes()` / `IsEventType()` 一起更新。
  - 事件的搬運方式改成母本 `Callback` 的等價物：`internal/eventctx` 把「這一筆位置產生的事件」
    放在 `context` 裡，`handler.Chain` 每筆位置開一個緩衝，規則在各自的階段 append，鏈的終端
    一次寫入。`alert.Pipeline.HandleContext` 讓引擎事件也進同一個緩衝，`EnqueueReport` 把
    「位置 + 全部事件」排進原本的 report 佇列，並在這裡才觸發 socket / 通知 hook。這順帶修掉
    M1-4 遺留的重複寫入：有事件的定位以前會同時被 `InsertReports` 與 `BatchSink` 各寫一次。
  - 規則（`internal/handler/rules.go`）：Alarm（`event.ignoreDuplicateAlerts` 預設 true）、
    Ignition、Behavior（`event.behavior.accelerationThreshold` / `brakingThreshold`，m/s²）、
    Fuel（`fuelDropThreshold` / `fuelIncreaseThreshold`）、Driver、Maintenance、Media、
    CommandResult、Proximity（`proximityEnterDistance` / `proximityExitDistance` /
    `unaccompaniedDistance`）。每個門檻都可被同名裝置屬性覆寫，跟母本 `AttributeUtil.lookup` 一樣。
  - `geofenceCrossed`：`geofence.Geofence.CrossesSegment` 對 polygon 做線段與邊的相交、對 circle
    做「兩端都在外但線段碰到圓」，由 `event.geofence.segmentCrossing`（預設 false）開啟；
    engine 現在保存每個裝置的上一筆位置。
  - 裝置資料來源：`eventState` 在背景刷新 `permissions` 的 device→maintenance 與 device→device
    連結（`Store.DeviceMaintenances` / `Store.DeviceLinks`），並用 position hook 即時更新每台車的
    最新位置，proximity 不必查 DB。命令結果事件在 `AcknowledgeCommand` 的同一個交易裡寫
    `commandResult`；裝置在位置裡回報 `result` 時另外由 `CommandResultRule` 產生一列。
  - 測試：`internal/handler/rules_test.go`（9 個規則、裝置門檻覆寫、緩衝到終端）、
    `internal/alert` 的 `TestGeofenceSegmentCrossing`。
  - 偏差：日曆限制（geofence / maintenance 的 calendarId）還沒套用；`Motion` 仍走 M1-3 的
    簡化狀態機，沒有母本 `NewMotionProcessor` 的距離 / 停留門檻。
  - 驗收：每支都有以母本事件語義寫的測試；`commandResult` 事件與 `commands` 表的收尾是同一次寫入。
- [x] **M4-2 notificator**：telegram / pushover / sms（HTTP + SNS）/ firebase / traccar（轉命令）/ whatsapp，加上 `TextTemplateFormatter`（Velocity 子集）。
  - 落地：`notify.Channel` 多一個 `UserTarget`，把「這個帳號在這個通道的收件地址」從
    `users.attributes` 取出來（`telegramChatId` / `pushoverUserKey` / `phone` /
    `notificationTokens`），沒有 `UserTarget` 的通道（webhook）維持「訂閱即啟用伺服器預設值」。
    `DeviceSubscriptions` 現在一起讀 `users.attributes`。
  - 各管道（`internal/notify/`）：`telegram.go`（bot API、可換 base URL、每個 chat id 一則）、
    `pushover.go`（token + user，支援 `user@device`）、`sms.go` + `sms_sns.go`（HTTP 模板依
    `<` / `{` / `[` 決定 content type；AWS SNS 用自製 SigV4，不拉 SDK）、`firebase.go`（HTTP v1，
    服務帳號 JWT → OAuth2 token → 逐 token 發送，token 快取）、`traccarpush.go`（官方 push
    服務）、`whatsapp.go`（Cloud API 樣板訊息）。
  - 模板：`template.go` 的 `TextTemplateFormatter` / `RenderTemplate` 支援 `$name`、`${a.b}`、
    `$$` 跳脫、`#if($x)…#else…#end`（可嵌套），缺欄位輸出空字串；map 與 struct 都能走
    （欄位名不分大小寫），所以 `${event.type}` / `${device.name}` / `${position.speed}` 直接可用。
  - REST：`GET /api/notifications/notificators` 支援 `announcement=true`（濾掉 command / webhook），
    `POST /api/notifications/test[/{notificator}]` 與 `POST /api/notifications/send/{notificator}`
    （admin，回 204）由命令注入的 `notifierService` 實作。
  - 設定：新增 `notificator.telegram.*`、`notificator.pushover.*`、`notificator.firebase.*`、
    `notificator.traccar.*`、`notificator.whatsapp.*`、`sms.http.*`、`sms.aws.*`；這些走設定檔 /
    環境變數，不佔命令列 flag。
  - 測試：`internal/notify/notifiers_test.go`（6 個管道用假的 `http.RoundTripper`，含 SNS 簽章與
    FCM JWT / token 流程、失敗狀態）、`template_test.go`（變數、if/else、巢狀、缺欄位、`$$`）、
    `TestParseNotificatorsAcceptsEveryChannel`、`TestSubscriptionTargetsUseUserAttributes`；
    `internal/api` 的 `TestNotificationTestAndSend`、`TestNotificatorsAnnouncementFilter`。
  - 偏差：`command` notificator（把告警轉成裝置命令）只接受名稱、沒有對應通道；Firebase 只支援
    FCM token，沒有 Java 的 22 字元 FID 分支；telegram 只送文字，沒有 `sendLocation`。
- [ ] **M4-3 比對規則**：`NotificationManager` 的 `notificators`、`always`、`notifyTypes`，以及裝置 / 使用者兩層 override。
  - 驗收：訂閱測試擴充到「同一事件命中多個範本時只送一次」。

### M5 報表與外送（M）

- [ ] **M5-1 報表**：route / trips / stops / summary / events / geofence / devices / combined，加 CSV / GPX / KML 匯出與 `xlsx|mail` 變體。
  - 驗收：同一份 positions 的報表輸出與母本 `reports/` 的測試向量一致（時間、距離、油耗欄位）。
- [ ] **M5-2 forwarder**：position 與 event 兩條，先 json / url / mqtt，再 amqp / kafka / redis / wialon。
  - 驗收：斷線重連與佇列滿的行為有測試（母本沒有，這裡要贏）。

### M6 傳輸層與協議廣度（L）

- [ ] **M6-1 傳輸**：UDP（`TrackerServer` datagram）、TLS、HTTP 型協議基底（`BaseHttpProtocolDecoder`）、poller（`BaseProtocolPoller`）、MQTT 基底（`BaseMqttProtocolDecoder`）。
  - 驗收：每個基底各有一個示範協議走通（HTTP 用 OsmAnd、poller 用 Arnavi、MQTT 用一個現成協議）。
- [ ] **M6-2 協議批次**：先 8 個高頻家族（Meitrack、Wondex、TK103、Gps103、Watch、Ruptela、Suntech、Eelink），每個都要 Java 測試向量 + 場景檔 + 埠設定；GT06 長尾（`0xA3` / WiFi / `0x94`）與 JT808 批次定位一併收在這裡。
  - 驗收：每個協議一支 `*_test.go` 跑母本向量，`device-sim` 有對應場景。
- [ ] **M6-3 地理加值**：geocoder（先 2 個）、speedlimit（Overpass）、mapmatcher、geolocation（cell tower）。外部服務要有 key 缺省與離線快取，不能讓它擋住 ingest。

### M7 運維與規模（M）

- [ ] **M7-1 schedule**：device inactivity、expirations、session timeout、reports、health check、delete temporary。
- [ ] **M7-2 `tc_statistics`**：每 device / protocol 的訊息量寫 DB，補上 `/api/statistics` 的歷史查詢（`/metrics` 只管即時）。
- [ ] **M7-3 媒體**：`media/`、`MediaEventHandler`、`VideoStreamResource`。優先度最低，等 M1–M5 都穩定再談。

## 可以做

不是 parity，但對這個專案有回報，隨時可以插隊：

- **命令佇列外部化**：現在佇列在記憶體，gateway 重啟要等裝置重連才補送（見「已知限制」）。搬到 Redis / NATS 後重啟不掉命令，也順帶解決多節點共用。
- **token 與裝置清單快取**：現在每個請求查兩次 DB（見「已知限制」）。加一層帶版本號的快取，撤銷時 bump 版本，仍然立即生效。
- **`positions` 分區 / TimescaleDB**：`positions` 會是最大的表，改成按月分區加保留策略，報表才有機會跑得動。
- **decoder fuzz**：四個 decoder 已經能用 Java 向量測，再補 `go test -fuzz`，長尾協議移植時可以直接量測。
- **協議分 build tag**：`internal/protocol/<name>` 用 `init()` 註冊，長尾協議用 build tag 排除，主 binary 不會被 272 個協議撐大。
- **`device-sim` chaos**：現有場景是「正常送」，再加半包、亂序、重送、斷線重連，M1-3 與 M5-2 都要靠它驗。
- **OpenAPI + 契約測試**：母本沒有；把 31 條路由的 request / response 固化成 spec，M3 補端點時不會改壞舊的。
- **`/api/alerts` → `/api/events` 相容層**：前端（同機的 `traccar_web_ts`）不用跟著改一次。

## 先不做

- 272 個協議全移植：按客群推進，M6-2 一批一批來。
- OIDC / LDAP / `api/signature`：企業才有需求，`api/security` 那層等有案子再開。
- 母本那批 geocoder 全移植：先 2 個，其餘等真的有人用。
- `web/McpServer`、`ConsoleServlet`：開發工具，Go 這邊有 `/metrics` 與 log 就夠。
- 影片串流（`JT1078`、`media/VideoStream`）：頻寬與儲存是另一個量級，除非客戶指定。

## 建議順序

M1 →（M2 ∥ M4）→ M3 → M5 → M6 → M7。

M1 決定後面每一段掛在哪；M2 的權限是所有 REST 的前置；M4 的事件型別要先齊，M3 的 `/api/events` 才有東西可查；
M3 依賴 M1-4 的表與 M2 的資源。M5 可以跟 M6 平行，但 M6 的 per-protocol 設定要等 M1-1。
M2 與 M6-2 都是「一直往裡加」的長工，適合跟其他里程碑交錯做。

## Gemni 建議與補充說明

### 1. 協議聚焦與調整 (H02 / GT06 / JT808 / OsmAnd)

- **OsmAnd 協定實作**：OsmAnd 採用 HTTP GET/POST 傳輸（例如 `/5055?id=123456&lat=25.03&lon=121.56&speed=15`），應在 `internal/protocol/osmand` 實作純解碼器，並於 HTTP REST 服務中開啟 `/osmand` 或獨立 HTTP 埠接收請求。
- **GT06 / JT808 長尾維護**：GT06 需注意特定車型（如 `NT_MODELS`）的欄位版面差異；JT808 應確保轉義字元（`0x7E` ↔ `0x7D02`/`0x7D01`）與多包拼包處理之穩定度。

### 2. 測試與運維驗證 (Testing & Verification)

- **OsmAnd 測試**：提供 `curl` 標準測試範例，並於 `cmd/device-sim` 補充 HTTP OsmAnd 模擬腳本。
- **指令配對與重試**：JT808 透過流水號 (Serial Number) 已實現精確 ACK 配對；GT06 / H02 因協議本身無流水號欄位，建議維持「最新一筆 Sent 命令收尾」機制，避免過度設計。

### 3. 可擴充性建議 (Scalability & Architecture)

- **即時推播 (WebSocket)**：建議於 M4 階段新增 `/api/socket`，實時推播 Position 與 Alert 給前端頁面。
- **Redis / MQ 訊息佇列**：當前命令與告警佇列存於記憶體，重啟會遺失排隊中指令。未來在生產環境可評估將指令佇列移至 Redis / NATS。

### 核對結果（2026-11-01）

逐條對過程式與母本（`traccar-master`）：

| 建議                                                         | 判定                | 依據                                                                                                                                                                                                                                    |
| ------------------------------------------------------------ | ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| OsmAnd 走 HTTP GET/POST，例 `/5055?id=…&lat=…&lon=…&speed=…` | ✅ 正確             | 母本 `ProtocolConfig` 給 OsmAnd 的預設埠就是 5055；`OsmAndProtocolDecoder` 除了 query string 也吃 `application/json` body，欄位還有 `timestamp`、`location`、`cell`、`wifi`、`valid`。Go 這邊還沒有這個解碼器，屬於 M6-1 的 HTTP 基底。 |
| GT06 要注意 `NT_MODELS` 的欄位版面差異                       | ✅ 正確             | `0x22` / `0x25` 的 NT 版面已實作，見上面 GT06 段與「已知限制」。                                                                                                                                                                        |
| JT808 的 `0x7E` ↔ `0x7D02` / `0x7D01` 轉義要穩               | ✅ 正確             | `internal/protocol/jt808/frame.go` 已處理，另含 `0xE7` 分隔符與 `0xE6` / `0x3E` 轉義表。                                                                                                                                                |
| JT808 多包拼包要穩                                           | ❌ 目前沒有         | 標頭只取 `bodyLength = attribute & 0x03ff`，沒看分包旗標 `0x2000`；帶分包旗標的 `0x0200` 會被當成完整訊息解析。這是真缺口，不是「穩定度」問題。                                                                                         |
| JT808 用流水號（Serial Number）精確 ACK 配對                 | ✅ 正確             | `codec.SerialSource` 給每筆命令號碼並存 `commands.serial`，`0x0001` 回應的流水號對回那一列；重送沿用同一個號碼。                                                                                                                        |
| GT06 / H02 沒有可配對的欄位，維持「最新一筆 sent」收尾       | ✅ 結論對，理由要修 | 協議本身不是沒有欄位：GT06 的 `0x80` 回應帶命令索引、H02 的 `R12` 帶應答序號。是母本的 encoder 把 GT06 索引固定寫 0，兩邊的回應都不足以區分多筆在等回應的命令，所以只能收最新那列。                                                     |
| 加 `/api/socket` 推播 position / alert                       | ✅ 正確且是缺口     | 母本是 `api/AsyncSocketServlet`；本 repo 的 roadmap M3-3 已列同一件事。                                                                                                                                                                 |
| 佇列在記憶體，重啟會遺失排隊中指令                           | ⚠️ 只對一半         | 接 `-dsn` 時命令先寫進 `commands` 表，重啟後裝置重連會把 `pending` 列重新編碼補送（`Dispatcher.PendingCommands`）；真的會丟的只有記憶體模式。搬到 Redis / NATS 的正當理由是**多節點共用**與縮短復原時間，不是「重啟遺失」。             |

接下來可以挑一條

- ~~接 DB 把 REST 走通~~ **已完成（2026-11-01）**：`scripts/rest-e2e.sh` / `make rest-e2e` 在真
  PostgreSQL 上回放四個場景，斷言 `/api/positions`、`/api/alerts`（`deviceOverspeed`）與
  `POST /api/commands` → 裝置重連後 `sent` 都成立。順帶修掉 `0009_commands_delivery.sql`
  讓全新資料庫連 migration 都過不了的問題：`splitStatements` 把註解裡的 `;` 當成語句結尾。
- M3-3 WebSocket /api/socket + 接 traccar_web_ts 前端——你原本想要「看得見」的介面就是這條。
- JT808 分包（0x2000）——上次核對時找到的真缺口：帶分包旗標的 `0x2000` 現在會被當完整訊息解成垃圾位置。這條最適合先補一個會紅的測試再修。
