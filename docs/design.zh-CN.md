# fleet 设计文档

一个小的、自托管的计算机管理系统中，用于你拥有的少量机器：你自己的
Windows/macOS/Linux 设备，以及你提供支持的家庭成员（比如父母的笔记本）。

工作名：**fleet**（仓库：`computer-management`）。

[English](design.md)

---

## 1. 问题与目标

### 目标

1. **声明式管理。** 用策略包描述机器的期望状态，agent 收敛到该状态并上报漂移。
   没有命令式的"以 root 执行这条命令"通道。
2. **无聊但可重复的接入流程。** 每台机器一枚注册令牌、一份装机清单，之后由 agent
   长期维持状态。
3. **浏览器可控。** Firefox 作为默认浏览器，一组强制安装、用户不可卸载的扩展，
   合理的默认首选项。
4. **低运维成本的远程支持。** 能看到所有机器及其合规状态，出问题时可经网状 VPN
   接入。
5. **零持续性费用。** 不需要 Apple Developer Program、Intune、Jamf。
6. **对限制保持诚实。** 当系统拒绝某事时（Windows 11 的默认浏览器、无 MDM 时的
   TCC 授权），系统**检测并提示**，而不是静默失败或谎报成功。

### 非目标

- 不做商业 MDM。没有 ADE/零接触、没有 App Store 应用分发、没有按应用 VPN、
  没有合规证明。
- 不管理你不拥有、或未获得明确知情同意的设备。
- 不做 EDR、DLP 或监控。
- 不做通用远程 shell / RAT。这是明确排除项，见 §7。

### 成功标准

- 一台全新的 macOS 或 Windows 机器，**人工操作 ≤ 10 分钟**即可完成接入并合规
  （含无法脚本化的图形界面点击）。
- 父母的机器在系统更新、浏览器更新、用户误操作之后仍不需要求助：agent 检测到
  漂移后要么自动改回，要么给出一个可操作的单步提示。
- 服务端不持有任何能对设备执行任意代码的长期凭据。

---

## 2. 三类"闸门"（核心设计约束）

多数自研管理项目失败，是因为它们假设"有 root 就什么都能做"。并不是。每个期望
状态项都落在三类之一，实现策略完全不同。

| 闸门 | 例子 | root/SYSTEM 够吗 | 策略 |
|---|---|---|---|
| **同意闸门** | macOS TCC（完全磁盘访问、App 管理、屏幕录制、辅助功能）、Windows UAC、Gatekeeper 的"仍要打开" | ❌ 无论 uid 多少都要用户同意 | 在装机时由在场的人**一次性**花掉"同意预算" |
| **身份闸门** | Windows `UCPD.sys`（默认浏览器）、Gatekeeper 的"Apple 签发 + 公证"要求、SIP、Defender PPL | ❌ 根本不存在可点击的同意对话框 | 改变**二进制是什么**（签名、MDM、发布者），或干脆绕开 |
| **架构闸门** | Windows 会话 0 隔离、按用户注册表 hive、macOS 按用户的 LaunchServices | ❌ 不是权限问题 | 在**正确的执行上下文**里运行（两层 agent） |

### 2.1 同意闸门：一次性授予，然后保护它

同意闸门很便宜，因为运维者就在机器旁边。但授权必须能存活：

- **身份稳定。** macOS TCC 把授权绑定到**代码身份**（designated requirement），
  而不是路径。临时（ad-hoc）签名每次重建都会改变身份 → 每次更新都要重新弹窗。
  因此：**用稳定的自签名证书签名**，安装到固定路径，保持固定的 bundle 标识或
  二进制路径。
- **日常账号不是管理员。** 管理员可以重新关闭 TCC、删除守护进程、`sudo rm`
  任何东西。TCC 的修改需要管理员授权，所以**普通**日常账号无法撤销运维者用
  管理员账号做的授权。这是整个设计中杠杆率最高的一项控制。
- **系统升级。** macOS 15+ 会周期性重新询问屏幕录制权限。把每一项授权都当作
  **agent 会持续上报的合规项**，而不是一次性设置完就忘。

