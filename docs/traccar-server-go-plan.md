# traccar-server-go 收尾計畫

收掉 README 的兩條「下一步」，補上已知限制裡代價最高的三個缺口。
四支 PR，一支一個可驗證單位，每一格都要有證據才算完成。
PR-1 先做，後面三支的 live lane 都要靠它的 simulator。

## How to read this

One box is one unit of work. Every box names the evidence that checks it. A nested box is a sub-step of the box above it. Check a box only when its evidence exists, a file, a log line, a screenshot, a test run, or a SHA. The body is a how-to. The appendices explain and record.

The program runs `playbooks/autopilot-stack.md`. Operator 逐支合併，每支停在 merge-ready 等 operator 按下去，owner 不自己 merge 到 `main` 之外的任何地方。

Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

這個 repo 沒有把 pstack vendor 進來。下面出現的 `git show origin/main:` 讀法要等 repo 真的放入 `pstack/` 才成立，現階段一律從安裝路徑 `~/.config/github-copilot/pstack/skills/` 讀同一份檔案。

## Program checklist

### Arm the program

- [ ] 把這份計畫與執行 playbook 講給 operator 聽，然後停住，等 operator 明確說開始。
- [ ] 每次 tick 重讀這幾份。vendored 之後改成 `git show origin/main:pstack/skills/poteto-mode/playbooks/autopilot-stack.md` 這種讀法，在那之前從安裝路徑讀。
  - [ ] `~/.config/github-copilot/pstack/skills/poteto-mode/playbooks/autopilot-stack.md`
  - [ ] `~/.config/github-copilot/pstack/skills/swarm/SKILL.md`
  - [ ] `~/.config/github-copilot/pstack/skills/poteto-mode/playbooks/opening-a-pr.md`
- [ ] operator 說開始後，把 audit tick 掛成 `/loop 1h`，不要靠記憶。
- [ ] tick prompt 用這段，一字不改。

```
Re-read the execution playbook from trunk. Audit the operation against it and fix drift in this tick. Probe every active lane and judge progress by side effects only. Stand down a stuck lane and dispatch its replacement now. Then post a short status message to the operator in chat only when the audit found a tracked change that no earlier status message reported, such as a PR opened, a code-ready head, a round launched or closed, a verdict, a merge, a stuck agent and the action taken, a blocker added or cleared, or a decision only the operator can make. Name every such change and nothing else. Do not repeat a table, the merged list, or an unchanged blocker. If the audit found none, end the turn with no reply text. Either way, log this tick's row in your decision trail. The row names the items reported, or none.
```

- [ ] operator 喊停或收回授權時，立刻給每個 owner 一道零寫入指令。

### Spawn owners

- [ ] 每支 PR 一個 owner，生命週期照 `playbooks/autopilot-stack.md`。
- [ ] 依賴圖如下。PR-1 先落地，它提供其他 PR 的 live lane 所需的 simulator。
  - [ ] PR-1 與 PR-3 獨立，兩支都從 `main` 開。
  - [ ] PR-2 與 PR-4 在 PR-1 之後開，否則 lane 只能用手寫的 python 腳本硬湊。
  - [ ] PR-4 不依賴 PR-2，兩支碰的檔案沒有交集。
- [ ] 檔案邊界。PR-1 只碰 `go_server/cmd/device-sim/`、`go_server/scripts/`、`go_server/cmd/traccar-gateway/main.go`、`go_server/internal/gateway/server_bench_test.go` 與 `go_server/README.md`。
- [ ] 檔案邊界。PR-2 只碰 `go_server/internal/codec/codec.go`、`go_server/internal/gateway/directory.go`、`go_server/internal/storage/postgres/store.go`、GT06 與 JT808 的 decoder 與測試。
- [ ] 檔案邊界。PR-3 只碰 `go_server/internal/api/auth.go`、`go_server/internal/api/users.go`、`go_server/internal/auth/`、`go_server/internal/storage/postgres/users.go`、`0006_users_password.sql`。
- [ ] 檔案邊界。PR-4 只碰 `go_server/internal/gateway/`、`go_server/internal/model/command.go`、`go_server/internal/storage/postgres/commands.go`、`0007_commands.sql`、`go_server/internal/api/server.go` 的命令段。
- [ ] Review gate。PR-3 改的是 API 互動，等 operator 看過 curl transcript 與終端錄影才合。

