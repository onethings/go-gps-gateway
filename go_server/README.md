# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的主力設備協議（H02 / GT06 / JT808 / Teltonika），
只做「解碼 → 標準 Position → 落地」，網路層與業務層之後再分段替換。

- 母本：`../traccar-master`（Java），每個 Go 檔案的註解都標明了對應的 Java 類別。
- 設計原則：協議層是**純函式**（`[]byte` → `*model.Position`），所有 I/O 集中在 `internal/gateway`。
- 現況：四個協議的 codec + TCP 網關 + PostgreSQL 落地 + 告警規則 / 地理圍欄 / 告警通知（webhook / email，
  含使用者訂閱）
  - REST（裝置管理、位置查詢、告警查詢、健康檢查、命令下發、`/metrics`、使用者 token / 裝置擁有權、
    帳號與 token 管理、密碼登入、通知訂閱）全部可編譯、`go test ./...` 全綠。

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
                   └─ Sink              寫入管線
                        ├─ alert.Pipeline  規則評估 → alerts 批次落地
                        │    └─ notify.Dispatcher  webhook / SMTP 通知（背景傳送）
                        └─ postgres.BatchSink  positions 批次落地

client ──HTTP──▶ api.Server (RequireAuth)
                   ├─ 憑證：-api-key（admin）、api_tokens（scope + user_devices）或 /api/login 換發的 token
                   ├─ GET /api/devices   ──▶ devices 查詢 / 新增 / 修改 / 刪除
                   ├─ GET /api/positions ──▶ positions 查詢（自動加裝置條件）
                   ├─ GET /api/alerts    ──▶ alerts 查詢（自動加裝置條件）
                   ├─ GET /api/health
                   ├─ POST /api/login    ──▶ email + 密碼換發一組 token（唯一不用帶憑證的路由）
                   ├─ GET /metrics       ──▶ Prometheus 文字格式
                   ├─ POST /api/commands ──▶ commands 落一列 ──▶ gateway.Router ──▶ 該協議 encoder ──▶ 設備連線
                   ├─ GET /api/commands  ──▶ commands 查詢（自動加裝置條件）
                   ├─ /api/users/*       ──▶ 帳號 / token / 裝置授權 / 通知訂閱（admin 或本人）
                   └─ /api/notifications ──▶ 通知範本（admin 維護，任何帳號可讀）
```

## 目錄

| 路徑                             | 說明                                                                                                                                       |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------ |
| `internal/model`                 | 統一資料模型：`Position` / `Network` / `CellTower` / `Command` / `User` / `Notification`                                                    |
| `internal/auth`                  | 權限模型：scope、`Principal`、token 產生 / SHA-256 雜湊、密碼 bcrypt 雜湊                                                                   |
| `internal/codec`                 | 協議介面：`FrameDecoder`、`Decoder`、`SessionDecoder`、`Encoder`、`Base`                                                                   |
| `internal/helper`                | Java helper 的移植：`Buffer`(Netty ByteBuf)、`PatternBuilder`、`Parser`、BCD、DateBuilder、CRC16/X25、CRC16/IBM、XOR、Luhn                 |
| `internal/protocol/h02`          | H02 / TK102 系                                                                                                                             |
| `internal/protocol/gt06`         | GT06 / Concox / Jimi 系                                                                                                                    |
| `internal/protocol/jt808`        | JT/T 808（中國交通部標準）                                                                                                                 |
| `internal/protocol/teltonika`    | Teltonika FMxxx / FMB（Codec 8 / 8E / 12 / 13）                                                                                            |
| `internal/protocol/protocaltest` | 共用測試斷言（`ProtocolTest` 的移植）                                                                                                      |
| `internal/gateway`               | TCP 網關、協議註冊表、裝置目錄、位置快取、Sink 介面、連線表與命令路由                                                                      |
| `internal/geofence`              | 圍欄幾何：Traccar WKT（`POLYGON` / `CIRCLE`）→ 內含判定（含反子午線）                                                                      |
| `internal/alert`                 | 告警規則（overspeed / powerCut / geofenceEnter / geofenceExit）+ 非阻塞寫入管線                                                            |
| `internal/notify`                | 告警通知：webhook / SMTP email、訊息格式化、裝置 attributes 覆寫、使用者訂閱路由、非阻塞傳送佇列                                                            |
| `internal/api`                   | REST：`GET/POST/PUT/DELETE /api/devices`、`GET /api/positions`、`GET /api/alerts`、`GET /api/health`、`POST /api/login`、`GET /metrics`、`GET/POST /api/commands`、`/api/users`（帳號 / token / 裝置授權 / 通知訂閱）、`/api/notifications`；認證 / scope / 裝置擁有權 |
| `internal/metrics`               | Prometheus 文字格式的計數器 / gauge / histogram（零依賴）                                                                                  |
| `internal/storage/postgres`      | PostgreSQL schema/migration、`DeviceDirectory` 與批次 `Sink`                                                                               |
| `cmd/traccar-gateway`            | 啟動四個監聽埠與 REST 的 CLI                                                                                                               |

## 快速開始

```bash
cd go_server

# 1) 只跑網關，position 以 JSON 印到 log（不需要資料庫）
go run ./cmd/traccar-gateway

# 2) 接 PostgreSQL（自動建表 / 套用 migration）
docker run --rm -d --name traccar-pg -p 5432:5432 -e POSTGRES_PASSWORD=traccar postgres:16
go run ./cmd/traccar-gateway \
  -dsn 'postgres://postgres:traccar@localhost:5432/postgres?sslmode=disable'
```

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

其他 flag：`-batch-size`（預設 500）、`-batch-interval`（預設 1s）、`-dsn`（空 = 記憶體模式）、
`-overspeed-knots`（預設 0 = 關閉規則）、`-power-cut-volts`（預設 0 = 關閉規則）、
`-overspeed-prefer-lowest`（多個圍欄重疊時取最低限速；預設取最高）、
`-alert-repeat`（預設 0 = 不抑制重複告警）、`-geofence-refresh`（預設 5m，0 = 只在啟動時載入一次）、
`-device-refresh`（預設 5m，0 = 只在啟動時載入一次；每裝置門檻用的 attributes）、
`-api-key`（預設讀 `TRACCAR_API_KEY`；空 = 只用資料庫 token 認證）、
`-issue-token` / `-token-scopes` / `-token-ttl`（發 token 後結束，需要 `-dsn`）、
`-login-max-failures`（預設 5，0 = 關閉登入失敗鎖定）與 `-login-lock-duration`（預設 15m）、
`-allow-anonymous`（關閉 REST 認證，只給本機開發用）、
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
- 待補：`0x94` 子類型（ADC/告警字串）、WiFi 掃描（`0x17/0x2C/0x33/0xA2`）、
  odometer/driver 的車型分支、`0xA3` variant 判定、ADAS/DMS。

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

- `devices(id, unique_id, name, model, protocol, attributes, last_update, created_at)`
- `positions(id, device_id, protocol, device_time, server_time, valid, latitude, longitude, altitude, speed, course, accuracy, attributes)`
- `alerts(id, device_id, position_id, type, alert_time, geofence_id, attributes)`（`0002_alerts.sql`）
- `geofences(id, name, area, attributes, created_at)`（`0002_alerts.sql`）
- `users(id, email, name, admin, disabled, created_at, password_hash)`（`0003_users.sql`；`disabled` 由
  `0004_users_disabled.sql` 補上，`password_hash` 由 `0006_users_password.sql` 補上，預設 NULL =
  只能用 token 登入）
- `user_devices(user_id, device_id)`（`0003_users.sql`，多對多）
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
| 復電            | 同一裝置上一個 `power` 低於門檻、這一筆回到門檻以上                        | `powerRestored`                  |
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
目前沒有圍欄管理 API，圍欄以 SQL 維護，`-geofence-refresh` 週期性地重新載入。
圍欄對**所有裝置**生效（Traccar 的 user/device 權限表還沒移植）。

查詢：

```bash
curl 'localhost:8082/api/alerts?deviceId=7&type=deviceOverspeed,powerCut&limit=20'
```

```json
{
  "alerts": [
    {
      "id": 1,
      "deviceId": 7,
      "type": "deviceOverspeed",
      "alertTime": "2026-10-31T07:32:10Z",
      "attributes": { "limit": 30, "protocol": "gt06", "speed": 31.5 }
    }
  ],
  "count": 1
}
```

參數：`deviceId`、`type`（可重複或逗號分隔）、`from` / `to`（RFC3339 或 epoch 毫秒）、
`limit`（1–1000，預設 100）、`offset`。預設依 `alert_time DESC, id DESC` 排序。

已知取捨：

- **`alerts.position_id` 目前一律是 NULL**。`BatchSink` 用 multi-row `INSERT`，拿不到 position id；
  需要精確關聯時用 `alerts.device_id` + `alert_time` 去 join `positions`，或之後改成
  `INSERT ... RETURNING`（會犧牲批次吞吐）。告警的 `attributes` 已內含 `latitude` / `longitude` /
  `speed` / `protocol` 快照，單看 `alerts` 表也夠用。
- **去重與圍欄狀態都在記憶體**：重啟後去重視窗重新計時、圍欄只做「播種」。
- **門檻 / 圍欄變更最多延遲一個 refresh 週期**：`devices.attributes` 依 `-device-refresh`、
  `geofences` 依 `-geofence-refresh` 重新載入（兩者預設 5m）；設 `0` 表示只在啟動時載入一次。
  調整 SQL 後若要立刻生效，重啟程式即可。
- **圍欄門檻只影響超速**：斷電是裝置狀態，只有全域與 per-device 兩層。
- **去重視窗比的是伺服器時間**，不是裝置時間；設備一次補傳大量歷史資料時只會留下第一筆
  （這正是避免告警風暴的目的，但會看不到歷史區間內的每一筆）。
- 圍欄狀態存在記憶體，重啟後每個裝置的第一筆 position 只做「播種」，不會補發 enter / exit。

## 通知管道

告警除了寫進 `alerts` 表，還可以推到 webhook 與 email。兩個管道共用同一份格式化內容：
`alert.Pipeline` 每產生一筆告警就丟進 `notify.Dispatcher` 的佇列，傳送在背景 goroutine 進行，
device 連線與資料庫寫入都不等網路。沒有設定任何 `-notify-*` 時整條路徑不會啟動；
通知需要 `-dsn`（告警規則本身就要資料庫），記憶體模式只會印出「ignoring -notify-* flags」。

路由分三層，依序決定：

1. `-notify-types` 先決定伺服器要不要送這個告警類型。
2. 裝置的 `notifyTypes` 再收窄；裝置的 `notifyEmail` / `notifyWebhook` 是該管道的**絕對覆寫**，
   設了就不會再加上任何使用者訂閱。
3. 沒有裝置覆寫時，若這台裝置的這筆告警有帳號訂閱，**訂閱者決定哪些管道要送**；
   完全沒有訂閱者時才用伺服器 flag 的預設收件者。

伺服器層級設定：

| Flag                                                | 說明                                                                                                       |
| --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `-notify-webhook`                                   | webhook URL；空 = 關閉 webhook                                                                             |
| `-notify-webhook-authorization`                     | 每個請求帶的完整 `Authorization` 值；空 = 不帶                                                             |
| `-notify-email-smtp`                                | SMTP `host:port`；空 = 關閉 email                                                                          |
| `-notify-email-from`                                | 寄件者；`-notify-email-smtp` 有值時必填                                                                    |
| `-notify-email-to`                                  | 預設收件者（逗號分隔）；空 = 只用裝置的 `notifyEmail`                                                      |
| `-notify-email-username` / `-notify-email-password` | SMTP AUTH；密碼預設讀 `TRACCAR_SMTP_PASSWORD`，使用者名稱空 = 不做 AUTH                                    |
| `-notify-email-tls`                                 | `auto`（預設：465 用 implicit TLS，其餘伺服器有 STARTTLS 就升級）、`starttls`、`implicit`、`none`           |
| `-notify-types`                                     | 只送這些告警類型（逗號分隔）；空 = 全部；`none` = 全關                                                     |
| `-notify-timeout`                                   | 單次裝置查詢 / 傳送的上限，預設 10s                                                                        |
| `-notify-queue`                                     | 等待傳送的告警上限，預設 256；滿了就丟棄並計數                                                             |

每個裝置可以用 `devices.attributes` 覆寫（與門檻共用 `-device-refresh` 的更新週期）：

| Attribute       | 說明                                                                             |
| --------------- | -------------------------------------------------------------------------------- |
| `notifyTypes`   | 這個裝置要送的類型（逗號分隔或 JSON 陣列）；`none` = 全關，`all` / `*` = 全部     |
| `notifyWebhook` | 覆寫 webhook URL（可多個）；`false` 或空字串 = 這個裝置不送 webhook               |
| `notifyEmail`   | 覆寫收件者（可多個）；`false` 或空字串 = 這個裝置不寄 email                       |

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
帳號只會收到 `user_devices` 有授權的裝置的告警；`users.disabled = true` 的帳號不送。
同一個帳號有多個符合的範本時，管道取聯集，同一個收件者只送一次。

- `notifications.type`：`deviceOverspeed` / `powerCut` / `powerRestored` / `geofenceEnter` /
  `geofenceExit`，或 `all` / `*`（全部類型）。
- `notifications.notificators`：`mail`（寄到帳號的 `users.email`，`email` 是別名）與
  `webhook`（打 `-notify-webhook` 的 URL）。至少一個，未知值會被 API 拒絕。

`mail` 的收件者固定是帳號 email；`webhook` 沒有 per-user URL，訂閱只決定「這台裝置的這種
告警要不要打伺服器 webhook」。兩個管道仍要先用 `-notify-email-smtp` / `-notify-webhook`
啟用，否則沒有 notifier 可以送。

| 路由                                                        | 說明                                                                 |
| ----------------------------------------------------------- | -------------------------------------------------------------------- |
| `GET /api/notifications`                                    | 列出所有範本（任何已驗證帳號）                                       |
| `POST /api/notifications`                                   | 建立範本（admin），body `{"type","notificators","description"}`      |
| `PUT /api/notifications/{id}`                               | 局部更新範本（admin），省略的欄位不動                                |
| `DELETE /api/notifications/{id}`                            | 刪除範本（admin），`user_notifications` 由 FK cascade                |
| `GET /api/users/{id}/notifications`                         | 列出帳號的訂閱（本人或 admin）                                       |
| `PUT /api/users/{id}/notifications/{notificationId}`        | 訂閱（本人或 admin；重複呼叫不會出錯）                               |
| `DELETE /api/users/{id}/notifications/{notificationId}`     | 取消訂閱（本人或 admin；原本沒訂閱也回 `204`）                       |

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

除了 `/api/health`、`POST /api/login` 與兩支密碼自助路由（`POST /api/password/forgot`、
`POST /api/password/reset`），REST 每個請求都要帶憑證，放在 `X-Api-Key` 或
`Authorization: Bearer <token>`（兩者等價）：

| 憑證         | 來源                                  | 權限                         |
| ------------ | ------------------------------------- | ---------------------------- |
| 共用 API key | `-api-key` / `TRACCAR_API_KEY`        | admin：所有 scope、所有裝置  |
| 使用者 token | `api_tokens` 表，用 `-issue-token` 發 | 依 `scopes` + `user_devices` |
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

| scope             | 端點                             |
| ----------------- | -------------------------------- |
| `read:positions`  | `GET /api/positions`             |
| `read:devices`    | `GET /api/devices`               |
| `read:alerts`     | `GET /api/alerts`                |
| `read:metrics`    | `GET /metrics`                   |
| `read:commands`   | `GET /api/commands`              |
| `write:devices`   | `POST/PUT/DELETE /api/devices`   |
| `write:commands`  | `POST /api/commands`             |
| `*`               | 全部（給機器用，等同舊 API key），另外是列出 / 撤銷自己 token 的必要條件 |

`*` 不等於 admin：帳號管理（建 / 改 / 刪帳號、發 token）仍然只認 `users.admin = true`，
所以帶 `*` 的 token 不會提權，只能管自己的 token。

### 使用者與裝置擁有權

`users.admin = true` 的使用者不受 scope 與裝置限制；其他使用者只看得到 `user_devices`
有授權的裝置。所有查詢（devices / positions / alerts）都會自動加上裝置條件，**沒有授權任何裝置
就回空結果，而不是全部**。修改 / 刪除裝置與下命令也要擁有該裝置，否則 `403`。
`users.disabled = true` 的帳號會立刻失效（token 查詢直接回 `401`），但 token 與授權都保留，
之後重新啟用就照舊可用。

非 admin token 用 `POST /api/devices` 建檔時，會自動寫入 `user_devices`，建立者立刻看得到；
admin key 建的裝置不會自動綁定，要另外授權（見下節）。

### 帳號管理 API

帳號相關路由全部需要 **admin 憑證**（`-api-key`，或 `users.admin = true` 的使用者 token），
不看 scope：即使 token 帶 `*` 也不能建 / 改 / 刪帳號或發 token，避免提權。沒有配置 `-dsn` 時這些
路由回 `501`；`-allow-anonymous` 時也會一併開放，只適合本機。
三處例外，都只碰「自己的」資料：
token 的列出與撤銷對帳號本人開放，但本人手上那組 token 必須帶 `*`（見「密碼登入」）；
通知訂閱的 `/api/users/{id}/notifications/...` 對帳號本人開放，不看 scope（見「使用者訂閱」）；
`PUT /api/users/me/password` 由帳號本人改自己的密碼（見「密碼登入」）。

| 路由                                       | 說明                                                               |
| ------------------------------------------ | ------------------------------------------------------------------ |
| `GET /api/users`                           | 列出帳號（`limit` 1–1000、`offset`），含 `deviceCount` / `tokenCount` |
| `POST /api/users`                          | 建立帳號，body `{"email","name","admin"}`；email 重複回 `409`      |
| `PUT /api/users/{id}`                      | 局部更新 `name` / `admin` / `disabled` / `password`，省略的欄位不動    |
| `DELETE /api/users/{id}`                   | 刪除帳號（token 與裝置授權 cascade）                               |
| `GET /api/users/{id}/devices`              | 列出已授權的裝置（格式同 `GET /api/devices`）                      |
| `PUT /api/users/{id}/devices/{deviceId}`   | 授權裝置（重複呼叫不會出錯）                                       |
| `DELETE /api/users/{id}/devices/{deviceId}` | 撤銷授權（原本就沒有也回 `204`）                                  |
| `GET /api/users/{id}/tokens`               | 列出 token 中繼資料，**不含**明文與雜湊（admin，或本人帶 `*` 的 token） |
| `POST /api/users/{id}/tokens`              | 發 token，body `{"scopes":[...],"expiresIn":"720h"}`，明文只回這一次（僅 admin） |
| `DELETE /api/users/{id}/tokens/{tokenId}`  | 撤銷 token（admin，或本人帶 `*` 的 token）                          |
| `PUT /api/users/me/password`               | 本人改密碼，body `{"currentPassword","newPassword"}`（需要使用者 token，共用 key 不行） |
| `POST /api/password/forgot`                | 寄出重設碼，body `{"email"}`；不論帳號是否存在都回 `202`（免憑證）  |
| `POST /api/password/reset`                 | 用重設碼換新密碼，body `{"token","password"}`；用過即失效（免憑證） |
| `GET /api/users/{id}/notifications`        | 列出通知訂閱（本人或 admin）                                       |
| `PUT /api/users/{id}/notifications/{notificationId}` | 訂閱通知範本（本人或 admin）                              |
| `DELETE /api/users/{id}/notifications/{notificationId}` | 取消訂閱（本人或 admin）                               |

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

參數：`protocol`（精確比對）、`limit`（1–1000，預設 100）、`offset`。
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

# 刪除：positions / alerts 由 FK CASCADE 一併刪除
curl -i -X DELETE localhost:8082/api/devices/7
# 204；不存在回 404
```

非 admin token 需要 `write:devices`，而且只能修改 / 刪除 `user_devices` 內的裝置，否則回 403；
`POST /api/devices` 建檔後會自動把新裝置綁給該 token 的使用者。

`uniqueId`、`name`、`model`、`protocol` 兩端都會 trim；空白的 `uniqueId` 回 400。
id 不是正整數回 400。`attributes` 目前只能以 SQL 維護。

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

參數與告警查詢一致：`deviceId`、`from` / `to`（RFC3339 或 epoch 毫秒）、`limit`（1–1000，預設 100，
超過會回 400）、`offset`。`attributes` 永遠是物件（沒有額外欄位時是 `{}`，不是 `null`）。

## 命令下發

`POST /api/commands` 把命令編碼成該協議的位元組，先落一列 `commands`，再寫進裝置目前那條 TCP
連線（需要 `-dsn`，因為要靠 `devices.protocol` 選 encoder 與寫紀錄）：

```bash
curl -i -X POST localhost:8082/api/commands \
  -H 'Content-Type: application/json' \
  -d '{"deviceId":7,"type":"engineStop","attributes":{}}'

# HTTP/1.1 202 Accepted
# {"id":41,"deviceId":7,"type":"engineStop","protocol":"gt06","queued":true}
```

| 狀態  | 意義                                                                 |
| ----- | -------------------------------------------------------------------- |
| `202` | 已記錄並寫入設備連線                                                 |
| `400` | body 不合法，或該協議不支援這個命令（都不會留下 `commands` 列）      |
| `403` | 缺少 `write:commands`，或該裝置不在 `user_devices` 內                 |
| `404` | 裝置不存在                                                           |
| `409` | 裝置離線，或 `devices.protocol` 還是 NULL（讓設備重連一次）          |
| `501` | 沒有配置命令路由                                                     |

各協議支援的命令以 encoder 為準：H02 `alarmArm` / `alarmDisarm` / `engineStop` / `engineResume` /
`positionPeriodic`；GT06 `engineStop` / `engineResume` / `custom`；JT808 `positionSingle` /
`positionStop` / `rebootDevice` / `positionPeriodic` / `alarmArm` / `alarmDisarm` / `alarmDismiss` /
`engineStop` / `engineResume` / `custom`；Teltonika `engineStop` / `engineResume` / `custom`。
`custom` 的內容放 `attributes.data`（JT808 需要十六進位字串）。

寫入是序列化的：解碼執行緒的 ACK 與 REST 的命令共用同一把連線鎖，不會把兩個幀交錯。
同一裝置重新登入時舊連線會被關閉，命令只送到最新那條。

裝置與連線的綁定時機：GT06 / JT808 / Teltonika 在握手（登入 / IMEI 封包）時綁定；
H02 沒有握手，第一筆能解析出裝置的訊息就會綁定。因此對「從未上報過」或「尚未登入」的裝置
下命令會得到 `409`。

### 命令狀態

每一筆命令在 `commands` 表裡走四種狀態之一：

| status    | 何時                                                        | `result_code` |
| --------- | ----------------------------------------------------------- | ------------- |
| `pending` | 已記錄，還沒寫進連線                                        | NULL          |
| `sent`    | 已寫進連線，等裝置回應                                      | NULL          |
| `done`    | 裝置回應且結果是 0（GT06 只要有回應就算）                   | 0             |
| `failed`  | 離線 / 寫入失敗，或裝置回報非 0                             | 失敗時是 NULL；裝置拒絕時是它回的值 |

`GET /api/commands` 列出命令，參數是 `deviceId`、`limit`（1–1000，預設 100）與 `offset`，
排序是 `id DESC`。需要 `read:commands`；非 admin 只看得到自己授權裝置的命令：

```bash
curl -s 'localhost:8082/api/commands?deviceId=7&limit=2' -H 'Authorization: Bearer trc_...'
# {"commands":[{"id":41,"deviceId":7,"type":"engineStop","attributes":{},"status":"done",
#               "resultCode":0,"createdAt":"2026-11-01T01:02:03.123Z",
#               "sentAt":"2026-11-01T01:02:03.130Z","doneAt":"2026-11-01T01:02:05.402Z"}],
#  "count":1}
```

`resultCode` / `sentAt` / `doneAt` 沒有值時不會出現。用 `psql -c 'select * from commands'`
看到的內容與這個端點一致。

### 回應配對

JT808 終端用 `0x0001` 終端一般回應回答下行訊息，payload 是「被回應的流水號、被回應的訊息 ID、
結果碼」；GT06 用 `0x80` / `0x81` / `0x82` 命令回應回答，沒有結果碼，有回應就算成功。這兩個
回應都不會被回覆，否則裝置會收到「回應的回應」。

兩者都沒有可以對回請求的識別碼 — JT808 encoder 一律送流水號 0，GT06 回應裡的是該幀自己的序號 —
所以 gateway 收到回應時，把**該裝置最新一列 `sent`** 收成 `done` 或 `failed`。連續送出時同一個
裝置可能有好幾列在等回應，一次回應只收掉最新那列；若前一筆一直沒回應，後送的那筆被回應時，
被蓋掉的是後送的那列（見「已知限制」）。

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

| 來源                     | 內容                                                                                                                                                                 |
| ------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `gateway.Server` log     | `<protocol>: listening on <addr>`、`<protocol>: decode: ...`、`<protocol>: read: ...`、`<protocol>: frame buffer exceeded N bytes`、`<protocol>: acknowledge command: <err>`（裝置回應已到但 `commands` 寫不回去，連線不受影響） |
| `postgres.BatchSink` log | `postgres: batch write (<n> positions): <err>`                                                                                                                       |
| `alert.Pipeline` log     | `alert: write (<n> alerts): <err>`、`alert: queue full, dropped <type> for device <id>`                                                                              |
| `alert` 啟動 log         | `alerts: load geofences: ...`、`alerts: reload geofences: ...`（含單一圍欄解析失敗）、`alerts: load device attributes: ...`、`alerts: reload device attributes: ...` |
| `notify.Dispatcher` log  | `notify: webhook, mail enabled`、`notify: <channel>: <err>`、`notify: lookup device <id>: <err>`、`notify: lookup subscriptions for device <id>: <err>`、`notify: queue full, dropped <type> for device <id>` |
| `api.Server` log         | `api: listening on <addr>`、`api: lookup token: <err>`、`api: query alerts: <err>`、`api: query positions: <err>`、`api: query devices: <err>`、`api: query commands: <err>`、`api: record command <type> for device <id>: <err>`、`api: send command <type> to device <id>: <err>` |
| CLI 關閉時               | `dropped <n> positions because the queue was full`、`dropped <n> alerts because the queue was full`、`dropped <n> notifications because the queue was full`          |
| `GET /api/health`        | `200 {"status":"ok","database":"ok"}`；資料庫 ping 失敗回 `503 {"status":"unavailable","database":"unavailable"}`（driver 錯誤只進 log，不外洩）                     |
| 記憶體模式 sink          | 每筆 position 一行 JSON（`log.Printf("position %s", ...)`）                                                                                                          |

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
  `/api/health`、`/api/login` 與密碼自助路由以外的請求都會回 401。第一次使用先帶 `-api-key` 啟動，
  用 `POST /api/users` 建立管理員並發 token（見「帳號管理 API」），或直接用 `-issue-token`；
  `-allow-anonymous` 只適合本機。對外仍建議加 TLS / 反代。
- 帳號 API 只有分頁，沒有搜尋／排序；email 建立後不可修改。第一組密碼只能由 admin 設定；
  本人改密碼、忘記密碼（email 重設碼）與登入失敗鎖定見「密碼登入」。
  鎖定是 per-account：知道 email 的人可以故意打錯密碼把該帳號鎖 15 分鐘，這是防暴力破解換來的
  拒絕服務面。忘記密碼端點沒有 IP 速率限制，只有每帳號一分鐘的節流；重設信能不能寄出取決於
  SMTP，寄失敗只留在 log。bcrypt cost 12 讓一次登入約 0.6 秒（見「密碼登入」）。
  每個請求都會查一次 token 與裝置清單，撤銷立即生效但也多一次 DB round trip。
- 裝置的 `attributes` 只能以 SQL 維護；`PUT` 只改 `name` / `model`，`protocol` 建立後不可改。
  裝置仍會由登入自動建立（`-auto-register`），與 `POST /api/devices` 併用。
- 命令會落地（`commands` 表）並依裝置回應收尾，但配對規則是「該裝置最新一列 `sent`」：
  JT808 的終端一般回應與 GT06 的 0x80 都沒有能對回請求的識別碼，連續送出多筆時，回應只會收掉
  最新那一列，沒被回應的留在 `sent`。沒有重送、沒有派送佇列：`pending` 卡住代表
  gateway 在寫進連線前就掛了，`sent` 卡住代表裝置沒回，兩者都只能人工判讀。`devices.protocol`
  為 NULL 的舊裝置會回 `409`，讓它重連一次即可。
- 告警只有超速 / 斷電 / 復電 / 圍欄四類；通知只有 webhook 與 email 兩種。使用者訂閱只決定
  類型與管道：email 收件者是帳號 email，webhook 仍共用 `-notify-webhook` 的 URL。門檻可用
  裝置 / 圍欄 attributes 覆寫，但只能以 SQL 維護。
- 圍欄只能以 SQL 維護，且對所有裝置生效；`alerts.position_id` 目前是 NULL（見上節）。
- `devices.model` 會被讀取，但只在解讀告警碼時。GT06 的 `LW*` / `SEEWORLD` / `VL103` `LL303` `VL512` `G18` 與 JT808 的 `G-360P` `G-508P` `AL300` `GL100` `VL300` 各走自己的分支，其餘照通用規則；比對前統一轉大寫。
  model 在裝置登入解析 session 時讀出並跟著連線走，所以改 model 要重連才生效。GT06 `NT_MODELS` 的 LBS2 / DRIVER 版面差異（Java 的 `modelNT`）還沒開，它改的是整段訊息佈局，不是只換告警名稱。
- 報表等其餘業務層仍是 Java 版負責。
- 記憶體目錄僅適合開發；正式環境請用 `-dsn`。

## 下一步

1. 命令的可靠性：重送與派送佇列、以 JT808 流水號配對（encoder 要改成每筆一個號碼）、
   `commands` 的保留期限與清理。
2. GT06 `NT_MODELS` 的 LBS2 / DRIVER 版面差異（Java 的 `modelNT`），計畫附錄 C 記錄了為什麼延後。
