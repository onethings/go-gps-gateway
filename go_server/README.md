# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的主力設備協議（H02 / GT06 / JT808 / Teltonika），
只做「解碼 → 標準 Position → 落地」，網路層與業務層之後再分段替換。

- 母本：`../traccar-master`（Java），每個 Go 檔案的註解都標明了對應的 Java 類別。
- 設計原則：協議層是**純函式**（`[]byte` → `*model.Position`），所有 I/O 集中在 `internal/gateway`。
- 現況：四個協議的 codec + TCP 網關 + PostgreSQL 落地 + 告警規則 / 地理圍欄
  + REST（裝置查詢、位置查詢、告警查詢、健康檢查、命令下發）全部可編譯、`go test ./...` 全綠。

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
                        └─ postgres.BatchSink  positions 批次落地

client ──HTTP──▶ api.Server
                   ├─ GET /api/devices  ──▶ devices 查詢
                   ├─ GET /api/positions ──▶ positions 查詢
                   ├─ GET /api/alerts   ──▶ alerts 查詢
                   ├─ GET /api/health
                   └─ POST /api/commands ──▶ gateway.Router ──▶ 該協議 encoder ──▶ 設備連線
```

## 目錄

| 路徑 | 說明 |
|---|---|
| `internal/model` | 統一資料模型：`Position` / `Network` / `CellTower` / `Command` |
| `internal/codec` | 協議介面：`FrameDecoder`、`Decoder`、`SessionDecoder`、`Encoder`、`Base` |
| `internal/helper` | Java helper 的移植：`Buffer`(Netty ByteBuf)、`PatternBuilder`、`Parser`、BCD、DateBuilder、CRC16/X25、CRC16/IBM、XOR、Luhn |
| `internal/protocol/h02` | H02 / TK102 系 |
| `internal/protocol/gt06` | GT06 / Concox / Jimi 系 |
| `internal/protocol/jt808` | JT/T 808（中國交通部標準） |
| `internal/protocol/teltonika` | Teltonika FMxxx / FMB（Codec 8 / 8E / 12 / 13） |
| `internal/protocol/protocaltest` | 共用測試斷言（`ProtocolTest` 的移植） |
| `internal/gateway` | TCP 網關、協議註冊表、裝置目錄、位置快取、Sink 介面、連線表與命令路由 |
| `internal/geofence` | 圍欄幾何：Traccar WKT（`POLYGON` / `CIRCLE`）→ 內含判定（含反子午線） |
| `internal/alert` | 告警規則（overspeed / powerCut / geofenceEnter / geofenceExit）+ 非阻塞寫入管線 |
| `internal/api` | REST：`GET /api/devices`、`GET /api/positions`、`GET /api/alerts`、`GET /api/health`、`POST /api/commands` |
| `internal/storage/postgres` | PostgreSQL schema/migration、`DeviceDirectory` 與批次 `Sink` |
| `cmd/traccar-gateway` | 啟動四個監聽埠與 REST 的 CLI |

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

預設監聽（可用同名 flag 覆寫，傳空字串可關閉該協議）：

| 協議 / 服務 | Flag | 預設位址 |
|---|---|---|
| H02 | `-h02` | `0.0.0.0:5013` |
| GT06 | `-gt06` | `0.0.0.0:5023` |
| JT808 | `-jt808` | `0.0.0.0:8610` |
| Teltonika | `-teltonika` | `0.0.0.0:5027` |
| REST | `-http` | `0.0.0.0:8082`（需要 `-dsn`；空 = 關閉） |

其他 flag：`-batch-size`（預設 500）、`-batch-interval`（預設 1s）、`-dsn`（空 = 記憶體模式）、
`-overspeed-knots`（預設 0 = 關閉規則）、`-power-cut-volts`（預設 0 = 關閉規則）、
`-alert-repeat`（預設 0 = 不抑制重複告警）、`-geofence-refresh`（預設 5m，0 = 只在啟動時載入一次）、
`-api-key`（預設讀 `TRACCAR_API_KEY`；空 = 關閉認證）。

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
- `schema_migrations(version, applied_at)`

寫入路徑：`BatchSink`（非阻塞佇列 + 每批一個 multi-row `INSERT`）。
佇列滿時 `Handle` 回傳 `ErrSinkFull` 並累計 `Dropped()`，不會拖慢 device 連線。

`devices.protocol` 在設備首次登入（`Register`）時寫入，供 `POST /api/commands` 選擇 encoder；
已存在且協議為 NULL 的舊資料會在下一次登入時補上（`COALESCE`，不會覆寫已知值）。
`devices.last_update` 由 `InsertPositions` 在每批寫入後以單一
`UPDATE ... FROM jsonb_to_recordset(...)` 推進，只前進不倒退（補傳舊資料不會讓它倒退）。

## 告警與地理圍欄

`alert.Pipeline` 包在 `BatchSink` 前面，每個 position 先過規則再進批次寫入：

| 規則 | 觸發條件 | `type` |
|---|---|---|
| 超速 | `Speed`（**節**，decoder 已從 km/h 換算）高於 `-overspeed-knots` | `deviceOverspeed` |
| 斷電 | `attributes.power`（**伏特**，GT06 `0x94` 是 12.5）低於 `-power-cut-volts` | `powerCut` |
| 復電 | 同一裝置上一個 `power` 低於門檻、這一筆回到門檻以上 | `powerRestored` |
| 進入 / 離開圍欄 | `geofences` 表的幾何內含判定變化（每個裝置各自記狀態） | `geofenceEnter` / `geofenceExit` |

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
{"alerts":[{"id":1,"deviceId":7,"type":"deviceOverspeed","alertTime":"2026-10-31T07:32:10Z",
            "attributes":{"limit":30,"protocol":"gt06","speed":31.5}}],"count":1}
```