### PR mechanics, for every PR

- [ ] forge 固定用 `gh`，這個專案沒有 Origin CLI。
- [ ] PR 開成 ready，不開 draft，base 是 `main`。
- [ ] 推送前跑一次 `go vet ./...`、`go build ./...`、`go test ./...`，三者都乾淨才推。
- [ ] commit 訊息走 `technical-writing`，body 寫動機與驗證，不准再出現 `ok`。
- [ ] 每次 commit 前跑 `/deslop`，送 review 前跑 `/no-comments`。
- [ ] Bugbot 與 security review 的留言逐條分類，照 `../references/bugbot-triage.md` 判 fix、dismiss 或問。
- [ ] code-ready 報告前 rebase 到當時的 `main`，之後的修正輪沿用同一個 merge base。

### Verdict and merge, for every PR

- [ ] code-ready head 上跑一次完整 swarm，十條 lane 都有結果才會有 verdict。
- [ ] 依 blocking rule 判 verdict。trunk 側就壞的 lane 記在 trunk，不擋這支 PR。
- [ ] 新 head 就重跑 verdict，patch-id 沒變的結果可以沿用。
- [ ] rebase 後 patch-id 不變是合併前的最後一道檢查。
- [ ] owner 把自己的 PR squash-merge 到 `main`。PR-3 要等 operator 在 review gate 按下之後才輪到這步。

### Boot recipe, for every live lane

- [ ] `git fetch origin <branch> && git checkout <sha>`。
- [ ] 起一個乾淨的 Postgres，`docker run --rm -d --name swarm-pg -p 55432:5432 -e POSTGRES_PASSWORD=test postgres:16`。
- [ ] 跑 `go run ./cmd/traccar-gateway -dsn "$DSN" -api-key test-admin -http 127.0.0.1:8082 -device-refresh 1s`，輪詢 `curl -sf 127.0.0.1:8082/api/health` 直到回 200，最多等 30 秒。
- [ ] 輸入只走 `curl` 與 `go run ./cmd/device-sim`。唯讀診斷用 `psql -c` 與 `curl 127.0.0.1:8082/metrics`。
- [ ] 每條 lane 的產物存到 `/tmp/swarm-<pr-id>/worker-<n>/<slug>.log`，回報時附完整路徑。

## Build the device simulator and its benchmarks (PR-1)

**Depends on.** None.

**Files.**

- [x] Create `go_server/cmd/device-sim/main.go`.
- [x] Create `go_server/cmd/device-sim/main_test.go`.
- [x] Create `go_server/internal/devicescene/scene.go`.
- [x] Create `go_server/internal/devicescene/scenes/gt06.json` and the other four scenes.
- [x] Create `go_server/internal/gateway/scene_replay_test.go`.
- [x] Create `go_server/scripts/live-lanes.sh`.
- [x] Create `go_server/internal/gateway/server_bench_test.go`.
- [x] Edit `go_server/cmd/traccar-gateway/main.go`.
- [x] Edit `go_server/README.md`.

**Build.**

- [x] `device-sim` 的 `replay` 子命令吃一份 JSON 場景檔，用 `gateway.DefaultRegistry` 的 encoder 組出 login 與 position 幀，送到 `-addr` 指定的協議埠。
- [x] `replay` 支援三種寫法，`single` 一次寫完、`split` 把一個幀拆兩次寫、`batch` 把兩個幀一次寫。
- [x] `scripts/live-lanes.sh` 收一個 lane 編號，跑對應場景，把 gateway log 與 sim 輸出寫進 `/tmp/swarm-pr1/`。
- [x] `server_bench_test.go` 加 `BenchmarkGatewayIngest` 與 `BenchmarkDecodeGT06`，同時報 ns/op 與 allocs/op。
- [x] `main.go` 加 `-auto-register` 旗標，預設 true，跟現在的無條件 `gateway.WithAutoRegister()` 同義，關掉時不註冊未知裝置。

**You see.**

