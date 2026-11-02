# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的設備協議與伺服器：解碼設備幀 → 標準 `Position` → 落地（PostgreSQL），
並提供對齊母本的 REST API、告警 / 地理圍欄、通知與命令下發。

- 母本：`../traccar-master`（Java）。每個 Go 檔案的註解都標明對應的 Java 類別。
- 設計原則：協議層是**純函式**（`[]byte` → `*model.Position`），所有 I/O 集中在 `internal/gateway`。
- 需求：Go 1.22+。PostgreSQL 只在要跑 REST / 告警 / 命令歷史時才需要。

## 現況

- 10 個協議：H02、GT06、JT808、Teltonika、OsmAnd、MQTT、GPS103、TK103、Meitrack、Wondex。
  純函式 codec + TCP / UDP / TLS / HTTP / poll 五種傳輸基底。
- 處理鏈：`ProcessingHandler` 宣告 14 個階段（filter … media），已實作 `motion`、`business`，
  其餘為 placeholder。`business` = `alert.Pipeline`（規則評估 → events 與 position 同交易落地）→
  `notify.Dispatcher`（webhook / SMTP，背景傳送）；`terminal` = `postgres.BatchSink` 批次落地。
- REST API：裝置 / 位置 / 事件、帳號與 token、權限與分享、資源 CRUD、命令、WebSocket 推播、
  統計、媒體、認證與密碼自助，全部可編譯、`go test ./...` 幾乎全綠（唯一失敗見「測試」）。
- 連線狀態：`devices.status`（online / offline / unknown）、心跳逾時、`events` 表。

> **測試**：`go build ./...` 通過。`go test ./...` 目前有一個**既有失敗**：
> `internal/protocol/suntech` 的 `TestDecodeBinary`（二進位幀的經緯度縮放不符預期，lat 22.1274 ≠ 22.1234）。
> `suntech` / `ruptela` 尚未納入預設啟動清單，此失敗不影響網關。修復前「全綠」的說法不成立。

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
| `internal/storage/postgres`               | PostgreSQL store + 16 支 migration（pgx v5）                                               |
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

## 資料庫

`internal/storage/postgres/migrations/*.sql`（16 支）由 `Store.Migrate` 依版本套用，主要表：

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
| --------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------- |
| 事件      | `GET /api/events`、`GET /api/events/{id}`（`/api/alerts` 為舊名別名）                                                                                                 |
| 裝置      | `GET/POST /api/devices`、`GET/PUT/DELETE /api/devices/{id}`                                                                                                           |
| 位置      | `GET /api/positions`、`GET /api/positions/{kml,kmz,csv,gpx}`                                                                                                          |
| 命令      | `GET/POST /api/commands`（落 commands 表 → Router → encoder → 設備連線）、`GET /api/commands/send`                                                                    |
| 健康/指標 | `GET /api/health`、`GET /metrics`（Prometheus）                                                                                                                       |
| WebSocket | `GET /api/socket`（位置 / 事件 / 裝置狀態，token 走 query string）                                                                                                    |
| 帳號      | `GET/POST /api/users`、`PUT/DELETE /api/users/{id}`、`/api/users/{id}/devices`、`/api/users/{id}/tokens`                                                              |
| 認證      | `POST /api/login`、`POST /api/password/forgot`、`POST /api/password/reset`、`PUT /api/users/me/password`                                                              |
| 通知      | `GET/POST/PUT/DELETE /api/notifications`、`/api/users/{id}/notifications`、`GET /api/notificators`、`GET /api/notifications/types`、`POST /api/notifications/test     | send/{notificator}` |
| 權限/分享 | `/api/permissions`、`POST /api/share/device`、`POST /api/share/group`（`-disable-share` 可關）                                                                        |
| 資源 CRUD | `/api/groups`、`/api/geofences`、`/api/calendars`、`/api/drivers`、`/api/maintenance(s)`、`/api/orders`、`/api/reports`、`/api/attributes/computed`                   |
| 管理      | `/api/protocols`、`/api/server`（含 timezones / geocode / speedlimit / geolocation / gc / reboot / file）、`/api/statistics`、`/api/statistics/devices`、`/api/audit` |
| 會話      | `GET/POST/DELETE /api/session`、`/api/session/{id}`、`/api/session/token`、`/api/session/token/revoke`                                                                |
| 媒體      | `GET/POST /api/media`、`GET/DELETE /api/media/{id}`                                                                                                                   |

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
- `PUT /api/devices/{id}` 只改 `name` / `model` / `attributes`；`protocol` 建立後不可改，母本其餘
  欄位（phone / contact / category / disabled / expirationTime）尚未建模。
