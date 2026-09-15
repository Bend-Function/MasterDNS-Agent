# 外部 Go 探测 Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在独立 MasterDNS-Agent 项目交付可跨平台运行的 TCP/HTTP 双栈探测程序，通过 HTTPS 从 MasterDNS 拉取任务并幂等上报结果。

**Architecture:** Go 程序按配置、协议客户端、探测器、调度循环、结果缓存和发布工具分层。Agent 不持有 AWS 或 DNS 凭证，不判定多节点整体健康、不执行 IP 轮换；平台负责这些决策。独立仓库以版本化 JSON 契约和固定样例与 MasterDNS 联调。

**Tech Stack:** Go 标准库（net/http、net、crypto/tls、encoding/json、regexp、context、os）、Go test、Linux systemd、GitHub Actions。go.mod 语言基线设为 Go 1.26；开发与发布选择该语言基线或更新受支持系列的稳定工具链，并记录实际版本。不使用 CGO，不引入 AWS SDK。

**Spec:** `docs/superpowers/specs/2026-09-15-multicloud-ip-rotation-design.md`，同步自已批准的 MasterDNS 设计。

## Global Constraints

- 仓库 `/Users/funcma/Project/MasterDNS-Agent`，分支 `codex/external-health-agent`；禁止在 master 上开发、自动合并或推送。
- 平台开发位于另一个仓库的 `codex/multicloud-ip-rotation` 分支；本项目不能导入兄弟目录或读取平台数据库/Redis。
- 仅支持 TCP、HTTP/HTTPS GET/HEAD；明确 IPv4/IPv6，不实现 ICMP、远程命令执行、云写操作。
- Agent 主动通过 HTTPS 拉取任务、上报结果，无入站监听端口。Go 源码全部在本仓库。
- 一次性安装 Token 兑换为专属运行 Token；Token 不进入日志、URL、进程命令行或世界可读文件。
- 明确指定目标 IP 与地址族；HTTP Host/TLS SNI 使用任务配置。不能通过默认 DNS 解析意外探测旧地址。
- 无 IPv6 能力是 unavailable，不是 failure。过期任务不执行；上传重试复用原 taskId/leaseId。
- 有界并发、有界缓存、指数退避；平台断连不能触发本地无限探测或本地换 IP。
- 目标默认限公共单播地址；私网需本地允许列表和平台任务配置同时允许；拒绝回环、链路本地、元数据地址。
- Linux amd64/arm64 提供安装与 systemd；Windows/macOS amd64/arm64 提供前台二进制及说明。

## 当前起点与准备

仓库当前只有 README 和 initial commit。规划时 PATH 中没有 go，开始执行前先安装 Go 工具链，记录 `go version`；这是执行准备，不能把“没有 Go”记录为测试通过。

确定真实 remote 后设置 module path：`git remote get-url origin`，HTTPS/SSH URL 统一转换为 `host/owner/repository`，不猜测组织名。Go 源码使用该 module path 的 internal 包导入。

协议 v1 由 MasterDNS 平台计划 P1 定义。复制 schema、说明和 fixture 到本仓库 `protocol/v1`，保存文件 SHA-256 清单。更新协议必须两个仓库分别提交，正常构建不得联网抓取 latest 契约。

## 文件结构

| 文件/目录 | 职责 |
| --- | --- |
| `cmd/masterdns-agent/main.go` | 子命令、退出状态、信号处理 |
| `internal/config` | 配置加载、权限检查、受信任地址策略 |
| `internal/protocol` | v1 DTO、JSON 校验与固定样例 |
| `internal/client` | Token 兑换、心跳、租约、提交、HTTP 重试语义 |
| `internal/checker` | TCP/HTTP、TLS、地址族和网络范围约束 |
| `internal/spool` | 有界结果文件、原子落盘、确认删除 |
| `internal/runner` | 并发执行、任务过期、信号退出、结果提交 |
| `scripts/install.sh`、`packaging/systemd` | Linux 安装、更新、卸载与服务配置 |
| `.github/workflows` | 单元测试、交叉编译、发布校验 |
| `protocol/v1` | 跨仓库契约、样例及哈希 |

## 任务依赖

A1 → A2/A3；A3 → A4；A1 → A5；A2+A4+A5 → A6；全部 → A7。A1 依赖平台 P1 的协议快照；A6 可通过 httptest fake 平台独立运行，真实联调依赖平台 P5–P7。

## A1：可运行命令、配置与协议兼容