- [ ] sim 對 GT06 埠送出 login 後，gateway log 出現該 IMEI 與裝置已註冊的那一行。
- [ ] `scripts/live-lanes.sh 2` 跑完後 `/tmp/swarm-pr1/lane-2.log` 最後一行是 `pass 2`。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] `TestWriteModesReplaySameBytes` 断言三種寫法的位元組流相同，`TestScenesReachTheSink` 把五個場景經 `net.Pipe` 灌進真的 `gateway.Server` 並斷言 sink 收到的 position 數，`TestAutoRegisterOff` 與 `TestAutoRegisterOn` 蓋住旗標的兩邊。跑 `go test ./cmd/device-sim/... ./internal/gateway/...`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. trunk 沒有 simulator，lane 記錄這件事，改為 gate 端到端狀態，sim 送 GT06 login 與兩筆 position 後 `select count(*) from positions` 要回 2。Save `lane-1.log`. Pass when head 側回 2 且 log 沒有 decode error。
- [ ] Lane 2. GT06 login 加一筆 position，檢查回應的 ACK 位元組。Save `lane-2.log`. Pass when sim 印出的 ACK hex 等於 `decoder_test.go` 的期望值。
- [ ] Lane 3. H02 文字幀帶轉義字元。Save `lane-3.log`. Pass when 解析出的經緯度與 fixture 的差值是 0。
- [ ] Lane 4. JT808 帶 `0x7e` 轉義與校驗碼。Save `lane-4.log`. Pass when sim 收到平台一般回應且 checksum 驗算通過。
- [ ] Lane 5. Teltonika 先 IMEI 握手再送 Codec 8 兩筆記錄。Save `lane-5.log`. Pass when `positions` 多兩列且兩列的 device_id 相同。
- [ ] Lane 6. `split` 模式把一個幀拆兩次寫。Save `lane-6.log`. Pass when 只產生一筆 position。
- [ ] Lane 7. `batch` 模式把兩個幀一次寫。Save `lane-7.log`. Pass when 產生兩筆 position 且順序與送出順序一致。
- [ ] Lane 8. 未知 IMEI 在 `-auto-register=false` 的第二個 instance 上連線。Save `lane-8.log`. Pass when 連線被關閉且 `devices` 沒有新列，同一幀打到預設 instance 則會新增一列。
- [ ] Lane 9. 同一 IMEI 連三次、斷三次。Save `lane-9.log`. Pass when `devices` 只有一列且 `id` 沒變。
- [ ] Lane 10. 200 條並行連線跑 10 秒。Save `lane-10.log`. Pass when `/metrics` 的 `decode_errors` 是 0，結束後 `connections_active` 回 0。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] Metric. `BenchmarkGatewayIngest` 的 positions per second 與 allocs per position。
- [x] Probe. `go test ./internal/gateway -bench 'BenchmarkGatewayIngest|BenchmarkDecodeGT06' -benchtime 2s -count 1`，trunk 與 head 交錯各跑五輪。
- [x] Baseline. trunk 沒有這個 benchmark，head 第一輪在本機 i5-4200U 量到 10,764 ns/op、92,901 positions/s、848 B/op、9 allocs/op，DecodeGT06 是 2,253 ns/op、9 allocs/op。後續三支 PR 都拿這組當基準。
- [x] Rule. head 低於基準的 80% 就算失敗，allocs per position 超過 12 也算失敗。

**Review gate.** None. PR-1 is not review-gated.

**Merge.**

- [ ] root 在 head SHA 給出 clean verdict。
- [ ] Bugbot 留言分類完成。
- [ ] rebase 到 `main` 後 patch-id 不變。
- [ ] owner squash-merge。

## Inject the device model into the protocol base (PR-2)

**Depends on.** PR-1.

**Files.**

- [x] Edit `go_server/internal/codec/codec.go`.
- [x] Edit `go_server/internal/gateway/directory.go`.
- [x] Edit `go_server/internal/storage/postgres/store.go`.
- [x] Edit `go_server/internal/protocol/gt06/decoder.go`.
- [x] Edit `go_server/internal/protocol/jt808/decoder.go`.
- [x] Edit `go_server/internal/protocol/gt06/decoder_test.go`.
- [x] Edit `go_server/internal/protocol/jt808/decoder_test.go`.
- [x] Create `go_server/internal/gateway/directory_test.go`.
- [x] Edit `go_server/internal/storage/postgres/store_test.go`.

