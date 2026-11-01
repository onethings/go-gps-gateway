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

- [x] Create `go_server/internal/storage/postgres/migrations/0006_users_password.sql`.
- [x] Create `go_server/internal/auth/password.go`.
- [x] Create `go_server/internal/auth/password_test.go`.
- [x] Edit `go_server/internal/api/auth.go`.
- [x] Edit `go_server/internal/api/users.go`.
- [x] Edit `go_server/internal/storage/postgres/users.go`.
- [x] Edit `go_server/go.mod`.
- [x] Edit `go_server/internal/api/server.go`, one route line for `POST /api/login`. Outside the list above;
      see Appendix C 偏差四.
- [x] Edit `go_server/internal/storage/postgres/store_test.go`, the env gated integration test for the two new
      storage methods. Outside the list above; see Appendix C 偏差五.

**Build.**

- [x] `0006_users_password.sql` 給 `users` 加 `password_hash`，預設 NULL，舊帳號不用改。
- [x] `auth.HashPassword` 與 `auth.VerifyPassword` 用 `golang.org/x/crypto/bcrypt`，cost 12，並把它從 indirect 升成直接依賴。
- [x] `POST /api/login` 收 email 與 password，成功回一組新 token，失敗回 401，停用帳號回 403。
- [x] `handleTokens` 與 `handleRevokeToken` 對非 admin 開放本人路徑，`isSelf` 已經存在，補上 scope 檢查就好。
      實作把「scope 檢查」定成 `*`：本人手上的 token 必須帶 `*` 才能列出 / 撤銷自己的 token，發 token 仍只有 admin 能做。
- [x] 任何 user JSON 都不輸出 `password_hash`。`password_hash` 只在 `auth.Credential` 裡出現，`model.User` 沒有這個欄位。

**You see.**

- [ ] `curl -s -X POST 127.0.0.1:8082/api/login -d '{"email":"a@b.c","password":"..."}'` 回 200 與 `trc_` 開頭的 token。
      Unchecked，因為這個沙箱禁止 bind TCP port，起不了 gateway。`TestLoginIssuesUsableToken` 在
      `httptest` 裡做同一件事，但那是 unit 證據，不算這一格。
- [ ] 同一支帳號打錯密碼回 401，`api_tokens` 沒有新列。
      Unchecked for the same reason. `TestLoginRejectsWrongPassword` covers the predicate in-process.

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] `password_test.go` 涵蓋雜湊可驗、錯密碼不可驗、空密碼被拒。`users_test.go` 涵蓋非 admin 只能碰自己的 token。跑 `go test ./internal/auth/... ./internal/api/... ./internal/storage/postgres/...`。
      Evidence. `go vet ./...`、`go build ./...`、`go test ./...` 全綠，`go test -race` 在三個受影響的 package 也乾淨。
      `auth`：`TestHashPasswordVerifies`（含 cost 12）、`TestHashPasswordIsSalted`、`TestHashPasswordRejectsEmpty`、
      `TestVerifyPasswordRejectsBrokenHash`、`TestHashPasswordRejectsTooLong`、`TestVerifyPasswordEmptyHashStillCosts`。`api`：`TestLoginIssuesUsableToken`（登入 token 打得到 `/api/devices`）、
      `TestLoginRejectsWrongPassword`、`TestLoginRejectsUnknownEmail`、`TestLoginRejectsAccountWithoutPassword`、
      `TestLoginDisabledAccount`（並驗證密碼錯時仍是 401）、`TestLoginRejectsMalformedRequests`（含 128 KiB body）、
      `TestTokenRoutesAllowOwnTokens`（本人 200 / 別人 403 / 窄 scope 403 / 自己發 token 403）、
      `TestUpdateUserSetsPassword`、`TestUpdateUserRejectsEmptyPassword`、`TestUpdateUserRejectsLongPassword`。`postgres`：`TestUserPasswordIntegration`，
      需要 `TRACCAR_POSTGRES_DSN`，本機沒有 DB 所以這裡只跑到 skip，列為未實測。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

