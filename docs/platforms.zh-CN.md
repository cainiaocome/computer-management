# 各平台底层细节

为什么 agent 这样设计，按操作系统分述。新增模块前请先读这里：这里多数硬限制不是
本项目的 bug，而是平台本身的工作方式。

相关文档：[`design.zh-CN.md`](design.zh-CN.md)（闸门分类）、
[`provisioning.zh-CN.md`](provisioning.zh-CN.md)（人工步骤）。

[English](platforms.md)

- [1. Windows](#1-windows)
- [2. macOS](#2-macos)
- [3. Linux](#3-linux)
- [4. 跨平台对比](#4-跨平台对比)

---

## 1. Windows

### 1.1 配置存在哪里

| 机制 | 位置 | 由谁强制 | root/SYSTEM 够吗 |
|---|---|---|---|
| 注册表（普通设置） | `HKLM\SOFTWARE\...`、`HKCU\SOFTWARE\...` | 应用自身 | HKLM 可以 |
| 管理模板 / 策略 | `HKLM\SOFTWARE\Policies\<厂商>\<应用>` | 应用在启动及周期性读取 | 可以 |
| 组策略 (GPO) | 从域控拉取，写入上述位置 | `gpsvc`，每 90 分钟 ±30 刷新 | 仅在域内 |
| 本地 GPO | `%SystemRoot%\System32\GroupPolicy` | `gpsvc` | 可以，通过 `LGPO.exe` |
| MDM (OMA-DM) | 注册到 MDM 服务端 | `dmclient` → `MDM_*` WMI 提供程序 | 需先 MDM 注册 |
| 计划任务 | `\Microsoft\Windows\...` | `schedule` 服务 | 可以 |
| 服务 | `HKLM\SYSTEM\CurrentControlSet\Services` | SCM | 可以 |

说明：

- `HKLM\SOFTWARE\Policies` 是**官方文档化**的管理入口：应用不应允许用户覆盖。
  不带 `\Policies` 的 `HKLM\SOFTWARE\<厂商>` 只是普通配置 —— 能写，但应用也随时
  可以重写它。
- **Windows 家庭版没有组策略**，也没有 AppLocker、`DefaultAssociationsConfiguration`
  和可用的 `gpedit.msc`。注册表和服务仍然可用，这也是 agent 更偏好它们的原因。
- Windows 的 MDM 需要真正的 OMA-DM 服务端（HTTPS 上的 SOAP + 证书认证）。实现
  它本身就是个大项目，本项目有意不做。

### 1.2 权限模型

- **LocalSystem (SYSTEM)** 拥有 `SeTcbPrivilege`、完整的本地访问权限，但没有用户
  配置文件。以 SYSTEM 运行的服务可以写 `HKLM` 和 `%ProgramFiles%`。
- **Windows 进程不是 Unix 意义上的 "root"。** 完整性级别、ACL 和内核过滤器仍然
  生效。SYSTEM 之上有三道真实上限：
  1. **会话 0 隔离** —— 服务不能与用户桌面交互，也不能显示 UI。按用户的操作需要
     令牌（`LogonUser` + `CreateProcessAsUser`，或在用户上下文中运行计划任务）。
  2. **受保护进程轻量级 (PPL)** —— Defender 等安全软件以受保护进程运行，即使
     SYSTEM 也无法打开或停止它们。
  3. **内核级强制策略** —— 例如 `UCPD.sys`（见下），它按**二进制身份**判定，
     而不是按权限。
- **UAC 令牌过滤**：交互式登录的管理员拿到的是*过滤后*的令牌，提权需要用户同意。
  通过网络（SMB/SSH）登录的本地账号默认也是过滤令牌，除非设置
  `HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System\LocalAccountTokenFilterPolicy=1`
  —— 这也是测试虚机里要打开它的原因。真实机器上永远不要打开。
- **Tamper Protection（防篡改）** 使得即使 SYSTEM 也无法关闭 Defender、通过注册表
  修改排除项或移除防护。请用受支持 API（`Set-MpPreference`）在装机时配置。

### 1.3 默认浏览器：`UCPD.sys`

`http`/`https`/`.pdf`/`.htm(l)`/Office 扩展名的关联保存在：

```
HKCU\Software\Microsoft\Windows\Shell\Associations\UrlAssociations\https\UserChoice
    ProgId = FirefoxURL-...
    Hash   = <基于 (SID, ProgId, 时间戳) 的 base64 哈希>
```

两道彼此独立的防线：

1. **哈希校验。** `Hash` 由用户 SID、ProgId 和时间戳推导。只写 `ProgId` 而没有
   匹配的 `Hash`，Windows 会把关联重置回去。算法未公开且随版本变化，重算哈希的
   第三方工具（`SetUserFTA`）经常失效。
2. **`UCPD.sys`**（User Choice Protection Driver，Windows 11 22H2+）。内核驱动，
   会拒绝任何非微软二进制对这些键的写入/删除/重命名/ACL 修改 —— **与权限无关，
   包括 SYSTEM**。它按映像身份识别调用者，并通过 PE 的 `OriginalFilename` 资源
   识别改名的二进制；微软还能在运行时更新它的规则，并会记录这些尝试。

对第三方二进制来说，这里**不存在可以点击的同意对话框**，所以它属于*身份闸门*：
唯一受支持的程序化路径是 MDM 的 `DefaultAssociationsConfiguration` CSP（由
`dmclient` 应用的配置 XML），而它需要专业版/企业版 + MDM。

对本项目的实际结论：`browser.default` 只做检测与提示。它根本没有写入路径，并且
有测试断言这一点。

### 1.4 Firefox 策略

`policies.json` 放在安装目录：

```
C:\Program Files\Mozilla Firefox\distribution\policies.json
C:\Program Files (x86)\Mozilla Firefox\distribution\policies.json   (32 位)
```

`HKLM\SOFTWARE\Policies\Mozilla\Firefox` 有等价的注册表形式，在安装目录不可预测时
更合适。Firefox 在启动时读取策略，改动需要重启浏览器生效。

本项目使用的关键策略键：

| 键 | 作用 |
|---|---|
| `ExtensionSettings.<id>.installation_mode = force_installed` | 扩展被安装，用户不能移除或禁用，显示为*受管理* |
| `ExtensionSettings."*".installation_mode = blocked` | 用户不能安装其他任何扩展 |
| `ExtensionSettings."*".blocked_install_message` | 尝试安装时显示的文案 |
| `ExtensionUpdate` | 保持受管扩展更新 |
| `Preferences.<pref>` | 任意首选项，用户不能覆盖 |
| `DefaultBrowserSettingEnabled` | Firefox 自行检查是否为默认浏览器并提供修复入口 |
| `DisableTelemetry`、`DisablePocket`、`DisableFirefoxAccounts` | 隐私默认值 |

"强制安装"由浏览器自身执行，因此只在策略文件可读时有效。管理员可以删除该文件；
普通用户不能。这就是整个设计：**策略 + 非管理员账号**，而不是单靠策略。

### 1.5 软件包

`winget`（App Installer）默认是用户范围。要在全局安装，agent 以 SYSTEM 运行并在
支持时加上 `--scope machine`；很多清单会忽略 scope。校验方式是
`winget list --id <id> --exact`，其退出码不可靠 —— 需要解析输出。

替代方案：`choco`/`scoop`（第三方）、`msiexec /qn` 安装 MSI，或 `Add-AppxPackage`
安装商店应用（需要用户上下文）。

### 1.6 服务

agent 用 `sc create`（或 `New-Service`）把自身安装为 `LocalSystem`、`start= auto`
的服务，并设置失败恢复策略：
`sc failure <name> reset= 86400 actions= restart/30000/...`。
注意：`SESSION 0` 中的服务不能显示 UI，也不能阻塞等待用户输入。

**服务二进制必须调用 `StartServiceCtrlDispatcher`。** 把普通控制台程序注册为服务，
SCM 会等待 30 秒后以错误 1053（"没有及时响应启动或控制请求"）失败。因此 agent 会
检测 `svc.IsWindowsService()`，并交给 `golang.org/x/sys/windows/svc` 接管；同一个
二进制在 `fleet-agent run` 以及 macOS/Linux 上仍以前台方式运行。

### 1.7 agent 在 Windows 上**做不到**的事

- 用程序设置默认浏览器（UCPD）。
- 通过注册表关闭 Defender 或添加排除项（防篡改）。
- 从服务显示 UI（会话 0）。
- 在没有模拟令牌的情况下修改按用户设置。
- 阻止 Recovery/WinRE，或阻止本地管理员删除服务。
- 在 Windows 家庭版上强制任何依赖 GPO/AppLocker 的东西。

---

## 2. macOS

### 2.1 配置存在哪里

| 机制 | 位置 | 说明 |
|---|---|---|
| 受管偏好 (MCX) | `/Library/Managed Preferences/`、`/Library/Preferences/` | root 拥有的 plist；Chrome/Edge 原生读取 |
| 普通偏好 | `~/Library/Preferences/<bundleid>.plist` | 按用户，用户可写 |
| 配置描述文件 | 安装到系统 | 只有 MDM 下发的才**不可移除** |
| launchd 任务 | `/Library/LaunchDaemons`、`/Library/LaunchAgents`、`~/Library/LaunchAgents` | 守护进程 vs 用户代理 |
| 应用包 | `/Applications/<App>.app/Contents/...` | macOS 13+ 写这里需要 App 管理授权 |
| MDM | 由 APNs 驱动的命令通道 | 需要 Apple 签发的 MDM 推送证书 |

**macOS 上 Chrome 和 Edge 明显比 Firefox 好管**：它们直接读取 root 拥有的
`/Library/Managed Preferences/com.google.Chrome.plist`，因此 root 守护进程无需修改
应用包、也不会触发 TCC 弹窗。本项目依赖的 Firefox macOS 策略则是应用包**内部**的
`policies.json` 文件。

### 2.2 权限模型，以及为什么 root 不够

macOS 在 uid 0 **之上**还有若干彼此独立的权威：

1. **SIP**（系统完整性保护，`csrutil`）。保护 `/System`、大部分 `/usr`、Apple
   签名的二进制，以及关键的 `TCC.db`。root 无法写这些路径。关闭 SIP 需要进入
   Recovery，管理型 agent 绝不能要求这样做。
2. **SSV**（签名系统卷）。系统卷是签名的 APFS 快照，root 的修改在挂载层就被拒绝。
3. **TCC**（透明度、同意与控制）。按*代码身份*授予隐私敏感能力。**TCC 同样约束
   root。** 没有完全磁盘访问权限的 root 守护进程读 `~/Library/Mail` 会得到 `EPERM`。
   `tccutil` 没有*授予*子命令，`TCC.db` 受 SIP 保护。授权只能来自：(a) 用户在系统
   设置中点击；或 (b) MDM 的 PPPC 描述文件。
4. **App 管理**（macOS 13+）：修改其他应用的包体属于一项 TCC 服务。这就是写
   `/Applications/Firefox.app/.../policies.json` 时会遇到的墙 —— 在给 agent 授予
   App 管理之前，写入会以 `EPERM` 失败。

这里相关的 TCC 服务：`完全磁盘访问`、`App 管理`、`辅助功能`、`屏幕录制`、
`ListenEvent`，以及按设备的摄像头/麦克风。每项都绑定代码签名身份，因此**重新签名
二进制会重置所有授权** —— 这就是装机清单坚持使用稳定自签名身份的原因。

### 2.3 代码签名、Gatekeeper 与 quarantine

- 内核要求 ARM64 二进制有有效签名才能执行；`codesign -s -`（临时签名）足以运行，
  但也仅此而已。
- Gatekeeper 只评估*被隔离*的文件（带 `com.apple.quarantine` 扩展属性的文件，
  由浏览器设置）。用 `curl`/`ssh` 取到的文件没有这个属性，因此这样安装的 agent
  永远不会被评估。
- 默认策略是"Apple 签发的 Developer ID **且**已公证"。要合法绕过，要么 (a) 永不
  设置 quarantine，要么 (b) 注册自定义评估规则：

  ```bash
  sudo security add-trusted-cert -d -r trustRoot -p codeSign \
       -k /Library/Keychains/System.keychain fleet-ca.pem
  sudo spctl --add --type execute --label 'Fleet CA' \
       --requirement 'certificate 1 H"<CA 证书 SHA1>"'
  ```

  `-p codeSign` 把信任范围限定在代码签名，因此即使 CA 私钥泄露也无法做 TLS
  中间人。`spctl --add` 已弃用；把 (a) 当作持久方案。
- 公证本身需要 Developer ID（$99/年）。本项目不需要它。

### 2.4 launchd：守护进程 vs 用户代理

| | LaunchDaemon | LaunchAgent |
|---|---|---|
| 路径 | `/Library/LaunchDaemons` | `/Library/LaunchAgents`、`~/Library/LaunchAgents` |
| 运行身份 | root（或指定 `UserName`） | 已登录用户 |
| 会话 | 系统级，无图形界面 | 用户的图形会话 |
| 能写 `/Library`、`/Applications` | 可以（受 TCC 约束） | 不可以 |
| 能改 LaunchServices 默认值 | 不可以（会话不对） | 可以 |
| 加载方式 | `launchctl bootstrap system <plist>` | `launchctl bootstrap gui/<uid> <plist>` |

root 无法"变成"用户的图形会话。要以某用户身份操作需要 `launchctl asuser <uid> <cmd>`，
即便如此也是按会话生效的。快速用户切换意味着可能同时存在多个会话。这就是*架构
闸门* —— 再多权限也修不好，所以本项目规划了两层（`design.zh-CN.md` §2.3）。

### 2.5 默认浏览器

默认处理程序是按用户的 `LaunchServices` 状态，保存在
`~/Library/Preferences/com.apple.LaunchServices/com.apple.launchservices.secure.plist`
的 `LSHandlers` 条目里：`LSHandlerURLScheme` 为 `https` 的那个块，其
`LSHandlerRoleAll` 就是浏览器。该 plist 是二进制格式，应使用
`plutil -convert json -o - <path>` 读取，而不是抓 `defaults read` 的文本——每个块里
还嵌着 `LSHandlerPreferredVersions` 对象，且键名是大写开头的（`LSHandlerURLScheme`，
不是 `lsHandlerURLScheme`），这两点都会让朴素文本解析出错。它**不是**受管偏好，
因此配置描述文件设不了它，root 守护进程也无法为其他用户设置。`defaultbrowser`
之类的工具是在**该用户的会话里**调用 `LSSetDefaultHandlerForURLScheme` 实现的。

结论：手工（或以该用户身份）设置一次，之后检测并提示。

### 2.6 MDM：是什么，以及代价

macOS 的 MDM 是：设备通过 HTTPS 向你的服务端登记，服务端在设备收到 APNs 推送后
由设备**主动拉取**命令。前提：

1. **MDM 推送证书。** 你生成 push CSR，交由"MDM 厂商"身份签名，上传到
   `identity.apple.com`，Apple 返回设备信任的、主题为
   `com.apple.mgmt.External.<uuid>` 的证书。如今标准 **$99/年 Apple Developer
   账号**即可申请 MDM CSR（过去 $500 企业版专属的要求已取消）。任何本地 PKI 都
   无法替代：Apple 的 APNs 网关会校验发送方。
2. **注册描述文件**，手动安装或自动下发（ADE 需要 Apple Business Manager 以及
   拥有 D-U-N-S 编号的法人实体）。

MDM 能做到而 root 做不到的事：

| 能力 | 无 MDM | 有 MDM |
|---|---|---|
| PPPC：预先批准 TCC（完全磁盘访问、屏幕录制、辅助功能） | 需用户点击 | 静默且永久 |
| 用户无法移除的配置描述文件 | 任何管理员都能移除 | 不可移除 |
| FileVault 密钥托管 | — | 可以 |
| 系统更新延迟/强制 | — | 可以 |
| 远程锁定/抹除、绕过激活锁 | — | 可以 |
| 零接触 (ADE) | — | 可以 |

对于几台家庭机器、日常用户又是非管理员的情况，装机清单已经免费覆盖了大部分。
只有当需要静默 PPPC、FileVault 密钥托管，或设备多到手工配置成为负担时，才值得
花那 $99。

### 2.7 agent 在 macOS 上**做不到**的事

- 给自己授予 TCC 权限，或修改 `TCC.db`（SIP）。
- 强制设置默认浏览器（按用户的 LaunchServices）。
- 修改 `/System`、加载未签名 kext，或为被隔离的应用绕过 Gatekeeper。
- 在没有 App 管理授权时写其他应用包内部。
- 从 root 守护进程操作用户的图形会话。
- 在有人进入 Recovery 删除 launchd 任务后继续存在（没有东西能）。

---

## 3. Linux

有意保持精简：它是服务端运行平台，也是方便的第三测试目标。

| 关注点 | 机制 |
|---|---|
| 服务 | `/etc/systemd/system` 中的 systemd 单元，`Restart=always` |
| Firefox 策略 | `/etc/firefox/policies/policies.json`（root 拥有；没有 TCC 之类的东西） |
| 软件包 | `apt-get install -y`，用 `dpkg-query -W -f='${Status}'` 校验 |
| 默认浏览器 | `xdg-settings get default-web-browser`（按用户，`~/.config/mimeapps.list`） |
| 权限 | root 真的够用，除了按用户的会话状态 |

Linux 是观察 agent 循环*真实*行为的最佳位置，因为这里什么都没挡着：没有 TCC、
没有 UCPD、没有代码签名要求。

---

## 4. 跨平台对比

| 关注点 | Windows | macOS | Linux |
|---|---|---|---|
| 实际最高权限 | SYSTEM（会话 0） | uid 0（无会话的守护进程） | uid 0 |
| root 之上的同意层 | UAC + PPL + 防篡改 | TCC + SIP + SSV | 无 |
| 身份闸门 | `UCPD.sys`（默认浏览器） | Gatekeeper（代码） | 无 |
| 按用户状态是否需要会话 | 需要（令牌、会话 0 隔离） | 需要（`launchctl asuser`） | 需要（用户 uid） |
| 强制安装浏览器扩展 | ✅ `policies.json` | ✅ 应用包内 `policies.json` | ✅ `policies.json` |
| 扩展真正不可卸载 | 策略 + 非管理员用户 | 策略 + 非管理员用户 | 策略 + 非管理员用户 |
| 强制默认浏览器 | ❌ UCPD | ❌ 按用户 LaunchServices | ⚠️ `xdg-settings`（用户级） |
| 服务管理器 | SCM（`sc.exe`） | launchd | systemd |
| 静默授予 TCC 等价权限 | 不适用 | 需要 MDM PPPC | 不适用 |
| 测试用的无人值守系统安装 | ✅ dockur/windows | ❌ 安装器是交互式的 | ✅ 容器 |