**Build.**

- [x] `codec.DeviceSession` 加 `Model` 欄位。`gateway.MemoryDirectory.Resolve`、`postgres.Store.Resolve` 與 `gateway.Server` 自動註冊那一條路徑一起填它，自動註冊的裝置 model 先留空。
- [x] `gt06.Decoder.decodePower` 收 `session.Model` 推出的 LW / SEEWORLD / VL 旗標，照 Java `Gt06ProtocolDecoder.decodeAlarm` 的模型分支決定告警名稱。`modelNT` 的 LBS2 / DRIVER 版面差異不在這支 PR，記在附錄 C。
- [x] `jt808.Decoder.decodeAlarm` 改成收 model，照 Java 的 `PADLOCK_MODELS` 與 `AOVX_MODELS` 與 `VL300` 三條分支走。
- [x] model 一律先轉大寫再比對，跟 Java 的 `toUpperCase(Locale.ROOT)` 對齊。

**You see.**

- [ ] model 設成 `G-360P` 的 JT808 裝置送 alarm bit 0，position 的 alarms 陣列出現移除告警。
- [ ] model 留空的裝置送同一幀，alarms 仍是現行的 SOS。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] 兩個 decoder 的測試各加一組 table，涵蓋 PADLOCK、AOVX、VL300、`NT26`、空字串與小寫輸入。跑 `go test ./internal/protocol/... ./internal/gateway/...`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 同一台 model 留空的 GT06 裝置送同一幀，trunk 與 head 的 position JSON 逐欄相同。Save `lane-1.log`. Pass when `diff` 沒有輸出。
- [ ] Lane 2. GT06 model 設 `NT26`，送 Java fixture 的那一幀。Save `lane-2.log`. Pass when 解析結果等於 Java 測試裡的期望值。
- [ ] Lane 3. GT06 model 設 `VL103`，走 `modelVL` 分支。Save `lane-3.log`. Pass when 與 Java fixture 相符。
- [ ] Lane 4. JT808 model 設 `G-360P`，alarm bit 1。Save `lane-4.log`. Pass when alarms 出現 tampering。
- [ ] Lane 5. JT808 model 設 `AL300`，alarm bit 16。Save `lane-5.log`. Pass when alarms 出現 movement。
- [ ] Lane 6. JT808 model 設 `VL300`，alarm bit 29。Save `lane-6.log`. Pass when alarms 沒有 accident，同一幀換成空 model 會出現 accident。
- [ ] Lane 7. JT808 model 是小寫 `al300`。Save `lane-7.log`. Pass when 結果與大寫相同。
- [ ] Lane 8. 用 `PUT /api/devices/{id}` 把 model 改成 `G-508P`，斷線重連後再上報。Save `lane-8.log`. Pass when 重連後那一幀走 padlock 分支。session 在登入時解析，model 不會套用到已建立的連線。
- [ ] Lane 9. model 是未知名稱 `ZZ999`。Save `lane-9.log`. Pass when 走通用分支且不報錯。
- [ ] Lane 10. 200 台混 5 種 model 同時上報 10 分鐘。Save `lane-10.log`. Pass when `decode_errors` 是 0、每台 device_id 穩定、`positions` 筆數等於送出筆數。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] Metric. `BenchmarkDecodeGT06` 與 `BenchmarkDecodeJT808` 的 ns/op 與 allocs/op。
- [x] Probe. `go test ./internal/protocol/gt06 ./internal/protocol/jt808 -bench Decode -benchtime 5s -count 5`，trunk 與 head 交錯。
- [x] Baseline. 先記 trunk 的兩個 ns/op 值，PR-1 的 benchmark 已在 trunk 上。
- [x] Rule. head 比 trunk 慢超過 5% 就失敗，allocs/op 上升也算失敗。 實測 trunk 中位數 2,343 ns，head 2,310 ns，allocs 兩邊都是 9。

**Review gate.** None. PR-2 is not review-gated.

**Merge.**

- [ ] root 在 head SHA 給出 clean verdict。
- [ ] Bugbot 留言分類完成。
- [ ] rebase 到 `main` 後 patch-id 不變。
- [ ] owner squash-merge。

## Add password login and self-service tokens (PR-3)