參數：`deviceId`、`type`（可重複或逗號分隔）、`from` / `to`（RFC3339 或 epoch 毫秒）、
`limit`（1–1000，預設 100）、`offset`。預設依 `alert_time DESC, id DESC` 排序。

已知取捨：

- **`alerts.position_id` 目前一律是 NULL**。`BatchSink` 用 multi-row `INSERT`，拿不到 position id；
  需要精確關聯時用 `alerts.device_id` + `alert_time` 去 join `positions`，或之後改成
  `INSERT ... RETURNING`（會犧牲批次吞吐）。告警的 `attributes` 已內含 `latitude` / `longitude` /
  `speed` / `protocol` 快照，單看 `alerts` 表也夠用。
- **門檻是全域的**：`-overspeed-knots` / `-power-cut-volts` 對所有裝置生效，
  Java 版的 per-device 門檻還沒移植。
- **去重與圍欄狀態都在記憶體**：重啟後去重視窗重新計時、圍欄只做「播種」。
- **去重視窗比的是伺服器時間**，不是裝置時間；設備一次補傳大量歷史資料時只會留下第一筆
  （這正是避免告警風暴的目的，但會看不到歷史區間內的每一筆）。
- 圍欄狀態存在記憶體，重啟後每個裝置的第一筆 position 只做「播種」，不會補發 enter / exit。

## 認證

設 `-api-key`（預設取環境變數 `TRACCAR_API_KEY`）之後，除了 `/api/health` 以外的端點都要帶 key：

```bash
TRACCAR_API_KEY='please-change-me' go run ./cmd/traccar-gateway -dsn '******localhost:5432/postgres?sslmode=disable'

curl -H 'X-Api-Key: please-change-me' 'localhost:8082/api/positions'
curl -H 'Authorization: Bearer please-change-me' 'localhost:8082/api/alerts'
```