這個沙箱禁止 bind TCP port，`docker` 也沒有 daemon，所以十條 lane 全部沒跑，全部維持未勾。
unit 那格用 `httptest` 蓋掉 lane 2 / 3 / 4 / 6 / 7 / 9 / 10 的判斷條件，但不算替代證據。

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

- [x] Metric. `POST /api/login` 的 p95 延遲與每個請求的 DB round trip 數。
      量到 login p50 594 ms、p95 740 ms（20 次，`httptest` 內同一 process，假 store，所以扣掉了 DB 時間）。
      DB round trip 是 2：`CredentialByEmail` 一次 SELECT，`issueToken` 一次
      `INSERT ... SELECT ... RETURNING`（`go_server/internal/storage/postgres/users.go`）。
- [ ] Probe. `ab -n 200 -c 5 -p login.json -T application/json 127.0.0.1:8082/api/login`，對 head 跑；trunk 沒有這個端點，改量 trunk 的 `GET /api/devices` 當基準。
      Unchecked，因為沒有可 bind 的 socket，`ab` 無從連起。上面那組數字是 in-process 替代，不是這條探針。
- [ ] Baseline. 先記 trunk 的 `GET /api/devices` p95。
      Unchecked as specified. 同一台機器、同一個 `httptest` 做法量到 `GET /api/devices` p95 38 µs（2000 次，假 store），
      真 DB 會更慢，但這是 head 的程式，不是 trunk 的探針。
- [ ] Rule. head 的 login p95 超過 trunk 基準的三倍就算失敗，bcrypt cost 12 是已知的固定成本，不在放寬範圍內。
      **FAIL.** 740 ms 對 38 µs 是 19,496 倍，就算把基準放大到真 DB 的 2 ms 也還是 370 倍。
      login 幾乎全部是 bcrypt cost 12 的時間，而規則不准為 bcrypt 放寬，所以這條規則用 cost 12 不可能通過。
      需要 operator 決定：接受 0.6 秒的登入並改規則，或把 cost 調低（cost 10 約為 1/4 時間），或讓 login 走別的
      驗證方式。owner 沒有自行改計畫指定的 cost。

**Review gate.** The operator reviews before merge.

未勾：gateway 起不來，三格都缺 transcript 與錄影；operator 在能 bind 的機器上跑完 lane 2 / 6 / 8 才有東西可看。

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

- [x] Create `go_server/internal/storage/postgres/migrations/0007_commands.sql`.
- [x] Create `go_server/internal/storage/postgres/commands.go`.
- [x] Create `go_server/internal/storage/postgres/commands_test.go`.
- [x] Edit `go_server/internal/model/command.go`.
- [x] Edit `go_server/internal/codec/codec.go`.
- [x] Edit `go_server/internal/gateway/server.go`.
- [x] Edit `go_server/internal/api/server.go`.
- [x] Edit `go_server/internal/protocol/jt808/decoder.go`.
- [x] Edit `go_server/internal/protocol/gt06/decoder.go`.
- [x] Edit `go_server/internal/protocol/jt808/message.go`, one constant for the `0x0001` message type。Outside the
      list above; see Appendix C 偏差六.
- [x] Edit `go_server/internal/auth/auth.go`, the `read:commands` scope。Outside the list above; see Appendix C 偏差七.
- [x] Edit `go_server/cmd/traccar-gateway/main.go`, four lines of store wiring。Outside the list above; see
      Appendix C 偏差八.

**Build.**

- [x] `0007_commands.sql` 建 `commands` 表，欄位是 id、device_id、type、attributes、status、result_code、
      sent_at、done_at，外加 `created_at`，並用 `CHECK` 把 status 限死在四個值。計畫沒列 `created_at`：
      一列 `pending` 還沒有 `sent_at`，沒有它就看不出這筆命令停了多久（見 Appendix C 偏差九）。
