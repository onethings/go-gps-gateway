# traccar-server-go：Go 協議網關

用 Go 重寫 Traccar 的設備協議與伺服器：設備幀 → 標準 `Position` → SQL 資料庫
（PostgreSQL / MySQL / SQLite），並提供對齊母本的 REST API、告警 / 地理圍欄、通知、
命令下發與處理鏈。

- 母本：`../traccar-master`（Java）；每個 Go 檔案都標明對應的 Java 類別。
- 協議層是純函式（`[]byte` → `*model.Position`），I/O 集中在 `internal/gateway`。
- 需求：Go 1.22+；資料庫只在 REST / 告警 / 命令歷史時需要。PostgreSQL、MySQL 8.0+、
  SQLite 三選一，由 `database.url` 的 scheme 決定。

## 現況

- **協議**：12 支（H02、GT06、JT808、Teltonika、OsmAnd、MQTT、GPS103、TK103、Meitrack、
  Wondex、Suntech、Ruptela），TCP / UDP / TLS / HTTP / poll 五種傳輸。
- **處理鏈**：15 個 stage 全部有實作，含 Time / Hemisphere、Geolocation / Geocoder /
  CopyAttributes / EngineHours、ComputedAttributes（`internal/expr` 求值）、ProximityRule。
- **REST**：裝置 / 位置 / 事件、帳號與 token、TOTP、權限與分享、資源 CRUD、命令、
  WebSocket、統計、媒體、HLS、OpenID、LDAP、密碼自助。
- **外送**：JSON、URL、MQTT、Redis、Wialon、Network（TCP/UDP）、AMQP 0-9-1、Kafka。
- **排程**：expiration、temporary、cleanup、health、reports、deviceInactive、deviceExpiration，
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

# MySQL（8.0+；本機 socket 或 TCP 皆可）
TRACCAR_DSN='mysql://user:pass@127.0.0.1:3306/traccar' make run-db
# SQLite（內嵌，取代 Java 版的 H2；檔案不存在會自動建立）
TRACCAR_DSN='sqlite:///var/lib/traccar/traccar.db' make run-db

docker compose up --build -d
curl -H 'X-Api-Key: dev-key' http://127.0.0.1:8082/api/health
open http://127.0.0.1:8082/          # 介面；./web 就是倉庫內的 traccar-web build
docker compose down -v

# 同一進程託管 traccar-web（把前端 build 出來的 build 目錄指給 -web-path）
./bin/traccar-gateway -dsn "$TRACCAR_DSN" -api-key dev-key -web-path ./web

# VPS（Ubuntu）：systemd + SQLite，--domain 另加 nginx；見「VPS 部署（Ubuntu）」
sudo ./deploy/ubuntu/install.sh
```

## 設定

三層（低 → 高）：內建預設 → `traccar.xml` → 環境變數；flag 在最上層。key 名照母本
`config/Keys.java`，Go 專屬 key 標了 `Go only`。

```xml
<properties>
    <entry key='web.address'>0.0.0.0:8082</entry>
    <entry key='database.url'>postgres://127.0.0.1:5432/traccar?sslmode=disable</entry>
    <!-- 或 mysql://user:pass@127.0.0.1:3306/traccar -->
    <!-- 或 sqlite:///var/lib/traccar/traccar.db -->
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

常用 flag：`-dsn`、`-api-key`、`-web-path`（traccar-web build 目錄）、`-allow-anonymous`、`-auto-register`（true）、`-batch-size`(500) /
`-batch-interval`(1s)、`-overspeed-knots` / `-power-cut-volts`（0=關）、`-alert-repeat`(0)、
`-geofence-refresh`(5m) / `-device-refresh`(5m)、`-login-max-failures`(5) / `-login-lock-duration`(15m)、
`-issue-token` / `-token-scopes` / `-token-ttl`、`-command-retry`(30s) / `-command-attempts`(3) /
`-command-queue`(32) / `-command-queue-ttl`(5m) / `-command-retention`(168h)、`-disable-share`、
`-notify-*`。

只走設定檔 / 環境變數的服務：