兩者等價，key 以常數時間比較。缺 key 或 key 不對回 `401`，並帶 `WWW-Authenticate: Bearer`。
沒設 key 時 REST 完全開放，啟動會印 `api: no -api-key set, REST is unauthenticated`。
優先用環境變數，key 才不會出現在 `ps` 的參數列。

下面 REST 範例省略了這個 header。

## 裝置查詢

`GET /api/devices` 讀 `devices` 表，預設依 `last_update DESC NULLS LAST, id DESC`：

```bash
curl 'localhost:8082/api/devices?protocol=gt06&limit=20'
```

```json
{"devices":[{"id":7,"uniqueId":"864717003283581","name":"","protocol":"gt06",
             "attributes":{},"lastUpdate":"2026-10-31T07:32:10Z","createdAt":"2026-10-31T06:00:00Z"}],"count":1}
```

參數：`protocol`（精確比對）、`limit`（1–1000，預設 100）、`offset`。
`lastUpdate` 只在裝置至少上報過一筆位置時出現（不會是 `0001-01-01`），`model` 為空時同樣省略；
`lastUpdate` 取的是**裝置時間**的最大值。

## 位置查詢

`GET /api/positions` 讀 `positions` 表，`from` / `to` 比對的是**裝置時間**（`device_time`），
排序固定 `device_time DESC, id DESC`：

```bash
curl 'localhost:8082/api/positions?deviceId=7&limit=2'
```

```json
{"positions":[{"id":42,"deviceId":7,"protocol":"gt06","deviceTime":"2026-10-31T07:32:10Z",
               "serverTime":"2026-10-31T07:32:11Z","valid":true,"latitude":54.943,"longitude":25.206,
               "altitude":100,"speed":27,"course":180,"accuracy":5,"attributes":{"sat":9}}],"count":1}
```

參數與告警查詢一致：`deviceId`、`from` / `to`（RFC3339 或 epoch 毫秒）、`limit`（1–1000，預設 100，
超過會回 400）、`offset`。`attributes` 永遠是物件（沒有額外欄位時是 `{}`，不是 `null`）。

## 命令下發

`POST /api/commands` 把命令編碼成該協議的位元組，直接寫進裝置目前那條 TCP 連線
（需要 `-dsn`，因為要靠 `devices.protocol` 選 encoder）：

```bash
curl -i -X POST localhost:8082/api/commands \
  -H 'Content-Type: application/json' \
  -d '{"deviceId":7,"type":"engineStop","attributes":{}}'

# HTTP/1.1 202 Accepted
# {"deviceId":7,"type":"engineStop","protocol":"gt06","queued":true}
```

| 狀態 | 意義 |
|---|---|
| `202` | 已寫入設備連線 |
| `400` | body 不合法，或該協議不支援這個命令 |
| `404` | 裝置不存在 |
| `409` | 裝置離線，或 `devices.protocol` 還是 NULL（讓設備重連一次） |
| `501` | 沒有配置命令路由 |

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

## 開發

```bash
go build ./...
go vet ./...
go test ./...                 # 全部（含 gateway 的 net.Pipe 端到端）
go test ./internal/protocol/gt06 -run TestDecodeGPS -v
go test ./internal/geofence ./internal/alert ./internal/api -v
go test -race ./internal/gateway ./internal/alert ./internal/api
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

資料庫整合測試（沒設環境變數會 skip）：

```bash
TRACCAR_POSTGRES_DSN='postgres://postgres:traccar@localhost:5432/postgres?sslmode=disable' \
  go test ./internal/storage/postgres -run 'TestStoreIntegration|TestAlertStoreIntegration' -v