**Files:**
- Create: `go.mod`, `.gitignore`, `cmd/masterdns-agent/main.go`
- Create: `internal/config/config.go`, `config_test.go`
- Create: `internal/protocol/types.go`, `validate.go`, `types_test.go`
- Create: `protocol/v1/probe-agent-v1.schema.json`, `probe-agent-v1.md`, `fixtures/probe-task-v1.json`, `fixtures/probe-result-v1.json`, `SHA256SUMS`
- Modify: `README.md`

**Interfaces:** `config.Load(path string) (Config,error)`；`protocol.ValidateTask(task Task, now time.Time) error`。JSON 字段完全对应 P1，不从 Go 字段名推断 wire naming。

```go
type Config struct {
    ServerURL string `json:"serverUrl"`
    ProbeID string `json:"probeId"`
    TokenFile string `json:"tokenFile"`
    StateDir string `json:"stateDir"`
    MaxConcurrency int `json:"maxConcurrency"`
    AllowIPv4 bool `json:"allowIpv4"`
    AllowIPv6 bool `json:"allowIpv6"`
    AllowedPrivateCIDRs []string `json:"allowedPrivateCidrs"`
}
type Task struct {
    Protocol string `json:"protocol"`
    TaskID string `json:"taskId"`
    RoundID string `json:"roundId"`
    ProbeID string `json:"probeId"`
    LeaseID string `json:"leaseId"`
    AddressVersion int `json:"addressVersion"`
    ConfigVersion int `json:"configVersion"`
    Address string `json:"address"`
    Family int `json:"family"`
    Hostname string `json:"hostname,omitempty"`
    Config CheckConfig `json:"config"`
    Deadline time.Time `json:"deadline"`
    NetworkPolicy *NetworkPolicy `json:"networkPolicy,omitempty"`
}
type NetworkPolicy struct {
    AllowedPrivateCIDRs []string `json:"allowedPrivateCIDRs"`
}
```

- [ ] 初始化 module path、添加 `version`/`run --config` 子命令测试和配置解析测试；未知字段、非 HTTPS 平台地址、无并发上限、地址族错误必须拒绝。开发 httptest 的 HTTP 例外仅通过测试构造器，不成为生产配置默认。

```go
func TestRejectWrongFamily(t *testing.T) {
    task := readTaskFixture(t)
    task.Address, task.Family = "192.0.2.10", 6
    if err := ValidateTask(task, task.Deadline.Add(-time.Second)); err == nil {
        t.Fatal("accepted IPv4 address as IPv6")
    }
}
```

- [ ] 运行 `go test ./internal/config ./internal/protocol`，确认新增行为先失败。
- [ ] 实现严格 JSON 解析与 ValidateTask：UUID、版本、family、netip.ParseAddr、deadline、TCP/HTTP 必填字段。CheckConfig 对齐 P1 的 type/protocol/port/hostname/method/path/headers/expectedStatuses/expectedStatusMin/expectedStatusMax/bodyContains/bodyPattern/followRedirects/verifyTls/timeoutMs。
- [ ] 结果 DTO 包含 protocol/taskId/leaseId/addressVersion/configVersion/outcome/latencyMs/measuredAt/statusCode/errorCode；unavailable 单独值。Unix token 文件拒绝 group/other 权限，Windows 使用当前用户目录并在文档说明 ACL 限制。
- [ ] fixtures 测试使用注入 now，不依赖样例时间仍在未来；提供 `readTaskFixture(t)` 帮助函数，读取 `../../protocol/v1/fixtures/probe-task-v1.json`。
- [ ] `go test ./...` 与 `CGO_ENABLED=0 go build ./cmd/masterdns-agent` 通过；提交 A1 文件与 README。

## A2：平台客户端与专属 Token

**Files:**
- Create: `internal/client/client.go`, `enroll.go`, `retry.go`, `client_test.go`
- Modify: `cmd/masterdns-agent/main.go`, `internal/protocol/types.go`

**Interfaces:** `Client.Exchange(ctx, installToken) (Enrollment,error)`、`Heartbeat(ctx,Capabilities) error`、`Lease(ctx,capacity) (LeaseResponse,error)`、`Submit(ctx,[]protocol.Result) ([]Ack,error)`。Enrollment=probeId/runtimeToken/protocol；Ack=taskId/status（accepted/duplicate/stale/rejected）。

- [ ] httptest 测试 bearer header、拒绝跨 origin 重定向、401 停止重试、429 遵循 Retry-After、结果提交保留同一 task ID。测试日志不包含测试 Token。