| 功能 | Key |
| --- | --- |
| 前端託管 | `web.path`（traccar-web build 目錄；同時是 `POST /api/server/file/{path}` 的寫入根目錄；空=不託管也不接受上傳） |
| 位置屬性複製 | `position.copyAttributes`（逗號分隔；空=關） |
| 帳號影像 | `deviceImage.path`（預設 `<media.path>/devices`） |
| HLS 串流 | `stream.path`（外部產生器寫 `<path>/<deviceId>/index.m3u8`） |
| Redis 推播 | `redis.url`、`redis.channel`（預設 `traccar`） |
| OpenID Connect | `openid.enable`、`openid.issuer`、`openid.clientId`、`openid.clientSecret`、`openid.redirectUrl`、`openid.scopes`、`openid.createMissing`、`openid.successUrl` |
| LDAP | `ldap.url`、`ldap.user`、`ldap.password`、`ldap.base`、`ldap.idAttribute`、`ldap.searchFilter`（母本名稱；`ldap.userFilter` 為相容別名）、`ldap.nameAttribute`、`ldap.mailAttribute`、`ldap.timeout` |
| 網路外送 | `forward.network.address`、`forward.network.protocol`（`tcp` / `udp`） |
| AMQP / Kafka 外送 | `forward.type`（`json` / `url` / `amqp` / `kafka`）、`forward.url`（AMQP 連線 URL 或 Kafka bootstrap）、`forward.exchange`（預設 `traccar`）、`forward.topic`（預設 `positions`）；事件側同名 `event.forward.*`（topic 預設 `events`） |
| 地理服務 | `geocoder.type`（nominatim / google / opencage / here / locationiq / mapbox / tomtom / photon / pelias / autonavi / baidu / tencent / ban / bingmaps / factual / geoapify / geocodefarm / geocodejson / geocodexyz / gisgraphy / mapquest / maptiler / mapmyindia / mygeocode / positionstack / pluscodes）、`geolocation.type`（google / mozilla / opencellid / unwired / here / universal）；其餘用 `*.url`。`nominatim`、`photon`、`ban`、`geocodejson`、`pluscodes` 免 `geocoder.key`（pluscodes 為本地計算） |
| 排程 | `schedule.{expiration,temporary,inactive,deviceExpiration,events,positions,commands}.interval`、`schedule.{events,positions,commands}.retention`（0=不刪） |
| 歷史查詢上限 | `report.maxPositions`（單次 `/api/positions` 與匯出的位置筆數上限；`0`=不限，預設 `50000`，與母本同名同義） |

## 協議

- **H02**：文字 `V1/V3/V4/V5/V6/V8/V19`、`NBR`、`LINK`、`VP1`、`HTBT`、`SMS`、心跳；
  二進位 32/45 byte 幀、`X` 幀、`R12` 應答。
- **GT06**：登入 / 心跳 / GPS-LBS 主線、`0x2A` 位址、`0x8A` 對時、`0x94` 電壓，CRC16/X25 ACK；
  `NT_MODELS` 自訂版面。待補：WiFi 掃描、ADAS/DMS。
- **JT808**：2013/2019 標頭、`0x7E` 轉義、XOR、註冊 / 位置 / 批次位置（`0x0704` / `0x0210`）、
  心跳 / ICCID / 對時。待補：駕駛員卡、參數、透傳、ADAS/DMS/胎壓/OBD。
- **Teltonika**：IMEI 握手、Codec 8 / 8E / 12 / 13、可變長度元素、LAC/CID。待補：Codec 7/16、BLE。
- **OsmAnd / MQTT**：OsmAnd 查詢字串；MQTT 3.1.1 重用同一 decoder。
- **GPS103 / TK103**：`imei:` 文字句、十進位度 + 半球。
- **Meitrack**：`$$` + 長度 + 欄位 + XOR。
- **Wondex**：`#DD#` + `;` 分隔。
- **Suntech**：`STT/ALT/EMG/EVT/UEX/BLE`、universal、zip / 二進位。待補：Encoder。
- **Ruptela**：IMEI 握手、Records / Extended、DTC、IO 設定；Encoder 齊全。

## 資料庫

`database.url` 的 scheme 決定方言，`Store.Migrate` 自動套用對應的 migration：

| 方言 | DSN 範例 | Migration |
| --- | --- | --- |
| PostgreSQL | `postgres://user:pass@host:5432/traccar?sslmode=disable` | `internal/storage/migrations/postgres/`（20 支） |
| MySQL 8.0+ | `mysql://user:pass@host:3306/traccar` | `internal/storage/migrations/mysql/` |
| SQLite | `sqlite:///var/lib/traccar/traccar.db` | `internal/storage/migrations/sqlite/` |