```

## 監控（開發期）

目前可觀察的輸出：

| 來源 | 內容 |
|---|---|
| `gateway.Server` log | `<protocol>: listening on <addr>`、`<protocol>: decode: ...`、`<protocol>: read: ...`、`<protocol>: frame buffer exceeded N bytes` |
| `postgres.BatchSink` log | `postgres: batch write (<n> positions): <err>` |
| `alert.Pipeline` log | `alert: write (<n> alerts): <err>`、`alert: queue full, dropped <type> for device <id>` |
| `alert` 啟動 log | `alerts: load geofences: ...`、`alerts: reload geofences: ...`（含單一圍欄解析失敗） |
| `api.Server` log | `api: listening on <addr>`、`api: query alerts: <err>`、`api: query positions: <err>`、`api: query devices: <err>`、`api: send command <type> to device <id>: <err>` |
| CLI 關閉時 | `dropped <n> positions because the queue was full`、`dropped <n> alerts because the queue was full` |
| 記憶體模式 sink | 每筆 position 一行 JSON（`log.Printf("position %s", ...)`） |

排查順序建議：

1. **設備連上來了但沒有 position** → 看 `decode:` 或 `sink:` 訊息；
   GT06/Teltonika 若未完成握手，之後的資料幀會被忽略（log 不會報錯，這是協議行為）。
2. **同一台設備一直重連** → 通常是握手回包不對（Teltonika 回 `0x00`、JT808 未註冊成功），
   檢查 `DeviceDirectory` 是否有該 `unique_id`（`-dsn` 模式下看 `devices` 表）。
3. **position 掉資料** → 關閉前看 `dropped`，或把 `-batch-size` 調小 / `-batch-interval` 調短。
4. **有 position 但沒有告警** → 先確認 `-overspeed-knots` / `-power-cut-volts` 不是 0，
   再看規則的單位（節 / 伏特）；圍欄告警需要 `geofences` 表有資料且 `area` 能解析
   （解析失敗會印 `alerts: load geofences: ...`，該筆圍欄會被跳過，其他圍欄照常運作）。

尚未實作、下一輪要加的指標（建議接 `/metrics`）：

```
traccar_connections_active{protocol}
traccar_frames_total{protocol}
traccar_decode_errors_total{protocol}
traccar_positions_total{protocol}
traccar_position_dropped_total
traccar_db_write_seconds (histogram)
traccar_db_write_errors_total
traccar_alerts_total{type}
traccar_alert_dropped_total
```

## 已知限制

- REST 預設**沒有認證**：沒設 `-api-key`（或 `TRACCAR_API_KEY`）時，能連到 `-http` 位址的人
  就能讀全站告警 / 位置並下命令。對外請務必設 key，或綁內網 / 反代後面。
  只有 `/api/health` 免 key（給 probe 用）。
- 裝置只能靠 device 登入自動建立（`-auto-register` 邏輯寫在 gateway），還沒有新增 / 改名 / 刪除的 API。
- 命令只寫進設備連線，不等 ACK、不落地，也沒有 Java 版的 `commands` 表與派送佇列；
  `devices.protocol` 為 NULL 的舊裝置會回 `409`，讓它重連一次即可。
- 告警只有超速 / 斷電 / 復電 / 圍欄四類，沒有通知管道（email / webhook），也沒有 per-device 門檻。
- 圍欄只能以 SQL 維護，且對所有裝置生效；`alerts.position_id` 目前是 NULL（見上節）。
- `devices.model` 只寫不讀，車型相關的協議分支（GT06 `NT_MODELS`、JT808 門磁/撞擊）尚未啟用。
- 報表等其餘業務層仍是 Java 版負責。
- 記憶體目錄僅適合開發；正式環境請用 `-dsn`。

## 下一步

1. 授權：API key 只有「全有全無」，還沒有 Traccar 的 user / device 權限表；對外建議加 mTLS 或反代。
2. 裝置管理 API（新增 / 改名 / 刪除）與每裝置 / 每圍欄門檻。
3. 通知管道（email / webhook）。
4. 把 `device model` 注入 `codec.Base`，打開各協議被延後的車型分支。
5. 指標（`/metrics`）與更完整的健康檢查（含 DB ping）。