```go
func TestUnauthorizedIsTerminal(t *testing.T) {
    var calls atomic.Int32
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        calls.Add(1)
        w.WriteHeader(http.StatusUnauthorized)
    }))
    defer srv.Close()
    c := newTestClient(srv.URL, "test-secret")
    _, err := c.Lease(context.Background(), 1)
    if !errors.Is(err, ErrUnauthorized) || calls.Load() != 1 { t.Fatal(err, calls.Load()) }
}
```

- [ ] `go test ./internal/client` 首次失败后，实现 P1 的四个 POST 路径；newTestClient 仅允许测试注入 HTTP transport，正常 Client 构造必须 HTTPS。
- [ ] HTTP 请求设置有限总超时与响应上限，非 JSON/超大响应明确拒绝；认证重定向禁止，服务端 5xx/网络错误交给 runner 退避，不在多层嵌套重试。
- [ ] `enroll --config` 从 stdin 或权限受限文件读取安装 Token，不接受明文命令行参数；成功后以临时文件+rename 持久化运行 Token 和 probeId。临时文件权限 0600，出错不输出 token/body。
- [ ] 时间以租约响应 serverTime 计算执行剩余预算，使用单调时钟维护剩余时长；避免本地时钟偏差让任务无限延长。
- [ ] 运行 client tests 和 `go test ./...`，提交 A2 文件。

## A3：双栈 TCP 与目标范围控制

**Files:**
- Create: `internal/checker/checker.go`, `network_policy.go`, `tcp.go`, `tcp_test.go`, `network_policy_test.go`

**Interfaces:** `Checker.Check(ctx context.Context, task protocol.Task) protocol.Result`；`NetworkPolicy.Validate(address netip.Addr, taskAllowsPrivate bool) error`；通过构造器注入 dialer 便于本地服务测试。

- [ ] 测试 TCP4/6 正常连接、拒绝连接、超时、无 IPv6 能力、IPv4-mapped IPv6、回环/链路本地/元数据地址拒绝和私网双重允许。

```go
func TestNoIPv6IsUnavailable(t *testing.T) {
    checker := newTestChecker(false)
    task := tcpTask("2001:db8::10", 6, 443)
    got := checker.Check(context.Background(), task)
    if got.Outcome != "unavailable" { t.Fatalf("got %s", got.Outcome) }
}
```

- [ ] 执行 `go test ./internal/checker` 观察失败。tcpTask 测试 helper 填充合法固定 IDs、版本 1、未来 deadline、timeoutMs=1000；不使用公网服务作为测试依赖。
- [ ] 使用 netip 规范化地址，明确 tcp4/tcp6，通过 net.JoinHostPort 构造 IPv6 host:port；不对目标 IP 再做 DNS 查询。
- [ ] 本地配置能力关闭、地址族不存在/无本地路由返回 unavailable；目标超时/拒绝在具备对应能力时返回 failure。成功返回 success 和毫秒耗时。
- [ ] 私网任务许可使用 P1 已定义的可选 networkPolicy.allowedPrivateCIDRs 字段，与本地列表求交集；补齐该字段在两个仓库的兼容测试，缺失默认不允许。回环/链路本地/metadata 即使列表覆盖也拒绝。
- [ ] Linux/macOS 支持 IPv6 的测试环境运行实际 `::1` 测试 listener；测试注入策略只用于本地测试，IPv6 不可用时记录 skip，不能误称双栈全部实测。
- [ ] 测试通过后提交 A3 文件和必要协议快照更新。

## A4：HTTP/HTTPS、SNI 与正文规则

**Files:**
- Create: `internal/checker/http.go`, `http_test.go`, `pattern.go`, `pattern_test.go`
- Modify: `internal/checker/checker.go`

**Interfaces:** `checkHTTP(ctx,task) protocol.Result` 由 Checker.Check 分派；`ValidatePattern(pattern string) error` 使用 Go regexp；检查配置来自 A1。

- [ ] 使用 httptest TLS server 测试 IP 直连、Host/SNI 域名匹配、证书失败、verifyTls=false 显式开关、GET/HEAD、状态范围/列表、正文包含和 RE2 正则。

```go
func TestRejectUnsupportedRegex(t *testing.T) {
    if err := ValidatePattern("(?<=token)ok"); err == nil { t.Fatal("accepted lookbehind") }
}
```