> 這裡用的是 Go 版自己的 schema，不是母本 `traccar-master` 的 `tc_*` schema。
> 直接接既有的 Java 版 MySQL 資料庫還需要一層 schema 對應（見「與母本差距」）。

MySQL 也可用本機 socket：`mysql://root@localhost/traccar?socket=/tmp/mysql.sock`。
SQLite 是純 Go（`modernc.org/sqlite`，無 cgo），用來取代 Java 版的 H2；
`sqlite::memory:` 適合測試。H2 本身是 Java 專屬，Go 版不支援。

MySQL 的端到端驗證（`rest-e2e.sh` 只打 REST，不依賴 `psql`）：

```bash
make db-up-mysql
DSN='mysql://root:traccar@127.0.0.1:53306/traccar' ./scripts/rest-e2e.sh
make db-down-mysql
```

主要表：`devices`、`positions`、`events`、`geofences`、`device_groups`、`calendars`、
`drivers`、`maintenances`、`orders`、`reports`、`users`、`api_tokens`、`commands`、
`password_resets`、`notifications`、`user_notifications`、`permissions`、`media`、
`protocol_settings`、`server_settings`、`actions`、`statistics`、`attributes`。

整合測試用 `TRACCAR_TEST_DSN`（舊名 `TRACCAR_POSTGRES_DSN` 仍相容）：

```bash
# SQLite，不需外部服務
TRACCAR_TEST_DSN='sqlite:///tmp/traccar-test.db' go test ./internal/storage
# MySQL
TRACCAR_TEST_DSN='mysql://root@127.0.0.1:3306/traccar' go test ./internal/storage
# PostgreSQL
TRACCAR_TEST_DSN='postgres://postgres@127.0.0.1:5432/postgres?sslmode=disable' go test ./internal/storage
```

## 資源需求

| 規模 | CPU | RAM | 磁碟 |
| --- | --- | --- | --- |
| 開發 / 1–20 台裝置 | 1 vCPU | 512MB–1GB | 10GB |
| 20–500 台（10–30 秒一筆） | 1–2 vCPU | 1GB | 25GB，需設 retention |
| 500 台以上 | 2 vCPU+ | 2GB+ | 依保留期與上報頻率 |

- gateway 常駐記憶體是數十 MB 級；每條裝置長連線一個 goroutine（約 8–16KB 加上讀取緩衝），
  幾百條連線仍在 100MB 以內。解碼是純函式，單核每秒可處理數千筆。
- **1GB 機器請用 release zip，不要在機器上編譯**：Go 編譯器峰值可吃 0.5–1GB。真要編譯，
  先加 2GB swap（`fallocate -l 2G /swapfile && chmod 600 /swapfile && mkswap /swapfile && swapon /swapfile`）。
- 1GB 請用 SQLite；MySQL 8 常駐就要 300–500MB，PostgreSQL 也要 100–200MB。
- 磁碟成長（positions 含索引約 300B/筆，預設**不**自動刪除）：

  | 裝置數 × 上報間隔 | 每天 | 每年 |
  | --- | --- | --- |
  | 1 × 10s | 2.6 MB | 約 1 GB |
  | 10 × 10s | 26 MB | 約 9.5 GB |
  | 10 × 30s | 8.6 MB | 約 3 GB |
  | 100 × 30s | 86 MB | 約 31 GB |

  25GB 磁碟建議在 `traccar.xml` 設定清理（`0` = 不刪）：

  ```xml
  <!-- 只吃 Go duration 單位（h / m / s），沒有 d：90 天 = 2160h。 -->
  <entry key='schedule.positions.retention'>2160h</entry>
  <entry key='schedule.events.retention'>4320h</entry>
  ```

  對應的 `schedule.positions.interval` / `schedule.events.interval` 預設就是 `24h`，不用另外設；
  值寫錯（例如 `90d`）只會在日誌留一行 `schedule: ...` 並維持不刪除。

## VPS 部署（Ubuntu）

`deploy/ubuntu/install.sh` 把下面幾節的步驟收成一支腳本：需要的話安裝 Go、建立 `traccar`
系統帳號、編譯並安裝 binary、複製 traccar-web build、寫 `/opt/traccar/conf/traccar.xml`
（與 Java 母版同位置，含隨機 `api.key`）、安裝並啟動 systemd unit，最後等 `/api/health` 回 200 才結束。