- [x] `model.Command` 加 `ID`、`Status`、`ResultCode` 三個欄位，狀態只有 pending、sent、done、failed。
      另外補了 `SentAt`、`DoneAt`、`CreatedAt`，因為 `GET /api/commands` 要輸出它們。
      `ResultCode` 是 `*int`：裝置回 0 與裝置沒回是兩件事，`int` 分不出來。
- [x] `handleCommand` 先寫一列 pending 再寫入連線，寫成功改成 sent，離線改成 failed 並保留現行的 409。
      編碼失敗（400）與 `devices.protocol` 為 NULL（409）都不留列：那兩種情況連 payload 都沒有。
      寫列失敗回 500 且**不送出**，否則呼叫端拿到 202 卻查不到紀錄。
- [x] `codec.Result` 加 `CommandAck` 欄位，JT808 的終端一般回應（`0x0001`）與 GT06 的 0x80 / 0x81 / 0x82
      命令回應填它，gateway 收到後把對應那列改成 done 或 failed。
- [x] 新增 `GET /api/commands`，照裝置擁有權過濾，參數是 deviceId 與 limit（另有 offset）。

**You see.**

- [ ] 對在線裝置發命令後，`select status from commands` 由 pending 轉 sent，收到回應後轉 done。
      Unchecked，因為這個沙箱禁止 bind TCP port，`docker` 也沒有 daemon，起不了 gateway 與 Postgres。
      `TestCommandStoreIntegration` 做的是同一件事，但需要 `TRACCAR_POSTGRES_DSN`，本機跑不到。
- [ ] 對離線裝置發命令，HTTP 仍是 409，`commands` 多一列 failed。
      Unchecked for the same reason. `TestCommandOfflineIsRecordedAsFailed` 在 `httptest` 裡驗了這條。

**Verify, unit.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] `commands_test.go` 涵蓋四種狀態轉移與 `GET /api/commands` 的擁有權過濾。`decoder_test.go` 各加一個 ACK
      解析 case。跑 `go test ./internal/storage/postgres/... ./internal/api/... ./internal/protocol/...`。
      Evidence. `go vet ./...`、`go build ./...`、`go test ./...` 全綠，`go test -race` 在
      `api` / `gateway` / `storage/postgres` / 兩個 protocol package 也乾淨。
      `postgres`：`TestCommandQueryDefaults`、`TestCommandQueryFilters`、`TestCommandQueryOwnership`
      （空授權清單產生 `WHERE false`）、`TestCommandQueryClamps`、`TestCreateCommandRejectsMissingDevice`，
      以及 `TestCommandStoreIntegration`（pending → sent → done、離線失敗、裝置回 1 變 failed、
      一次回應只收掉最新一列、擁有權過濾）；後者需要 `TRACCAR_POSTGRES_DSN`，本機只跑到 skip。
      `api`：`TestCommandIsRecorded`（202 且回 id）、`TestCommandOfflineIsRecordedAsFailed`、
      `TestCommandUnsupportedIsNotRecorded`、`TestCommandIsNotSentWhenRecordingFails`、`TestCommandList`
      （欄位與時間戳）、`TestCommandListIsScopedToOwnedDevices`、`TestCommandListNeedsScope`。
      `jt808`：`TestDecodeTerminalGeneralResponse`（結果 0 與 1）、`TestDecodeShortTerminalGeneralResponse`。
      `gt06`：`TestDecodeCommandReply`（0x80 / 0x81 / 0x82，且不回覆）。`gateway`：
      `TestGT06CommandReplyIsTracked`（net.Pipe 上的登入 → 0x80 → tracker 收到 device 1、code 0，
      且不再回一個 ACK）。

**Verify, live.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked. Ten lanes on `openrouter/OpenRouter 寫Code/x-ai/grok-4.7` at the PR head, per the boot recipe.