**Depends on.** None.

**Files.**

- [ ] Create `go_server/internal/storage/postgres/migrations/0006_users_password.sql`.
- [ ] Create `go_server/internal/auth/password.go`.
- [ ] Create `go_server/internal/auth/password_test.go`.
- [ ] Edit `go_server/internal/api/auth.go`.
- [ ] Edit `go_server/internal/api/users.go`.
- [ ] Edit `go_server/internal/storage/postgres/users.go`.
- [ ] Edit `go_server/go.mod`.

**Build.**

- [ ] `0006_users_password.sql` 給 `users` 加 `password_hash`，預設 NULL，舊帳號不用改。
- [ ] `auth.HashPassword` 與 `auth.VerifyPassword` 用 `golang.org/x/crypto/bcrypt`，cost 12，並把它從 indirect 升成直接依賴。
- [ ] `POST /api/login` 收 email 與 password，成功回一組新 token，失敗回 401，停用帳號回 403。
- [ ] `handleTokens` 與 `handleRevokeToken` 對非 admin 開放本人路徑，`isSelf` 已經存在，補上 scope 檢查就好。
- [ ] 任何 user JSON 都不輸出 `password_hash`。

**You see.**

- [ ] `curl -s -X POST 127.0.0.1:8082/api/login -d '{"email":"a@b.c","password":"..."}'` 回 200 與 `trc_` 開頭的 token。
- [ ] 同一支帳號打錯密碼回 401，`api_tokens` 沒有新列。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `password_test.go` 涵蓋雜湊可驗、錯密碼不可驗、空密碼被拒。`users_test.go` 涵蓋非 admin 只能碰自己的 token。跑 `go test ./internal/auth/... ./internal/api/... ./internal/storage/postgres/...`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. `-api-key` 走法在 trunk 與 head 都回 200。Save `lane-1.log`. Pass when 兩邊狀態碼與 body 相同。
- [ ] Lane 2. 建帳號、設密碼、登入拿 token。Save `lane-2.log`. Pass when 回 200 且 token 可用來打 `/api/devices`。
- [ ] Lane 3. 同一帳號打錯三次密碼。Save `lane-3.log`. Pass when 三次都 401，`api_tokens` 沒有新列。
- [ ] Lane 4. 停用帳號後登入。Save `lane-4.log`. Pass when 回 403。
- [ ] Lane 5. 在已經有資料的 DB 上跑 0006。Save `lane-5.log`. Pass when migration 成功且 `select count(*) from users` 前後相同。
- [ ] Lane 6. 非 admin 列出自己的 token。Save `lane-6.log`. Pass when 回 200 且看不到別人的 token。
- [ ] Lane 7. 非 admin 列出別人的 token。Save `lane-7.log`. Pass when 回 403。
- [ ] Lane 8. 非 admin 撤銷自己的 token，再拿它打一次。Save `lane-8.log`. Pass when 第二次回 401。
- [ ] Lane 9. 抓任何 user JSON 檢查欄位。Save `lane-9.log`. Pass when 全部回應都找不到 `password_hash`。
- [ ] Lane 10. 送 1 MB 的 login body。Save `lane-10.log`. Pass when 回 413 或 400，gateway 沒有 panic。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. `POST /api/login` 的 p95 延遲與每個請求的 DB round trip 數。
- [ ] Probe. `ab -n 200 -c 5 -p login.json -T application/json 127.0.0.1:8082/api/login`，對 head 跑；trunk 沒有這個端點，改量 trunk 的 `GET /api/devices` 當基準。
- [ ] Baseline. 先記 trunk 的 `GET /api/devices` p95。
- [ ] Rule. head 的 login p95 超過 trunk 基準的三倍就算失敗，bcrypt cost 12 是已知的固定成本，不在放寬範圍內。

**Review gate.** The operator reviews before merge.

- [ ] 把 `lane-2.log`、`lane-6.log`、`lane-8.log` 的 curl transcript 貼進 chat。JSON API 沒有 GUI 可截圖，transcript 就是那次 screenshot。
- [ ] 用 `script -q` 錄一段 30 到 60 秒的 video，內容是建帳號、登入、撤銷 token 三個動作，母帶放 `/tmp/swarm-pr3/pr-3-review.mp4`。
- [ ] 把 transcript 與 video 貼進 chat，停住等 operator 按下。