**没有命令行可以授予 TCC。** `tccutil` 只能重置，`TCC.db` 即使对 root 也被 SIP
保护。因此装机步骤必然是一份**人工图形界面清单**，在完成之前 agent 必须上报
`consent: missing`。

### 2.2 身份闸门：不存在同意对话框

- **Windows `UCPD.sys`**（User Choice Protection Driver，Win11 22H2+）会拒绝
  任何非微软二进制对 `http`、`https`、`.pdf`、`.htm(l)`、Office 扩展名对应
  `UserChoice` 键的写入、删除、重命名和 ACL 修改 —— **包括 SYSTEM**。它通过 PE
  的 `OriginalFilename` 资源识别被改名的二进制，并且拥有微软可动态更新的规则。
  结论：**永远不要尝试用程序设置 Windows 默认浏览器。** 装机时手工设置一次，
  之后只*检测*漂移并*提示*。
- **Gatekeeper。** 持久的方案有两种：(a) 让文件永远不带上
  `com.apple.quarantine` 扩展属性（用 `curl`/`ssh` 而不是浏览器下载）；或
  (b) 注册自定义评估规则：
  `spctl --add --type execute --requirement 'certificate 1 H"<CA 的 sha1>"'`。
  仅在钥匙串中信任自定义根证书**并不够**，且 `spctl --add` 已被标记弃用、未来
  可能移除。
- **SIP / PPL / 安全启动** 在设计上无法绕过。不要对抗它们。

### 2.3 架构闸门：在正确的上下文里运行

| 层 | 运行身份 | 能做什么 |
|---|---|---|
| **执行层** | Windows 服务（LocalSystem）、macOS `LaunchDaemon`（root） | 软件包、服务、`/Library` 与 `HKLM` 文件、本地用户、防火墙、浏览器策略文件 |
| **会话层** | 登录后的按用户进程（`LaunchAgent` / 用户登录任务） | 默认浏览器提示/设置、按用户偏好、需要 TCC 的操作、UI 提示、"受管理"托盘图标 |

root 进程无法变成已登录用户的图形会话。Windows 上 LocalSystem 位于**会话 0**，
不能显示 UI、也碰不到用户桌面；操作 `HKCU` 需要模拟令牌或用户上下文任务。macOS
上需要 `launchctl asuser <uid>`。快速用户切换意味着可能同时存在多个会话。

**规则：** 每个策略键只有一个归属层。若某项需要同意或需要会话，它属于会话层 ——
root 层不许假装自己能做。

---

## 3. 架构

```
        ┌────────────────────────────────────────────┐
        │                服务端 (Go)                 │
        │  HTTP API · 策略存储(SQLite) · 审计日志     │
        │  Ed25519 策略签名 · 注册令牌               │
        └───────────────▲───────────────▲────────────┘
                        │ HTTPS，仅 agent 主动外连
        ┌───────────────┴───────┐   ┌───┴───────────────────┐
        │  执行层               │   │  会话层                │
        │  (root / LocalSystem) │   │  (已登录用户)          │
        │  执行 + 校验           │   │  按用户范围的项        │
        └───────────────────────┘   └───────────────────────┘
                        │
              网状 VPN（Tailscale）用于支持访问 —— 可选、带外
```

- **agent 只向外拨号。** 不开放入站端口、不做 NAT 穿透、设备上不监听 socket。
  父母家里的网络零配置即可使用。
- **轮询而非推送**（约 5 分钟 + 抖动，失败后退避）。推送需要 APNs/FCM 或长连接；
  对几十台规模，一次 HTTP GET 足够。
- **服务端权威但并非全能。** 它下发*策略文档*，而不是命令。见 §7。

### 3.1 为什么用 Go

各平台单一静态二进制（`CGO_ENABLED=0`）、一流的交叉编译、标准库 `net/http` 与
`crypto/*` 已覆盖全部需要、作为 Windows 服务或 `launchd` 任务安装都很简单，且
agent 与服务端用同一语言，线协议类型可以共享同一个包。

---

## 4. 策略模型

### 4.1 强制类（enforcement class）

每个策略项都声明它如何被强制执行。这是防止 agent 谎报成功的关键。