這個沙箱禁止 bind TCP port，`docker` 也沒有 daemon，`scripts/live-lanes.sh` 在 tree 裡也不存在
（PR-1 的分支還沒合進來），所以十條 lane 全部沒跑，全部維持未勾。
unit 那格用 `httptest` 與 `net.Pipe` 蓋掉 lane 2 / 3 / 4 / 6 / 7 / 8 的判斷條件，但不算替代證據。

- [ ] Lane 1. Regression lane against trunk. 在線裝置發一次 engineStop，trunk 與 head 都回 202，且 sim 都收到同一個位元組序列。Save `lane-1.log`. Pass when 兩邊位元組相同。
- [ ] Lane 2. 發命令後立刻查 `commands`。Save `lane-2.log`. Pass when 那一列是 pending 或 sent。
- [ ] Lane 3. 離線裝置發命令。Save `lane-3.log`. Pass when HTTP 409 且新列是 failed。
- [ ] Lane 4. JT808 裝置回終端一般回應、結果 0。Save `lane-4.log`. Pass when 那一列轉 done。
- [ ] Lane 5. JT808 裝置回結果 1。Save `lane-5.log`. Pass when 那一列轉 failed 且 `result_code` 是 1。
- [ ] Lane 6. GT06 裝置回 0x80 命令回應。Save `lane-6.log`. Pass when 那一列轉 done。
- [ ] Lane 7. 送不存在的命令型別。Save `lane-7.log`. Pass when 回 400 且 `commands` 沒有新列。
- [ ] Lane 8. 用裝置擁有權過濾查 `GET /api/commands`。Save `lane-8.log`. Pass when 看不到別人的裝置的命令。
- [ ] Lane 9. 有 pending 列時重啟 gateway。Save `lane-9.log`. Pass when 重啟後列還在且沒有重送。
      （程式碼上成立：gateway 啟動時不掃 `commands`，所以不會重送；但沒有實測。）
- [ ] Lane 10. 一秒內送 100 個命令到 100 台在線裝置。Save `lane-10.log`. Pass when 5 秒內全部到終態，`notification_dropped_total` 或 `alert_dropped_total` 沒有增加。

**Verify, perf.** Tests alone are not sufficient verification. A PR is verified only when its unit, live, and perf boxes are all checked.

- [x] Metric. `POST /api/commands` 的 p95 延遲與每秒完成的命令數。
      只量到 handler 這一段：`BenchmarkCommandAccepted` 在 `httptest` 裡配假 store，三輪 2000 次，
      39 µs / 45 µs / 55 µs 一次，等於 handler 上限約 18k 到 25k 次/秒，p95 遠低於 50 毫秒。
      真路徑還要加三次 DB round trip（INSERT、UPDATE sent、ack 時的 UPDATE），本機沒有資料庫量不到。
- [ ] Probe. `scripts/live-lanes.sh 10` 在 trunk 與 head 各跑五次，交錯執行。
      Unchecked，這個腳本不在 tree 裡，而且沒有可 bind 的 socket。
- [ ] Baseline. 先記 trunk 的 p95，trunk 沒有 `commands` 表，所以 trunk 量的是寫入 socket 的時間。
      Unchecked for the same reason.
- [ ] Rule. head 的 p95 超過 50 毫秒就失敗，每秒完成數低於 100 也失敗。
      Unchecked。上面那組 in-process 數字離門檻有三個數量級，但它不含資料庫，不能拿來判這條規則。

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

留下的未證問題是 bcrypt cost 12 在目標硬體上的實際 p95。PR-3 量到了：本機 i5-4200U 上 p50 594 ms、
p95 740 ms，而計畫的 3 倍規則要求它跟 `GET /api/devices` 同一個量級，所以這條規則沒過。
bcrypt cost 12 是計畫指定的值，owner 沒有改它，等 operator 決定（見 PR-3 的 perf 格）。

PR-4 留下的未證問題是「命令多三趟 DB 之後 p95 還守不守得住 50 毫秒」。本機只量到 handler 那一段：
`BenchmarkCommandAccepted` 配假 store 是 39 到 55 µs 一次。三次 round trip 的成本要真實資料庫才量得到。