**Merge.**

- [ ] root 在 head SHA 給出 clean verdict。
- [ ] Bugbot 留言分類完成。
- [ ] operator 在 review gate 按下。
- [ ] rebase 到 `main` 後 patch-id 不變。
- [ ] owner squash-merge。

## Persist commands and record device acknowledgements (PR-4)

**Depends on.** PR-1.

**Files.**

- [ ] Create `go_server/internal/storage/postgres/migrations/0007_commands.sql`.
- [ ] Create `go_server/internal/storage/postgres/commands.go`.
- [ ] Create `go_server/internal/storage/postgres/commands_test.go`.
- [ ] Edit `go_server/internal/model/command.go`.
- [ ] Edit `go_server/internal/codec/codec.go`.
- [ ] Edit `go_server/internal/gateway/server.go`.
- [ ] Edit `go_server/internal/api/server.go`.
- [ ] Edit `go_server/internal/protocol/jt808/decoder.go`.
- [ ] Edit `go_server/internal/protocol/gt06/decoder.go`.

**Build.**

- [ ] `0007_commands.sql` 建 `commands` 表，欄位是 id、device_id、type、attributes、status、result_code、sent_at、done_at。
- [ ] `model.Command` 加 `ID`、`Status`、`ResultCode` 三個欄位，狀態只有 pending、sent、done、failed。
- [ ] `handleCommand` 先寫一列 pending 再寫入連線，寫成功改成 sent，離線改成 failed 並保留現行的 409。
- [ ] `codec.Result` 加 `CommandAck` 欄位，JT808 的終端一般回應與 GT06 的 0x80 命令回應填它，gateway 收到後把對應那列改成 done 或 failed。
- [ ] 新增 `GET /api/commands`，照裝置擁有權過濾，參數是 deviceId 與 limit。

**You see.**

- [ ] 對在線裝置發命令後，`select status from commands` 由 pending 轉 sent，收到回應後轉 done。
- [ ] 對離線裝置發命令，HTTP 仍是 409，`commands` 多一列 failed。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] `commands_test.go` 涵蓋四種狀態轉移與 `GET /api/commands` 的擁有權過濾。`decoder_test.go` 各加一個 ACK 解析 case。跑 `go test ./internal/storage/postgres/... ./internal/api/... ./internal/protocol/...`。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

- [ ] Lane 1. Regression lane against trunk. 在線裝置發一次 engineStop，trunk 與 head 都回 202，且 sim 都收到同一個位元組序列。Save `lane-1.log`. Pass when 兩邊位元組相同。
- [ ] Lane 2. 發命令後立刻查 `commands`。Save `lane-2.log`. Pass when 那一列是 pending 或 sent。
- [ ] Lane 3. 離線裝置發命令。Save `lane-3.log`. Pass when HTTP 409 且新列是 failed。
- [ ] Lane 4. JT808 裝置回終端一般回應、結果 0。Save `lane-4.log`. Pass when 那一列轉 done。
- [ ] Lane 5. JT808 裝置回結果 1。Save `lane-5.log`. Pass when 那一列轉 failed 且 `result_code` 是 1。
- [ ] Lane 6. GT06 裝置回 0x80 命令回應。Save `lane-6.log`. Pass when 那一列轉 done。
- [ ] Lane 7. 送不存在的命令型別。Save `lane-7.log`. Pass when 回 400 且 `commands` 沒有新列。
- [ ] Lane 8. 用裝置擁有權過濾查 `GET /api/commands`。Save `lane-8.log`. Pass when 看不到別人的裝置的命令。
- [ ] Lane 9. 有 pending 列時重啟 gateway。Save `lane-9.log`. Pass when 重啟後列還在且沒有重送。
- [ ] Lane 10. 一秒內送 100 個命令到 100 台在線裝置。Save `lane-10.log`. Pass when 5 秒內全部到終態，`notification_dropped_total` 或 `alert_dropped_total` 沒有增加。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [ ] Metric. `POST /api/commands` 的 p95 延遲與每秒完成的命令數。
- [ ] Probe. `scripts/live-lanes.sh 10` 在 trunk 與 head 各跑五次，交錯執行。
- [ ] Baseline. 先記 trunk 的 p95，trunk 沒有 `commands` 表，所以 trunk 量的是寫入 socket 的時間。
- [ ] Rule. head 的 p95 超過 50 毫秒就失敗，每秒完成數低於 100 也失敗。