- 告警只有超速 / 斷電 / 復電 / 圍欄四類；通知管道以 webhook / email 為主力。群組授權只影響「看
  得到哪些裝置」，通知收件者仍只認 `user → device` 直接授權（未展開群組）。
- 分享帳號只能靠 admin 事後刪除撤銷（無到期自動清理）；`expiration` 只作用在 token 上。
- `/api/socket` 的 token 放 query string（瀏覽器無法對 WebSocket 設 header），會出現在存取紀錄裡。
- `suntech` / `ruptela` codec 已存在但未納入預設啟動清單；`suntech` 目前有未通過的測試（見「現況」）。
- 記憶體目錄（無 `-dsn`）僅適合開發；正式環境請用 PostgreSQL。

## Roadmap & 未完成事項

- [ ] **ProcessingHandler**：補齊 Filter（過濾漂移）、Geocoder（逆地理編碼）、EngineHours/Trips 計算。
- [ ] **協議層**：修復 `suntech` 縮放測試失敗；實作 Queclink 協議；擴充各協議命令下發 Encoder。
- [ ] **集群與擴展**：引進 Redis 分散式 Session 與 WebSocket 推播廣播，支援多網關節點水平擴展。
- [ ] **前端對接**：提供完整 WebSocket JSON 訂閱協定文檔。

----now check start---
我已經實際比對 traccar-master 與 go_server 的程式碼、路由與測試（commit 0e35d75，工作區乾淨）。先給結論，再給依據。

結論：完成度取決於「分母」
計算基準 完成度 說明
整個 traccar-master 功能對等（272 協議 + 全業務層） 約 40% 母本 272 支協議解碼器佔 Java 程式碼 66%，Go 只做了 12 支
單機核心營運系統（不含冷門協議、叢集、Web UI） 約 80% 網關、REST、事件、通知、報表、地理服務都可用
最初指定的四大協議目標（H02/GT06/JT808/Teltonika） 約 95% 解碼完成，命令下發仍有部分缺口
母本有 272 個協議解碼器、1,059 個 Java 主程式檔（118,398 行）；Go 版 277 個 .go 檔、非測試碼 40,575 行。
若只看協議廣度是 12/272 = 4.4%，但四大主力協議在實務部署中覆蓋率最高，所以「能不能上線」和「是否完全取代母本」是兩個差距很大的數字。

各模組完成度
模組 完成度 現況證據
協議解碼 4%（廣度）<br>主力 4 支 100% 12 個 codec：h02/gt06/jt808/teltonika + gps103/tk103/meitrack/wondex/osmand/mqtt/ruptela/suntech；protocol.go 只註冊 10 支
傳輸層 100% TCP / UDP / TLS / HTTP / poll 五種基底都有
處理鏈（ProcessingHandler） 約 70% 15 個階段中 9 個有實作；6 個仍是 placeholder（見下）
事件規則 約 90% 12 類事件幾乎都有：Alarm/Ignition/Behavior/Fuel/Driver/Maintenance/Media/CommandResult/Proximity + 超速/斷電/圍欄
REST API 約 85% 母本 openapi.yaml 76 條路徑，Go 覆蓋約 65 條
通知 約 90% webhook、SMTP、Telegram、Pushover、Firebase、WhatsApp、SMS、AWS SNS、Traccar Push
報表 約 90% route/trips/stops/summary/events/geofences/devices/combined + KML/KMZ/CSV/GPX/XLSX
外送 Forward 約 55% 只有 URL(/HTTP)、MQTT、Redis、Wialon；缺 AMQP、Kafka、Event forwarder、Network forwarder
地理服務 約 90% geocoder / geolocation / speedlimit / mapmatcher 都有
認證與權限 約 80% 密碼、token、scope、權限、分享；缺 TOTP、OpenID
叢集與水平擴展 約 10% 單節點；無 Redis session、無 WebSocket 廣播、無 multicast
資料庫 約 70% 只有 PostgreSQL、自有 schema；母本還支援 MySQL/H2，schema 不相容
還沒有完成的部分