## Appendix B. Alternatives rejected

用 Java 版的 `traccar-master` 當 live 對照組被否決，因為它要 Gradle 與 MySQL 才能跑同一批 lane，成本遠高於寫一支 Go simulator。

讓 PR-2 與 PR-4 並行到最後才一起驗證被否決，因為兩支都會動 `codec.Result` 與 decoder，衝突會在合併時才爆出來。

把 `attributes` 與圍欄改成 REST 可維護被否決，那是另一支獨立的 PR，塞進來會讓 PR-3 與 PR-4 的檔案邊界失效。

PR-4 用流水號配對 ACK 被否決。JT808 可以改 encoder 讓每筆命令拿一個流水號，但 GT06 / H02 / Teltonika
的回應沒有等價欄位，配對規則會變成「兩種協議兩套」，而且 encoder 不在 PR-4 的檔案邊界內。等「命令的可靠性」
那條獨立 PR 一起做（見 README 的下一步）。

## Appendix C. Risks

PR-1 的 benchmark 是後續三支 PR 的 perf 基準，如果它本身的量測不穩，PR-2 與 PR-4 的 5% 與 50 毫秒門檻都失去意義。owner 要看的是同一台機器上五輪的變異係數。
PR-2 風險在 Java fixture 的解讀，尤其是 `NT26` 那條分支改動哪個欄位。owner 要看的是與 `Gt06ProtocolDecoderTest` 的逐欄比對。
PR-3 風險是 bcrypt 拖慢 login，以及 migration 0006 在舊資料上的相容。owner 要看的是 lane 5 與 perf 格。
實測把這個風險變成了事實：login p95 740 ms，perf 規則沒過（見 PR-3 的 perf 格）。migration 0006 只加一個
可為 NULL 的欄位，舊帳號不動，但 lane 5 在沙箱裡跑不了，所以「在已經有資料的 DB 上跑 0006」仍未實測。
PR-4 風險是 ACK 對不上請求，JT808 的回應沒有序號可以配對，可能要用時間窗配對。owner 要看的是 lane 4 與 lane 5 在同一時間窗內只影響一列。
這個風險落地成事實：JT808 encoder 一律送流水號 0，GT06 回應裡的是該幀自己的序號，兩邊都沒有能對回請求的
識別碼，所以配對規則是「該裝置最新一列 `sent`」。它只會動一列，但當前一筆一直沒回應時，被回應的會是
後送的那筆（見 README「已知限制」與「下一步」第 2 條）。
PR-2 刻意不搬 GT06 的 `modelNT` 分支。它改的是 `MSG_GPS_LBS_2` 與 `MSG_GPS_LBS_DRIVER` 的欄位佈局，散在 Java 的 LBS 解碼段裡，混進告警名稱的 PR 會讓兩件事的驗證糾在一起。它需要自己的 PR 與自己的 lane。這一段後來獨立落地，見附錄 F。

偏差一，lane 的產物是 `.log` 與 SQL 輸出，不是 `.png`，因為這個服務沒有 GUI。偏差二，`git show origin/main:` 的讀法在 repo 尚未 vendor pstack 前不成立。偏差三，PR-1 執行時所在的沙箱禁止 bind TCP port，gateway 起不來，所以 unit 與 perf 兩格有實測值，live 十格改在 operator 的機器上跑。
偏差四，PR-3 動了 `go_server/internal/api/server.go` 的一行路由註冊。`Handler()` 是唯一掛路由的地方，
`handleLogin` 放在同一個 package 的另一個檔案也只能靠它註冊，所以這一行無法留在檔案邊界外。它落在
認證段，與 PR-4 會改的命令段不同行，rebase 時不會撞。
偏差五，PR-3 動了 `go_server/internal/storage/postgres/store_test.go`。新增的兩條 SQL 沒有其他行為驗證，
不補一個 env gated 的整合測試就只剩編譯檢查。這個檔案不屬於 PR-4 的檔案邊界。
偏差六，PR-4 動了 `go_server/internal/protocol/jt808/message.go`。`0x0001` 這個訊息型別常數跟其他
`MSG_*` 放在一起，塞進 decoder.go 只為了守檔案邊界會讓它變成孤兒。
偏差七，PR-4 動了 `go_server/internal/auth/auth.go`，新增 `read:commands` scope。新端點需要一個讀取
scope，借用 `read:devices` 會讓「只看命令」的 token 順便看到整個裝置清單。這是一行常數加一行 `KnownScopes`。
偏差八，PR-4 動了 `go_server/cmd/traccar-gateway/main.go`。gateway 的 ack 收尾與 REST 的寫列必須共用
同一個 store，`main.go` 是唯一把 store 交出去的地方，四行 wiring 沒有別的落點。
偏差九，`0007_commands.sql` 多了計畫沒列的 `created_at`。`pending` 那列還沒有 `sent_at`，
沒有 `created_at` 就看不出它停了多久；其他表也都有這一欄。