- [ ] `go test ./internal/checker` 观察失败；实现 Transport.DialContext 固定目标 IP/族，TLSClientConfig.ServerName 来自任务 hostname，HTTP Request.Host 同步设定。连接池按任务隔离或禁用复用，避免借旧连接跳过换址验证。
- [ ] 重定向最多 5 次，仅允许同 hostname、同端口、同 scheme 的跳转，始终固定目标 IP；禁止 HTTPS 降级或跨目标跳转。若需跨 origin，返回明确错误供平台改检查配置。
- [ ] 响应体限制 1 MiB，读取纳入任务总 deadline；超过限制且需要正文匹配时返回 body_limit_exceeded。不得在上报/日志中包含响应体或自定义敏感请求头。
- [ ] 不兼容的 regexp 返回配置错误/unavailable，不将其算成服务 failure；与平台新增 RE2 子集校验 fixture 保持一致，明确 JS lookbehind/backreference 不支持。
- [ ] 用本地注入的 CA 验证 SNI 成功/失败，无需关闭正常测试 TLS 校验。HTTP/TCP 全套测试通过后提交。

## A5：有界结果缓存与幂等确认

**Files:**
- Create: `internal/spool/spool.go`, `spool_test.go`

**Interfaces:** `Open(dir string, maxItems int, maxBytes int64) (*Spool,error)`；`Put(result protocol.Result) error`；`Batch(limit int) ([]protocol.Result,error)`；`Ack(taskID string) error`。

- [ ] 测试断电式重启后结果可读、同 taskId 不重复、原子 rename、满容量拒绝、损坏文件隔离、上传未确认不删除、accepted/duplicate/stale 可清理。

```go
func TestDuplicateDoesNotGrowSpool(t *testing.T) {
    s, err := Open(t.TempDir(), 2, 1<<20)
    if err != nil { t.Fatal(err) }
    result := testResult("11111111-1111-4111-8111-111111111111")
    if err := s.Put(result); err != nil { t.Fatal(err) }
    if err := s.Put(result); err != nil { t.Fatal(err) }
    batch, err := s.Batch(100)
    if err != nil || len(batch) != 1 { t.Fatal(err, len(batch)) }
}
```

- [ ] `go test ./internal/spool` 先失败；实现 stateDir 下受限权限结果文件，taskId 必须 UUID，不能成为路径穿越入口。testResult helper 返回 A1 合法结果 DTO。
- [ ] 默认最多 1000 条、10 MiB；容量不足停止领取新任务、优先上传已有结果，不静默删除未确认结果。损坏文件移到有界 quarantine 并记录不含 payload 的错误。
- [ ] 每次提交最多 100 个结果；仅对服务端明确确认的 taskId 执行 Ack，429/5xx/网络错误原样保留。stale 可删除，rejected 记录原因后隔离，不能反复重发造成循环。
- [ ] 缓存保留原 leaseId/版本/measuredAt；不能为了通过服务端校验替换为新的租约 ID。
- [ ] `go test -race ./internal/spool` 通过后提交。

## A6：调度主循环与平台联调

**Files:**
- Create: `internal/runner/runner.go`, `clock.go`, `runner_test.go`
- Modify: `cmd/masterdns-agent/main.go`
- Create: `tests/integration/agent_test.go`

**Interfaces:** `Runner.Run(ctx context.Context) error`；构造参数为 Client、Checker、Spool、Config 和可注入 Clock。用小接口定义 Lease/Submit/Heartbeat，便于 fake 测试，不在 runner 中直接构造 HTTP 客户端。

- [ ] fake 平台测试最大并发、重复任务只执行一次、过期任务不探测、结果上传重试不重测、401 停止领取、SIGTERM 有界退出、平台断线后缓存有界。

```go
func TestExpiredTaskIsNotExecuted(t *testing.T) {
    run := newRunnerFixture(t)
    run.Enqueue(expiredTask())
    run.Tick()
    if run.CheckCalls() != 0 { t.Fatal("executed expired lease") }
}
```

- [ ] `go test ./internal/runner ./tests/integration` 先失败；fixture 实现 fake clock/client/checker/spool 并暴露 Enqueue/Tick/CheckCalls，用有限 tick 测试，不 sleep 等真实时间。
- [ ] 运行循环顺序：提交缓存结果→心跳→按剩余容量领任务→检查期限与身份→执行→落盘→上报。默认并发 8、硬上限 64；探测总 deadline 取任务期限与 config timeout 中较短者。
- [ ] 心跳默认每 30 秒，空任务轮询默认 2 秒并遵从服务端 retryAfterMs；错误退避从 1 秒指数增长到 60 秒，加 jitter，成功后复位，401 进入需要重新注册的退出状态。
- [ ] 同 taskId 当前正在执行或已在 spool 则不重测；新 leaseId 旧结果不能嫁接。ctx 取消后停止领任务，给在飞结果最多 5 秒落盘，未上传结果留待重启。
- [ ] 使用平台 P5–P7 的隔离测试 API，验证注册→领租约→TCP/HTTPS→上报→轮次聚合。平台聚合轮次与 Agent 一次探测职责分开，不在 Agent 重试网络探测来伪造连续成功。
- [ ] `go test -race ./...` 和真实服务联调完成后提交；未具备平台环境的联调明确记录未执行。