```bash
# 1. 把 repo 放到 VPS（git clone，或本機 rsync/scp 上傳）
git clone https://github.com/onethings/traccar-server_go.git
cd traccar-server_go

# 2. 一鍵安裝：SQLite + systemd，Web/API 開在 0.0.0.0:8082
sudo ./go_server/deploy/ubuntu/install.sh

# 3. 開 http://<VPS-IP>:8082/ ，註冊第一個帳號（會成為 administrator）
```

常用變化：

| 需求 | 指令 |
| --- | --- |
| 網域 + nginx（gateway 只聽 `127.0.0.1:8082`，nginx 反向代理並終結 TLS） | `sudo ./go_server/deploy/ubuntu/install.sh --domain tracker.example.com` |
| Let's Encrypt TLS | `sudo apt-get install -y certbot python3-certbot-nginx && sudo certbot --nginx -d tracker.example.com` |
| MySQL / PostgreSQL（先建好 DB 與帳號） | `--dsn 'mysql://traccar:密碼@127.0.0.1:3306/traccar'`、`--dsn 'postgres://traccar:密碼@127.0.0.1:5432/traccar?sslmode=disable'` |
| 在本機（macOS）編譯後只上傳執行檔 | `GOOS=linux GOARCH=amd64 go build -o tg ./go_server/cmd/traccar-gateway`，`scp tg vps:` 後 `sudo ./go_server/deploy/ubuntu/install.sh --binary ./tg` |
| 升級 | `git pull && sudo ./go_server/deploy/ubuntu/install.sh`（設定檔保留；`--force` 才重寫並換 `api.key`） |
| 設定檔放 `/etc/traccar`（舊版位置） | `sudo ./go_server/deploy/ubuntu/install.sh --conf-dir /etc/traccar` |

安裝後的檔案：

| 路徑 | 內容 |
| --- | --- |
| `/opt/traccar/bin/traccar-gateway` | 執行檔（升級時覆蓋） |
| `/opt/traccar/web` | traccar-web build；同時是 `POST /api/server/file/{path}` 的寫入根目錄 |
| `/opt/traccar/data/traccar.db` | 預設 SQLite 資料庫（`--dsn` 可換 PostgreSQL / MySQL） |
| `/opt/traccar/conf/traccar.xml` | 設定檔；位置與 Java 母版相同（`--conf-dir` 可改） |
| `/opt/traccar/conf/api-key` | 隨機產生的 admin API key（`0640 root:traccar`） |
| `/etc/systemd/system/traccar-gateway.service` | systemd unit（`ProtectSystem=strict`，只放行 `data` / `web` 可寫） |

日常操作：

```bash
sudo systemctl status traccar-gateway
sudo journalctl -u traccar-gateway -f
curl -H "X-Api-Key: $(sudo cat /opt/traccar/conf/api-key)" http://127.0.0.1:8082/api/health
sudo systemctl restart traccar-gateway
```

設備要連得進來，除了 22/80/443，ufw 與雲端安全組都要開協議埠：

```bash
sudo ufw allow 22/tcp
sudo ufw allow 80,443/tcp
sudo ufw allow 5013,5023,5027,8610/tcp        # H02 / GT06 / Teltonika / JT808
sudo ufw allow 5055,1883/tcp                  # OsmAnd / MQTT
sudo ufw allow 5001,5002,5006,5011,5020,5046/tcp
sudo timedatectl set-timezone Asia/Taipei     # H02 / GT06 的對時與編碼用本地時間
```

移除：

```bash
sudo systemctl disable --now traccar-gateway
sudo rm /etc/systemd/system/traccar-gateway.service
sudo rm -rf /opt/traccar
sudo rm -rf /etc/traccar          # 只有從舊版（設定在 /etc）升級上來的才需要
sudo rm -f /etc/nginx/sites-enabled/traccar-gateway /etc/nginx/sites-available/traccar-gateway
sudo userdel traccar
```

### 手動部署（MySQL + traccar-web）

不想用腳本、也不想自己架 nginx 時，Go 進程可以直接託管前端：把 traccar-web 的 build
（`index.html` 與 assets）放到一個目錄，`-web-path` 指過去即可；`/api/*` 仍由同一進程處理，
session cookie 與 WebSocket 都是同源。