## Appendix D. Links and reading list

編輯 PR-2 前讀 `pstack/skills/how/SKILL.md`，先弄清 `codec.Base` 與 `gateway.Server` 的職責分界。
PR-4 的設計有爭議時讀 `pstack/skills/interrogate/SKILL.md`，讓第二個模型挑戰 ACK 配對方案。
整段期間的決策軌跡照 `pstack/skills/show-me-your-work/SKILL.md`，檔案放 repo 根的 `decisions.tsv`，預設不進 git。
Java 母本在 `traccar-master/src/main/java/org/traccar/protocol/`，GT06 與 JT808 的 model 分支都在那裡。

## Appendix E. 命令的可靠性（README「下一步」第 1 條）

附錄 B 把「用流水號配對 ACK」延後的理由是 encoder 不在 PR-4 的檔案邊界內，而且當時 GT06 / H02 /
Teltonika 沒有等價欄位。這一支獨立 PR 把三件事一起收掉。

**流水號。** `codec.SerialSource` 發 1–0xffff 的號碼，`codec.Base.Serials` 讓測試能固定它，正式路徑
用行程層級的 `codec.DefaultSerials`。JT808 encoder 每建一個幀就拿一個號碼寫進 header，並抄在
`model.Command.Serial`；`commands.serial` 記住它，終端 `0x0001` 回應的「被回應流水號」就對回那一列。
重新編碼同一筆命令沿用原本的號碼，所以重連後的復原仍然對得上。`custom` 是呼叫端自己組好的整幀，
不編號，也不參與配對。

**派送佇列與重送。** `gateway.Dispatcher` 是 `CommandTracker` 的實作，也是 REST 與 gateway 共用的
唯一狀態寫入者：寫不進去就排隊（每台裝置一份，上限 `-command-queue`），裝置握手時 flush 並把
`pending` 的列重新編碼補送，寫進連線後 `-command-retry` 內沒有回應就重寫同一份位元組，最多
`-command-attempts` 次。從沒送出去的命令另有 `-command-queue-ttl` 期限。`gateway.Server` 在綁定
session 時用選配介面 `DeviceConnectHook` 通知派送器，儲存層沒實作它就完全不受影響。

**保留期限。** `0009_commands_delivery.sql` 加 `serial` 與 `attempts`，`Store.DeleteCommandsBefore`
每小時跑一次（`-command-retention`，0 = 不清理）：結束的列看 `done_at`，沒結束的看 `created_at`。

