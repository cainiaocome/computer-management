# 装机步骤

本文的步骤是**无法自动化**的部分：同意闸门（TCC、UAC）与身份闸门（`UCPD.sys`、
Gatekeeper）要么需要人工点击，要么直接拒绝第三方二进制。原理见
[`design.zh-CN.md`](design.zh-CN.md) §2。

每台机器预留约 10 分钟的人工时间。

[English](provisioning.md)

---

## 0. 动手之前

- **取得同意。** 如果机器属于别人，请说明：管理了什么、采集了什么（资产清单：
  系统版本、主机名、已装浏览器、合规状态 —— 不含个人文件）、以及如何联系你。
- **准备好运维者管理员账号。** 管理员密码由你掌握；日常用户是普通账号。这是
  浏览器策略、TCC 授权和 agent 本身能长期生效的前提。
- **服务端可达**（TLS 或网状 VPN），并已生成注册令牌：

  ```bash
  curl -s -X POST https://fleet.example.com/api/v1/admin/enrollment-tokens \
    -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
    -d '{"label":"mom-macbook","ttl_seconds":3600,"max_uses":1}'
  ```

- 如需远程支持，把机器加入网状 VPN（例如 Tailscale）。这是带外操作，不由 agent
  管理。

---

## 1. macOS

### 1.1 先创建你自己的管理员账号

在 Apple Silicon（以及开启 FileVault 时），创建账号需要 Secure Token，而第一个
账号才会拿到它。顺序：

1. 在初始设置中创建**运维者管理员**账号（你）。
2. 进入系统后再创建**日常用户**，并设为普通账号：
   系统设置 → 用户与群组 → 添加用户 → *标准*。
3. 运维者账号保留在 admin 组；日常用户必须不在。

如果日常用户是管理员，本项目几乎所有保证都会失效：他可以重新打开 TCC、删除守护
进程、移除浏览器策略。

### 1.2 安装 agent

用 `curl`/`ssh`/Tailscale 安装，**不要**用浏览器下载 —— `curl` 取到的文件永远
不会带上 `com.apple.quarantine` 属性，Gatekeeper 也就没有机会拦截。

```bash
sudo scripts/install-unix.sh -b ./fleet-agent -s https://fleet.example.com -t <TOKEN>
```

这会把二进制复制到 `/usr/local/bin/fleet-agent` 并安装
`/Library/LaunchDaemons/com.fleet.agent.plist`。

### 1.3 可选但推荐：稳定的代码签名（免费）

macOS 把 TCC 授权绑定到**代码身份**。临时（ad-hoc）签名的二进制每次重建都会改变
身份，于是每次更新后都要重新授权。请使用一个稳定的自签名身份：

```bash
# 1. 创建 CA + 代码签名叶子证书（钥匙串访问 > 证书助理，或 cfssl/openssl）。
#    叶子证书需要 EKU codeSigning（critical）。
codesign --force --sign "Fleet Code Signing" --identifier com.fleet.agent ./fleet-agent
codesign -dv --verbose=4 ./fleet-agent     # 记下 identifier / TeamIdentifier

# 2. 只对代码签名信任该 CA（不要信任为 TLS，避免中间人风险）
sudo security add-trusted-cert -d -r trustRoot -p codeSign \
     -k /Library/Keychains/System.keychain fleet-ca.pem

# 3. 允许该 CA 签名的代码执行，无需公证
sudo spctl --add --type execute --label 'Fleet CA' \
     --requirement 'certificate 1 H"<CA 证书的 SHA1>"'
spctl --assess --type execute -vv /usr/local/bin/fleet-agent
```

`spctl --add` 已弃用；若未来某个 macOS 版本移除了它，退路是继续用 `curl`/`ssh`
分发，使 quarantine 属性永远不会被设置。

### 1.4 只授予策略真正需要的权限

系统设置 → 隐私与安全性。按最小化原则授予：

| 授权 | 何时需要 | 原因 |
|---|---|---|
| **App 管理** | macOS 上的 Firefox 策略 | 写 `Firefox.app/Contents/Resources/distribution/policies.json` 属于修改其他应用包 |
| 完全磁盘访问 | 仅当某模块要读取用户数据 | 否则毫无必要，且是隐私负担 |
| 屏幕录制 | 仅当内嵌远程协助 | macOS 15+ 本来就会周期性重新询问 |

把 `/usr/local/bin/fleet-agent` 拖进"App 管理"列表（或用 `+` 添加）。这项没有
命令行：`tccutil` 只能重置，`TCC.db` 即使对 root 也被 SIP 保护。

### 1.5 注册并验证

```bash
sudo fleet-agent enroll -server https://fleet.example.com -token <TOKEN>
sudo fleet-agent status
sudo fleet-agent once -log-level debug      # 立即执行一次，不必等轮询
```