1. 建 MySQL 資料庫與專用帳號（8.0+）：

   ```sql
   CREATE DATABASE traccar CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
   CREATE USER 'traccar'@'127.0.0.1' IDENTIFIED BY '改成強密碼';
   GRANT ALL PRIVILEGES ON traccar.* TO 'traccar'@'127.0.0.1';
   ```

2. 取得 traccar-web：官方 release zip 內的 `web/`，或 traccar-web repo `npm ci && npm run build`
   的輸出，放到 `/opt/traccar/web`。

3. 啟動：

   ```bash
   ./bin/traccar-gateway \
     -dsn 'mysql://traccar:改成強密碼@127.0.0.1:3306/traccar' \
     -api-key "$(openssl rand -hex 24)" \
     -web-path /opt/traccar/web \
     -http 127.0.0.1:8082
   ```

4. 開瀏覽器：第一次沒有任何帳號時，前端會顯示註冊表單；`POST /api/users` 允許匿名建立
   **第一個**帳號並自動設為 administrator。之後的匿名註冊只在 `server.registration=true`
   時開放。

5. 若要改用 nginx 託管前端（或用 Docker）：

   ```nginx
   server {
       listen 443 ssl;
       root /opt/traccar/web;
       location / { try_files $uri /index.html; }
       location /api/ {
           proxy_pass http://127.0.0.1:8082;
           proxy_http_version 1.1;
           proxy_set_header Upgrade $http_upgrade;
           proxy_set_header Connection "upgrade";
           proxy_set_header Host $host;
       }
   }
   ```

   ```bash
   # 或整組用容器（MySQL + gateway + ./web）
   TRACCAR_API_KEY=... MYSQL_ROOT_PASSWORD=... make compose-up-mysql
   ```

6. 驗證：

   ```bash
   curl -H 'X-Api-Key: ...' http://127.0.0.1:8082/api/health
   DSN='mysql://traccar:改成強密碼@127.0.0.1:3306/traccar' ./scripts/rest-e2e.sh
   ```

## 打包與發佈（release zip）

`make dist` 把 linux/amd64 與 linux/arm64 的執行檔、traccar-web build 與 Ubuntu 安裝腳本
打包成一個 zip；目標機器不需要 Go，也不需要原始碼。

```bash
make dist                    # → dist/traccar-server-go_<git describe>.zip (+ .sha256)
make dist VERSION=v0.1.0     # 指定版本
```

zip 內容與用法：

```
traccar-server-go_<version>/
├── install.sh
├── bin/linux-amd64/traccar-gateway
├── bin/linux-arm64/traccar-gateway
├── web/
├── traccar-gateway.service.in
├── nginx.conf.in
└── INSTALL.txt
```

```bash
unzip traccar-server-go_0.1.0.zip && cd traccar-server-go_0.1.0
sudo ./install.sh            # 架構由 uname -m 自動選 bin/linux-<arch>/
```

發佈到另一個公開 repo，兩種都行：

**Release asset**（建議；repo 不必放原始碼，下載走 CDN）：

```bash
gh auth login          # 只需要一次，需在沙箱外執行
./scripts/publish-release.sh <你的帳號>/<公開專案> v0.1.0
```

`publish-release.sh` 會在 repo 不存在時先 `gh repo create --public --add-readme`，再把
`dist/*.zip` 與 `dist/*.zip.sha256` 上傳成 release asset（同 tag 已存在時改用 `gh release upload --clobber`）。

```bash
BASE=https://github.com/<你的帳號>/<公開專案>/releases/download/v0.1.0
curl -L -O "$BASE/traccar-server-go_0.1.0.zip"
curl -L -O "$BASE/traccar-server-go_0.1.0.zip.sha256"
sha256sum -c traccar-server-go_0.1.0.zip.sha256
unzip traccar-server-go_0.1.0.zip && cd traccar-server-go_0.1.0 && sudo ./install.sh
```

**直接提交進 repo**（`git add` 或網頁上傳 zip 與 `.sha256`；檔案不大時最省事）：