**已驗證。** `go vet ./...`、`go build ./...`、`go test ./...` 全綠，`go test -race` 在 `gateway`、
`storage/postgres`、`jt808` 與 `metrics` 也乾淨。`dispatch_test.go` 十條：在線直送、離線排隊後
重連補送、重送到次數用完、排隊逾時、以流水號配對、無流水號時收最新一列、重啟後從 `pending`
復原、佇列滿回錯、協議沒在聽時直接失敗、一台裝置的佇列不擋另一台。`commands_test.go` 另加
`TestAcknowledgeQuery`（無資料庫）與整合測試的流水號、`attempts`、`PendingCommands`、
`DeleteCommandsBefore` 四段。`jt808` 的 `TestEncodeCommandCarriesItsOwnSerial` 蓋住編號、沿用與
`custom` 不編號。`server_test.go` 的 `TestQueuedCommandReachesAReconnectingDevice` 把三段接起來：
沒連線時下命令 → 排隊，GT06 登入 → `Relay,1#` 出現在登入 ACK 之後，裝置回 `0x80` → 佇列清空。`api` 的 `TestCommandQueuedWhenTheDeviceIsAway` 與 `TestCommandQueueFullIsRefused`
蓋住 202 `queued:true` 與 429。

**仍未驗證。** live lane 與真實 PostgreSQL 兩項照舊：這個沙箱禁止 bind TCP port，`initdb` 也被
禁止（`shmget` 不准），所以 `TestCommandStoreIntegration` 只跑到 skip，十條 lane 一列都沒跑。
「重送一次等於執行兩次」對 `rebootDevice` 這種命令是真實代價，已寫進 README 的已知限制。

## Appendix F. GT06 NT_MODELS 的 LBS2 / DRIVER 版面（README「下一步」第 2 條，原附錄 C 的延後項）

附錄 C 把這段延後的理由是「它改的是整段訊息佈局，不是只換告警名稱」。獨立成一支之後，驗證只剩
一件事：同一份位元組在 `modelNT` 與非 `modelNT` 下必須解出 Java 期望的欄位。

**改動。** `modelFlags` 多一個 `nt`，來源是 `ntModels`（`NT11` `NT20` `NT26` `NT40` `NT46` `VL100`
`XT40`）。`hasStatus` 改成吃 model：`0x22` / `0x25` 只有在 NT model 下才有狀態區。NT 分支依序
讀幀首的定位來源＋IMEI＋裝置時鐘（`decodeNTPrefix`）、一般 GPS 區塊、LBS 區塊、狀態位元組、
`decodeNTPower`（電壓 2 bytes、電池 1 byte ÷10、RSSI、告警、語言），最後 `decodeNTTrailer`
（里程 3 bytes、引擎時數 3 bytes ×60000、`0x25` 再讀駕駛卡長度＋類型＋卡號）。另外 `0x25` 的
cell id 是 4 bytes，其他訊息是 3 bytes。

**偏離 Java 的一點。** Java 的 `position.setDeviceTime(...)` 寫進 `devicetime` 欄位，Go 的
`positions` 表沒有這一欄，所以幀首那 6 個 BCD bytes 讀掉就丟。GPS 區塊本身的時間仍然是定位時間，
這一項不影響位置正確性，只是少了一個「裝置自己認為的時間」。

**已驗證。** `go test ./internal/protocol/gt06`，`go test -race` 同套也乾淨。四條測試用 Java
`Gt06ProtocolDecoderTest` 的原向量：NT20 的 `0x22`（`hours` 22140000、電池 4.0、電壓 24.36）、
XT40 的 `0x22`（電壓 12.51、里程 18059、時數 2220000）、同一份 NT20 位元組在 XT40 下（電池 4.0，
證明選路看 model 不看幀）、XT40 的 `0x25`（駕駛卡 `0235313030363538303642444603`、電壓 12.97、
cell tower 724/16/0xc80b/0x0c352c17，最後一項同時證明 4 bytes 的 cid 沒有位移）。
`TestDecodeNTLocationIsModelDriven` 用同一份位元組配非 NT model，斷言 `hours` / `odometer` /
`power` / `battery` 全部不存在 — 這是 `modelNT` 的核心行為。

**仍未驗證。** 真機。四條向量都來自 Java 測試套件，沒有 NT 裝置的實機封包；lane 也照舊跑不了
（沙箱禁止 bind port），所以「NT 裝置設了 model 之後線上解出來的欄位」只有單元層級的證據。