| class | 含义 | 漂移时 |
|---|---|---|
| `enforce` | root 层可完全控制 | 静默改回，上报 `compliant`/`drifted` |
| `converge_user` | 会话层以该用户身份执行 | 登录时与每轮轮询时重新执行 |
| `nudge` | 无法用程序修改（身份闸门） | 检测后给出一键操作，上报 `action_required` |
| `bootstrap_once` | 需要人工图形界面操作（同意闸门） | 上报 `missing` + 说明，绝不声称成功 |
| `report_only` | 只观测不强制 | 只记录，不修改机器 |

### 4.2 策略包格式

策略包是规范化的 JSON，由服务端用 Ed25519 签名：

```json
{
  "schema": 1,
  "device_id": "dev_01H...",
  "generation": 7,
  "issued_at": "2026-11-24T12:00:00Z",
  "policies": [
    { "id": "firefox/base", "type": "firefox.policy", "class": "enforce",        "enabled": true, "config": {} },
    { "id": "apps/base",    "type": "packages",       "class": "enforce",        "enabled": true, "config": {} },
    { "id": "browser",      "type": "browser.default","class": "nudge",          "enabled": true, "config": {"browser":"firefox"} },
    { "id": "consent",      "type": "macos.consent",  "class": "bootstrap_once", "enabled": true, "config": {} }
  ],
  "signature": "base64(ed25519('fleet-policy-bundle-v1\n' + 去掉 signature 的规范化 JSON))"
}
```

- `generation` 对每台设备单调递增；agent 拒绝 generation 低于已应用值的策略包
  （防回滚）。
- agent 在首次注册时**钉住**服务端签名公钥（TOFU），并拒绝任何验签失败的策略包。
  不存在"接受未签名策略"的模式，连调试开关也没有。
- 策略按**组**下发（如 `parents`、`self`、`work`），支持按设备覆盖；由服务端解析
  成扁平列表后下发。

### 4.3 模块集（MVP）

| 类型 | class | 平台 | 说明 |
|---|---|---|---|
| `firefox.policy` | enforce | 全部 | 写 `policies.json`：强制扩展、锁定首选项 |
| `packages` | enforce | 全部 | `winget` / `brew` / `apt` 软件包存在性 |
| `file` | enforce | 全部 | 文件内容、权限、属主 |
| `user.account` | enforce | 全部 | 确保日常账号存在且非管理员 |
| `browser.default` | nudge | 全部 | 检测默认浏览器，打开正确的设置页面 |
| `macos.consent` | bootstrap_once | macOS | 上报 TCC 授权状态 + 说明 |
| `inventory` | report_only | 全部 | 系统版本、磁盘、已装浏览器、最后在线时间 |

所有模块都**幂等且可校验**：各自有 `Apply` 并在执行后返回**观测到的**状态，而不是
意图。

### 4.4 Firefox 细节

Firefox 策略是"无法卸载"真正可行的少数位置之一，需要两层：

1. **策略层** —— `ExtensionSettings` 中
   `"installation_mode": "force_installed"`。扩展显示为*受管理*，禁用/删除按钮
   变灰。另有 `blocked_install_message`、`Preferences`、`DisableTelemetry` 等。
2. **账号层** —— 日常用户不是管理员，因此无法编辑
   `/Applications/Firefox.app/Contents/Resources/distribution/policies.json` 或
   `HKLM\SOFTWARE\Policies\Mozilla\Firefox`。

策略文件位置：

| 系统 | 路径 |
|---|---|
| Windows | `C:\Program Files\Mozilla Firefox\distribution\policies.json`（也可用 HKLM 注册表） |
| macOS | `/Applications/Firefox.app/Contents/Resources/distribution/policies.json` —— macOS 13+ 写这里需要 **App 管理**授权 |
| Linux | `/etc/firefox/policies/policies.json` |

风险与说明：

- macOS 上该 bundle 路径在 Firefox 更新后较脆弱；逐版本验证，纳管机器建议固定
  ESR。
- macOS 上 Chrome/Edge 明显更容易（root 拥有的受管偏好 plist，无需改 app bundle、
  不触发 App 管理弹窗）。如果 macOS 上"强制扩展"比"必须是 Firefox"更重要，优选
  Chrome。这是有意记录下来的权衡。

---

## 5. 协议