1. 協議層（最大缺口）
   272 支母本協議中尚有 260 支未實作（Queclink、Meitrack 變體、Concox 系列等）。
   suntech、ruptela codec 存在但沒註冊進預設啟動清單；suntech 還有一個失敗測試。
   已支援協議的子功能缺漏（README 自述）：
   GT06：0x94 子類型、WiFi 掃描（0x17/0x2C/0x33/0xA2）、0xA3 variant、ADAS/DMS
   JT808：0x0704/0x0210 批次、0x5501/0x5502、0x0702 駕駛員卡、0x0104 參數、0x0900/0x8900 透傳、ADAS/DMS/胎壓/OBD
   Teltonika：Codec 7/16、BLE beacon、io78 driver id、圖片傳輸
   命令 Encoder 只有 5 支：h02 / gt06 / jt808 / teltonika / ruptela；gps103、tk103、meitrack、wondex 只能收不能下命令。
2. 處理鏈仍是 placeholder 的 6 個階段
   chain.go 宣告了 15 個階段，實際未實作：

階段 母本對應
filter FilterHandler / OutdatedHandler（漂移、過期點過濾）
defaultData HemisphereHandler / TimeHandler
position PositionHandler（最後位置、有效性）
geofence GeofenceHandler（把 geofenceIds 寫回 position）
computedAttributes ComputedAttributesHandler
distance DistanceHandler（累積里程）
另外 GeocoderHandler（把地址寫進 position.address）、EngineHoursHandler 也還沒接進處理鏈。
注意：main.go 其實已經接了 motion / alertEvent / fuel / driver / maintenance / media / speedLimit / mapMatcher / business 共 9 個，README「現況」寫「只實作 motion、business」是過時描述。

3. REST / 資料模型缺口
   母本有、Go 沒有的路由：

GET /api/positions/{id}、GET /api/commands/{id}
GET /api/devices/{id}/accumulators、GET /api/devices/{id}/image
POST /api/permissions/bulk
POST /api/users/totp（兩階段驗證）
GET /api/session/openid/auth、/callback（OpenID Connect）
GET /api/stream/{deviceId}/{channel}/live.m3u8、/{index}.ts（HLS 影像串流）
POST /api/attributes/computed/test
GET /api/server/cache
資料模型：device.go 只有 id/uniqueId/name/model/protocol/groupId/attributes/status，缺母本的 phone、contact、category、disabled、expirationTime、calendarId、positionId；PUT /api/devices/{id} 只能改 name/model/attributes。

其他已知限制：

帳號 API 無搜尋/排序，email 建立後不可改
群組授權不會展開到通知收件者
分享帳號沒有到期自動清理
/api/socket token 放 query string
GT06/H02/Teltonika 命令回應無識別碼，只能配對「該裝置最新一筆 sent」4. 基礎設施
Forward：缺 AMQP、Kafka、事件外送、Network forwarder
叢集：無 Redis 分散式 session、無 WebSocket 跨節點廣播 → 目前只能單節點
資料庫：只支援 PostgreSQL；schema 是 Go 版自己的，不能直接接母本資料庫
Web 前端：Go 版沒有前端（母本 repo 本身也不含 traccar-web，前端是獨立專案）5. 驗證狀態（我實際跑過）

PostgreSQL 整合測試（TRACCAR_POSTGRES_DSN）在此沙箱無法啟動 DB，全部 skip
make smoke / make rest-e2e 因沙箱禁止綁定 TCP 埠，未實際跑過
建議的下一步優先順序
修 suntech 縮放測試並註冊 ruptela/suntech（成本最低）
補 filter、distance、computedAttributes、geofence 四個處理鏈階段（影響報表與事件正確性）
補 positions/{id}、commands/{id}、device 欄位與 accumulators（前端對接必需）
依實際客戶設備清單，一支一支補協議（不要追 272 支）
要水平擴展再做 Redis session/broadcast，目前可以先維持單節點
----now check end ---