然后到服务端：

```bash
curl -s https://fleet.example.com/api/v1/admin/devices \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" | jq '.devices[] | {hostname, generation, compliance}'
```

### 1.6 设置一次默认浏览器

手工操作，或以日常用户身份：

```bash
defaultbrowser firefox     # https://github.com/mkdom/defaultbrowser
```

在此之前 `browser.default` 会一直上报 `action_required`。它永远不会静默"成功"，
因为 macOS 把默认处理程序保存在按用户的 LaunchServices 状态里。

---

## 2. Windows

### 2.1 安装为服务

在**提升权限**的 PowerShell 中：

```powershell
.\scripts\install-windows.ps1 -Binary .\fleet-agent.exe -Server https://fleet.example.com -Token <TOKEN>
```

以 LocalSystem 运行（`FleetAgent` 服务），失败自动重启。

未签名的二进制在部分机器上会触发 SmartScreen/Defender 信誉拦截。若这成为支持
负担，请用 Authenticode 签名（Azure Trusted Signing 很便宜）。

### 2.2 **手工**设置默认浏览器

设置 → 应用 → 默认应用 → Firefox → *设为默认*。

不要脚本化。Windows 11 的 `UCPD.sys` 是内核驱动，会拒绝任何非微软二进制对
`UserChoice` 键的写入、删除、重命名和 ACL 修改，**包括 SYSTEM**，并会上报这些
尝试。因此 `browser.default` 只做检测与提示。

### 2.3 把日常用户创建为普通账号

```powershell
net user mom /add
net localgroup Users mom /add
# 不要把 mom 加入 Administrators
```

如果该账号是管理员，`user.account` 会上报 `drifted`；只有策略项写了
`"demote": true` 时才会真正降权。

### 2.4 可选的 Defender 排除项

仅在 Defender 干扰软件包安装时需要。使用受支持的 API，而不要改注册表
（Tamper Protection 会拦截注册表修改，并可能将其还原）：

```powershell
Add-MpPreference -ExclusionPath "C:\Program Files\Fleet"
```

### 2.5 验证

```powershell
Get-Service FleetAgent
& "C:\Program Files\Fleet\fleet-agent.exe" status
& "C:\Program Files\Fleet\fleet-agent.exe" once -log-level debug
```

---

## 3. 如何读懂上报

| 状态 | 含义 | 处理 |
|---|---|---|
| `compliant` | 观测状态与策略一致 | 无 |
| `changed` | 刚刚执行了变更（下一轮会变成 `compliant`） | 无 |
| `drifted` | 不一致且**没有**修复（dry-run，或未启用 demote） | 检查，按需启用修复 |
| `action_required` | 身份/同意闸门：需要人点一下 | 打开 `detail.open` 里的深链 |
| `unsupported` | 平台不匹配或缺工具（如没有 `brew`） | 安装工具或删除该项 |
| `error` | 模块执行失败 | 看 `message`，再看 agent 日志 |

在 `compliant` 与 `drifted` 之间反复横跳的设备说明有别人在改它 —— 通常是用户，
或者浏览器更新替换了策略文件。这正是重放循环存在的意义。

---

## 4. 卸载 / 恢复

macOS：

```bash
sudo launchctl bootout system /Library/LaunchDaemons/com.fleet.agent.plist
sudo rm /Library/LaunchDaemons/com.fleet.agent.plist /usr/local/bin/fleet-agent
# Firefox 策略：删除你安装的 distribution/policies.json
```

Windows（提升权限）：

```powershell
.\scripts\install-windows.ps1 -Uninstall
```

如果你失去了服务端访问，设备会继续执行最后一份已签名的 generation。这是有意
设计的：策略是本地且已签名的，而不是与服务端的实时连接。

---

## 5. 故障排查

| 现象 | 原因 | 处理 |
|---|---|---|
| `rejecting policy bundle: signature is not valid` | 服务端密钥变了（新的 `-key` 文件） | 重新注册，或恢复旧密钥 |
| 上报 `unknown device` | 设备密钥被轮换，或数据库从旧备份恢复 | 重新注册 |
| Firefox 扩展仍可被移除 | 日常用户是本地管理员 | 降权；单靠策略不够 |
| 系统更新后 Firefox 策略消失 | 更新程序替换了 bundle 路径 | 下一轮轮询会自动重写；考虑固定 Firefox ESR |
| `cannot write Firefox policy: App Management consent is required` | macOS TCC | 给 agent 授予 App 管理，并保持代码身份稳定 |
| `browser.default` 一直是 `action_required` | 没人点过"设为默认" | 手工设置一次 |
| 已注册但没有策略包 | 设备不在任何组，或组里没有策略 | `PUT /api/v1/admin/devices/{id}` 带 `{"group":"..."}` |