全部位于 `/api/v1`，JSON。TLS 由部署负责（反向代理或网状 VPN）；agent 信任的是
策略签名。

| 方法 | 路径 | 认证 | 用途 |
|---|---|---|---|
| `POST` | `/enroll` | 注册令牌 | 用一次性令牌换取 `device_id` + `device_secret` + 签名公钥 |
| `POST` | `/checkin` | 设备密钥 | 上报观测状态，接收签名策略包 |
| `POST` | `/report` | 设备密钥 | 上报模块结果 |
| `GET` | `/admin/devices` | 管理员令牌 | 列出设备、合规状态、最后在线 |
| `POST` | `/admin/devices/{id}/policy` | 管理员令牌 | 分配组 / 覆盖项 |
| `POST` | `/admin/enrollment-tokens` | 管理员令牌 | 生成一次性令牌（TTL、可用次数） |
| `POST` | `/admin/groups/{name}/policy` | 管理员令牌 | 设置组策略 |
| `GET` | `/healthz` | 无 | 存活检查 |

- 设备密钥是 32 字节随机值，服务端只存其 SHA-256 哈希。
- 注册令牌一次性、有 TTL、只存哈希，可绑定主机名前缀。
- 所有管理操作写入追加式审计日志（`actor`、`action`、`target`、`payload`、时间）。

---

## 6. 数据模型

```
devices(id, hostname, os, os_version, arch, group_name, generation,
        enrolled_at, last_seen_at, device_secret_hash, state_json)
groups(name, policy_json, updated_at)
enrollment_tokens(token_hash, label, expires_at, used_at, max_uses, uses)
audit_log(id, at, actor, action, target, detail)
server_keys(kid, public_key, private_key_enc)   -- 实现中为磁盘上的密钥文件
```

对几十台设备，SQLite（纯 Go 驱动，无需 cgo）足够。存储层位于接口之后，将来换
Postgres 是替换而非重写。

---

## 7. 威胁模型

agent 是每台纳管机器上的 root 级守护进程。因此**服务端是整个系统中最值钱的目标**
—— 一个能执行任意 root 代码的被攻破服务端，等于把这些设备当成肉鸡的勒索软件。

按优先级排列的缓解措施：

1. **不存在命令执行通道。** API 没有"执行脚本"接口，agent 也没有可从线上触达的
   `exec` 原语。线格式只能表达*固定模块集上的声明式策略*。这是本设计中最重要
   的属性。
2. **签名 + 钉住公钥。** agent 校验策略包的 Ed25519 签名，忽略任何未签名或无法
   验证的包。传输层中间人无法注入策略。
3. **防回滚。** `generation` 必须严格递增。
4. **缩小影响面。** 模块小、可审查、可单独关闭；任何新模块默认 `report_only`。
5. **设备上不留秘密**，除设备密钥（只够取该设备自己的策略）。
6. **守护进程最小权限。** 执行层在可行处降权执行包管理器操作；不读取用户数据。
7. **可审计。** 每次变更都带前后状态回传，并在面板可见。
8. **知情同意与披露。** 纳管机器上有可见标识（菜单栏/托盘项或登录横幅），运维者
   书面说明采集了什么。对家庭机器，这是一次对话，不是一个勾选框。

明确不在范围内：防御已经取得设备 root 的攻击者（他们能卸载 agent），以及设备
所有者进入 Recovery/WinRE。"无法卸载"永远是*策略*属性（非管理员账号）与*持久性*
属性（重放循环），不是绝对保证。

---

## 8. 装机清单

这些是无法自动化的人工步骤。agent 会把每一项作为合规项上报，运维者能看到还差
什么。

### 8.1 macOS（全新机器）

1. **先创建运维者管理员账号**（Apple Silicon/FileVault：它必须先于日常账号存在，
   否则拿不到 Secure Token）。
2. 用 `curl … | sudo sh` 安装 agent（无 quarantine 属性 → 不触发 Gatekeeper），
   或经 SSH/Tailscale 安装。
3. 校验 agent 签名；若日后分发 `.app`/`.pkg`，使用自定义 CA + `spctl` 规则。
4. 在 系统设置 → 隐私与安全性 中授予：**App 管理**（写 Firefox 策略文件所需）；
   完全磁盘访问 / 屏幕录制**仅在该功能启用时**才授予。