```bash
BASE=https://raw.githubusercontent.com/<你的帳號>/<公開專案>/main
curl -L -O "$BASE/traccar-server-go_0.1.0.zip"
curl -L -O "$BASE/traccar-server-go_0.1.0.zip.sha256"
sha256sum -c traccar-server-go_0.1.0.zip.sha256
unzip traccar-server-go_0.1.0.zip && cd traccar-server-go_0.1.0 && sudo ./install.sh
```

macOS 沒有 `sha256sum`，用 `shasum -a 256 -c`。

> raw 路徑只適合小檔案（單檔上限 100 MB）；之後版本變大或要版本紀錄，再改用 release asset。

目前公開下載點：`onethings/tra-server-go-releases-fortesting`。

## REST API

`-api-key`（admin）、`api_tokens`（scope + permissions）或 `/api/login` token；`/api/health`、
`/api/login`、密碼自助、OpenID 與 `/api/socket` 例外。非 admin 只能存取授權裝置；
本機可用 `-allow-anonymous` 關閉認證。

清單端點（devices / positions / users / commands / notifications / resources）回傳**裸 JSON 陣列**，
和母本 `openapi.yaml` 一致；事件與告警仍回 `{events|alerts, count}`。`GET /api/positions` 的每筆
位置同時帶 `deviceTime` 與 `fixTime`（Go 只有一個時間戳，兩者相同）。

`POST /api/users` 在帳號表為空時允許匿名建立第一個帳號（自動成為 administrator），
之後只有在 `server.registration=true` 時才開放匿名註冊；一般使用者可 `PUT /api/users/{id}`
更新自己的 name / attributes / password，但不能給自己 administrator 或 disabled。
`GET /api/users/{id}` 允許 admin 或本人。

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
- `make smoke`：無資料庫重播；`make rest-e2e`：資料庫全鏈驗證（`DSN` 可指 PostgreSQL 或 MySQL）。
- `cmd/device-sim`：設備模擬器。

## 已知限制

- 有 `-dsn` 時 REST 預設要求憑證；`-api-key` 或第一個註冊帳號可取得管理權限。
- 帳號 API 只有分頁；email 建立後不可改；登入失敗鎖定（bcrypt cost 12）。
- `users` 表沒有 `device_limit` / `user_limit` 欄位，API 一律回 0；母本停在 `tc_*` 相容時再補。
- `PUT /api/devices/{id}` 是整欄取代；`protocol` / `uniqueId` 不可改；`disabled` 尚未擋 ingest。
- 群組授權只影響可見裝置；通知收件者仍只認 `user → device`。
- 分享帳號由 admin 或 `schedule.temporary.interval` 到期刪除；`/api/socket` 接受 query token 或 session cookie。
- `suntech` 只能收不能下命令；記憶體模式僅適合開發。
- MySQL 需 8.0+（`ROW_NUMBER` 窗函式）；SQLite 適合單節點，不建議多程序共用同一檔案。
- TOTP / OpenID / LDAP / HLS / 裝置影像 / Redis 推播都要設定後才啟用，未設定回 501。
- 位置事件寫入時 `position_id` 多為 NULL（位置批次寫入不回填 id），事件與位置在報表上不會互相關聯。

## 與母本差距

| 面向 | Go 版 | 缺口 |
| --- | --- | --- |
| 協議解碼器 | 12 | 256 |
| 命令 Encoder | 5 | 60 |
| 處理鏈 / 事件 | 15 stage / 12 規則 | 0 |
| OpenAPI 路徑 | 76 | 0 |
| 地理 provider | geocoder 26、geolocation 6 | — |
| 外送 | JSON、URL、MQTT、Redis、Wialon、Network、AMQP、Kafka | — |
| 資料庫 | PostgreSQL、MySQL 8.0+、SQLite（內嵌取代 H2） | H2（Java 專屬）；母本 `tc_*` schema 尚未相容 |
| 認證 | 密碼、token、TOTP、OpenID、LDAP | 0 |
| 前端 | 可直接託管 traccar-web build（同源 cookie / WebSocket） | 無內建 build；需自備 `web/` |
| 水平擴展 | Redis 推播 | multicast；跨節點 device session |
| 影像 | 上傳 / 下載、HLS 端點 | JT1078 解碼與切片 |
| 排程 | 10 | — |

後續順序：依客戶設備補協議與 Encoder → 要接母本資料庫再處理
母本 `tc_*` schema 相容 → 要即時影像再做 JT1078。