## A7：安装、更新、交叉构建与发布

**Files:**
- Create: `scripts/install.sh`, `scripts/test-install.sh`, `scripts/build.sh`
- Create: `packaging/systemd/masterdns-agent.service`
- Create: `.github/workflows/test.yml`, `.github/workflows/release.yml`
- Create: `docs/INSTALL.md`, `docs/PROTOCOL.md`, `docs/VALIDATION.md`
- Modify: `README.md`, `cmd/masterdns-agent/main.go`

**Interfaces:** 二进制名 `masterdns-agent`，子命令 version/enroll/run/config-check。安装脚本子命令 install/status/update/uninstall；platform 发布源配置仅指向本仓库受信任的版本产物。

- [ ] 写临时目录安装测试：token 不出现在进程参数/输出，文件权限正确，checksum 失败不覆盖旧二进制，卸载默认保留配置，清除凭证需显式选项。使用测试 root 前缀，不在单元测试中修改真实 /etc 或调用实际 systemctl。

```sh
sh scripts/test-install.sh
# Expect: install permissions, rejected checksum and preserved config all pass.
```

- [ ] Linux 安装创建低权限 masterdns-agent 用户，将二进制放 /usr/local/bin，配置放 /etc/masterdns-agent，状态放 /var/lib/masterdns-agent；systemd 使用 NoNewPrivileges、ProtectSystem、限制可写目录，不要求 root 网络权限。
- [ ] 下载 HTTPS 发布清单和架构对应产物，校验 SHA-256。checksum 只能检验与发布源一致，不能当作独立签名；HTTPS 发布源为信任边界，不允许任务动态指定更新 URL。
- [ ] 更新使用临时二进制验证 version/config-check 后原子替换，服务启动失败回退旧二进制；不覆盖 Token/配置。卸载停止并移除服务，默认保留配置与缓存，明确提供清理选项。
- [ ] build.sh 使用固定输出目录 dist；构建 linux/darwin/windows × amd64/arm64 六种产物，CGO_ENABLED=0，嵌入版本/commit，Windows 加 .exe，生成 SHA256SUMS。

```sh
go test -race ./...
go vet ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o dist/masterdns-agent-linux-amd64 ./cmd/masterdns-agent
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o dist/masterdns-agent-linux-arm64 ./cmd/masterdns-agent
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -o dist/masterdns-agent-darwin-amd64 ./cmd/masterdns-agent
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -o dist/masterdns-agent-darwin-arm64 ./cmd/masterdns-agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -o dist/masterdns-agent-windows-amd64.exe ./cmd/masterdns-agent
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -o dist/masterdns-agent-windows-arm64.exe ./cmd/masterdns-agent
```

- [ ] CI 普通提交跑 test/vet/构建；只有显式发布 tag 才生成 release，开发任务不自动 push tag。Linux amd64/arm64 用实际主机/runner 验证服务安装与 TCP/IPv6；Windows/macOS 至少本地/CI 原生运行测试，交叉编译成功不写成已实机验收。
- [ ] 文档说明协议版本、运行/安装命令、IPv6 能力、权限、结果缓冲、日志、Go RE2、HTTP 重定向范围和升级兼容。记录平台/Agent 两边 commit 与协议哈希，更新 README，提交 A7 文件。

## 自检与完成条件

- P1 JSON fixture 在两个仓库通过校验，协议文档包含真实请求/响应而非只有字段名。
- A1–A7 所有测试命令有结果；记录缺少 IPv6/系统服务环境的项目，不以跳过代表通过。
- Agent 不包含 AWS SDK、云密钥处理或换址接口，不读取 MasterDNS 数据库。
- `go test -race ./...`、`go vet ./...`、六目标编译、Linux 安装测试及跨仓库探测联调完成。
- 保留分支供用户审阅，不自动合并到 master、发布 release 或安装到用户生产机器。