5. 把日常账号创建为**普通**用户；除非它就是运维者账号，否则将其从 `admin` 组移除。
6. 注册：`sudo fleet-agent enroll --server https://… --token …`。
7. 手工（或以该用户身份运行 `defaultbrowser`）设置 Firefox 为默认浏览器一次。
8. 确认面板显示 `compliant`。

### 8.2 Windows（全新机器）

1. 安装为服务（`fleet-agent install`），UAC → 是。
2. 如需 Defender 排除项，用受支持 API（`Set-MpPreference`）；Tamper Protection
   会拦截注册表方式。
3. **在 设置 → 默认应用 中手工设置默认浏览器。** 绝不要用程序设置 ——
   `UCPD.sys` 会拒绝，并可能记录这次尝试。
4. 把日常账号创建为**普通**用户；保留运维者账号。
5. 注册并确认 `compliant`。

### 8.3 两者共同

- 把机器加入网状 VPN（Tailscale）用于支持访问 —— 带外，不由 agent 管理。
- 记录注册令牌标签，便于日后阅读审计日志。

---

## 9. 测试策略

- **单元测试** —— 模块的 `Apply`/校验逻辑以纯函数为主，可跨三个平台在 CI 运行。
  Firefox 策略渲染与策略包签名是带金样本的纯函数。
- **接口测试** —— 用 `httptest` 打真实服务端 + 临时 SQLite；覆盖注册、重放、
  过期令牌、错误签名、generation 回滚。
- **agent 集成** —— 在三个 runner 上用 `apply --once` + 临时文件系统根
  （`--root`），校验 agent 写了什么、上报了什么。
- **真实系统端到端**（第 2 层）—— 在真实的 Windows/macOS 上创建真实本地账号、
  写真实注册表/`/Applications`、安装真实服务/launchd，并断言 agent 的上报与实际
  一致。见 `test/e2e/README.zh-CN.md`。
- **真实机器端到端**（第 3 层）—— 用 dockur/windows 与 dockur/macos 启动虚机，
  从 ISO 全新安装后由宿主机经 SSH 驱动测试。macOS 虚拟化仅限 Apple 硬件，故仅
  本地手动运行。

CI 的非目标：在 GitHub Actions 内部启动 macOS 虚机（许可与可行性），以及 TCC/
UCPD 的完整行为（只能在真实机器上观察，由 §8 的清单覆盖）。

---

## 10. 推进计划

| 阶段 | 交付 | 完成标准 |
|---|---|---|
| 0 | 本文档、装机清单、网状 VPN | 一台父母的机器手工配置完成一次 |
| 1 | 服务端 + agent + `firefox.policy`、`packages`、`inventory`；注册；合规视图 | 全新机器 → 合规，除 §8 外无需手工配置 |
| 2 | `service` 模块、`browser.default` 提示、`macos.consent` 上报、漂移检测、审计面板 | 两个平台上，漂移能在一个轮询周期内被发现 |
| 3 | 自升级（签名清单，Sparkle 风格）、回滚、组继承 | agent 自升级且不丢失 TCC 授权 |
| 4 | 仅在确有需要时：真正的 macOS MDM（MicroMDM/Fleet）以获取 PPPC 与 FileVault 密钥托管 | — |

阶段 4 有意放在最后。Apple 的 $99/年 换来的是静默 PPPC 授权、FileVault 密钥托管
与不可移除的配置描述文件；如果只是几台家用机器且日常用户非管理员，装机清单已能
免费覆盖同样的效果。

---

## 11. 待决问题

- 纳管机器用 Firefox ESR 还是正式版（macOS 的 bundle 路径脆弱性）。
- 是否让 macOS 默认用 Chrome、其他平台用 Firefox（考虑到 macOS 上 App 管理的摩擦）。
- 在没有付费签名身份时如何做自升级信任链（Ed25519 清单 + `curl` 式安装可完全绕开
  Gatekeeper；Windows 上的 Authenticode/SmartScreen 仍是痛点）。
- 运维者管理员凭据放在哪里，以及运维者不可用时如何恢复。