**Review gate.** None. PR-4 is not review-gated.

**Merge.**

- [ ] root 在 head SHA 給出 clean verdict。
- [ ] Bugbot 留言分類完成。
- [ ] rebase 到 `main` 後 patch-id 不變。
- [ ] owner squash-merge。

## Close the program

- [ ] 上面每一格都帶證據打勾，缺證據的格子維持未勾並在 chat 講清楚。
- [ ] 回報用執行 playbook 指定的格式，開頭重述目標，附上四支 PR 的連結與各自的 verdict。
- [ ] 把 `decisions.tsv` 補到最後一列，並跑一次自我稽核。

## Appendix A. Prototype evidence

沒有跑 prototype。這份計畫的未定問題是「headless 服務的 live lane 要產出什麼」，答案是 `.log` 與 `psql` 輸出，不是截圖。這一項在 PR-1 落地時用 lane 10 驗證，lane 10 是目前唯一能證明十條 lane 可自動化的機制。

留下的未證問題是 bcrypt cost 12 在目標硬體上的實際 p95，PR-3 的 perf 格會量到。

## Appendix B. Alternatives rejected

用 Java 版的 `traccar-master` 當 live 對照組被否決，因為它要 Gradle 與 MySQL 才能跑同一批 lane，成本遠高於寫一支 Go simulator。

讓 PR-2 與 PR-4 並行到最後才一起驗證被否決，因為兩支都會動 `codec.Result` 與 decoder，衝突會在合併時才爆出來。

把 `attributes` 與圍欄改成 REST 可維護被否決，那是另一支獨立的 PR，塞進來會讓 PR-3 與 PR-4 的檔案邊界失效。

## Appendix C. Risks

PR-1 的 benchmark 是後續三支 PR 的 perf 基準，如果它本身的量測不穩，PR-2 與 PR-4 的 5% 與 50 毫秒門檻都失去意義。owner 要看的是同一台機器上五輪的變異係數。
PR-2 風險在 Java fixture 的解讀，尤其是 `NT26` 那條分支改動哪個欄位。owner 要看的是與 `Gt06ProtocolDecoderTest` 的逐欄比對。
PR-3 風險是 bcrypt 拖慢 login，以及 migration 0006 在舊資料上的相容。owner 要看的是 lane 5 與 perf 格。
PR-4 風險是 ACK 對不上請求，JT808 的回應沒有序號可以配對，可能要用時間窗配對。owner 要看的是 lane 4 與 lane 5 在同一時間窗內只影響一列。
PR-2 刻意不搬 GT06 的 `modelNT` 分支。它改的是 `MSG_GPS_LBS_2` 與 `MSG_GPS_LBS_DRIVER` 的欄位佈局，散在 Java 的 LBS 解碼段裡，混進告警名稱的 PR 會讓兩件事的驗證糾在一起。它需要自己的 PR 與自己的 lane。

偏差一，lane 的產物是 `.log` 與 SQL 輸出，不是 `.png`，因為這個服務沒有 GUI。偏差二，`git show origin/main:` 的讀法在 repo 尚未 vendor pstack 前不成立。偏差三，PR-1 執行時所在的沙箱禁止 bind TCP port，gateway 起不來，所以 unit 與 perf 兩格有實測值，live 十格改在 operator 的機器上跑。

## Appendix D. Links and reading list

編輯 PR-2 前讀 `pstack/skills/how/SKILL.md`，先弄清 `codec.Base` 與 `gateway.Server` 的職責分界。
PR-4 的設計有爭議時讀 `pstack/skills/interrogate/SKILL.md`，讓第二個模型挑戰 ACK 配對方案。
整段期間的決策軌跡照 `pstack/skills/show-me-your-work/SKILL.md`，檔案放 repo 根的 `decisions.tsv`，預設不進 git。
Java 母本在 `traccar-master/src/main/java/org/traccar/protocol/`，GT06 與 JT808 的 model 分支都在那裡。
