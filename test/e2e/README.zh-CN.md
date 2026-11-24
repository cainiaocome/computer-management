# 端到端测试

分三层，因为"端到端"的含义取决于你愿意为"真实机器"付多少代价。

| 层 | 运行位置 | 真实的部分 | 耗时 | CI |
|---|---|---|---|---|
| **1. 逻辑层** —— `scripts/e2e.sh` | 宿主机，任意系统 | 服务端、agent 二进制、HTTP、签名策略包、临时根目录中的文件 | 秒级 | 每次推送 |
| **2. 真实系统层** —— `test/e2e/guest/verify.{ps1,sh}` | 真实的 Windows / macOS 机器 | 上述全部，**加上**真实注册表、真实本地账号、真实服务/launchd、真实 Program Files / `/Applications`、真实 TCC 与 UCPD | 分钟级 | 每次推送（GitHub 托管的 runner 就是真实系统） |
| **3. 真实机器层** —— `test/e2e/vm/*/run.sh` | 启动好的 Windows 11 / macOS 虚机 | 上述全部，**加上**从 ISO 全新安装、从零配置、由宿主机经 SSH 驱动的整机 | 首次 30–60 分钟 | 手动 / 带 KVM 的自托管 runner |

第 1 层抓逻辑 bug。第 2 层抓"模块谎报了自己做了什么"。第 3 层抓"在我机器上没问题，
但全新安装的机器上不行"。

[English](README.md)

## 第 1 层 —— 逻辑端到端

```bash
bash scripts/e2e.sh        # 或: make e2e
```

启动真实服务端、生成令牌、用真实 agent 二进制注册、由服务端签名策略包、由 agent
验签并把文件写入临时根目录，然后断言幂等性以及重放的令牌会被拒绝。

## 第 2 层 —— 真实系统，不用虚机

与虚机方案运行的是同一套断言，只是省掉了虚机。需要 root（或管理员）权限，因为它
要创建用户、写 Program Files / `/Applications`、安装服务。

Linux 不支持这一层：平台相关断言只针对 Windows 和 macOS。

```bash
# macOS（root）
sudo env SERVER=http://127.0.0.1:8080 ADMIN_TOKEN=... \
  AGENT=$PWD/bin/fleet-agent INSTALLER=$PWD/scripts/install-unix.sh \
  bash test/e2e/guest/verify.sh
```

```powershell
# Windows（提升权限）
.\test\e2e\guest\verify.ps1 -Server http://127.0.0.1:8080 -AdminToken ... `
  -Agent .\bin\fleet-agent.exe -Installer .\scripts\install-windows.ps1
```

两个脚本都会自行生成注册令牌、自行分配策略，并逐项打印 PASS/FAIL。任何失败都会
以非零码退出。

检查内容（全部针对运行中的操作系统，无 mock）：

- 注册可用，且重放的令牌被拒绝
- 每个策略项都有一条上报，且状态与系统真实情况相符
- `policies.json` 出现在真实路径、可解析、确实强制安装扩展
- `user.account` 对普通用户报 `compliant`、对管理员报 `drifted` —— 并用
  `net localgroup Administrators` / `dseditgroup` 交叉验证
- `browser.default` 从不上报 `changed`；Windows 上 `UserChoice` 键在前后逐字节
  一致（证明没有任何东西尝试写它）
- 第二轮不产生任何变化：generation 相同、文件 mtime 相同
- 服务 / launchd 任务能安装、运行、干净卸载
- **不许说谎**：任何模块报 `changed`/`compliant` 时，可观测的效果必须真实存在

`verify.sh` 刻意不依赖 `python3`/`jq`：裸装的 macOS 虚机两者都没有。

## 第 3 层 —— 真实 Windows 虚机

```bash
test/e2e/vm/windows/run.sh            # 启动（或复用）并测试
RESET=1 test/e2e/vm/windows/run.sh    # 抹盘重装 Windows
DOWN=1  test/e2e/vm/windows/run.sh    # 测试完关闭虚机
```

前置条件：带 `/dev/kvm` 的 Linux 宿主机、带 compose 插件的 Docker、约 40 GB 磁盘、
`ssh`/`scp`。

工作方式：

1. `oem/install.bat` 在无人值守安装的最后一步运行，启用 OpenSSH 并写入宿主机公钥
   （`administrators_authorized_keys`）。它还会设置
   `LocalAccountTokenFilterPolicy=1`，使本地管理员的 SSH 会话是提权的 —— **仅限
   测试虚机**，真实机器永远不要这么做。
2. 宿主机在 `:8080` 启动 fleet 服务端（虚机内通过 `host.lan:8080` 访问），编译
   `fleet-agent.exe` 并放到 `shared/`（虚机内是 `Z:`）。
3. `run.sh` 等待 SSH，在虚机内运行 `verify.ps1`，并把
   `C:\fleet-e2e-results.json` 拷回来。

首次启动会下载约 8 GB 并安装 Windows；普通硬件上预计 20–60 分钟。之后复用
`storage/`，约一分钟即可启动。

## 第 3 层 —— 真实 macOS 虚机

dockur/macos **无法无人值守安装**：安装器需要人在 Web 控制台里操作
（磁盘工具 → 抹掉 → 重装 → 创建账号）。因此系统安装是一次性人工步骤，之后全部
脚本化：

```bash
test/e2e/vm/macos/run.sh --prepare   # 启动虚机并打印控制台地址
# ……在控制台里完成安装，然后在虚机的"终端"里执行：
#     sudo -S mount_9p shared
#     bash /Volumes/shared/bootstrap-guest.sh
test/e2e/vm/macos/run.sh             # 此后全自动
```

`bootstrap-guest.sh` 会打开"远程登录"、安装宿主机公钥，并授予免密 sudo（仅限一次
性虚机）。

**许可提醒：** Apple 只允许在 Apple 硬件上虚拟化 macOS。不要把这一层放进 CI；
请改用真实 Mac 或 Apple 托管的 runner。

## 为什么不把 macOS 虚机放进 CI

GitHub 托管的 `macos-latest` runner *本身就是* Apple 硬件虚拟化上的真实 macOS，
所以第 2 层可以跑在那里。在 Linux runner 里再套一层 macOS 虚机既慢又有许可问题。

## 如何新增一项检查

1. 在 `internal/agent/modules` 中实现模块行为。
2. 加单元测试（快速、全平台）。
3. 若模块跨平台，则同时给 `verify.ps1` **和** `verify.sh` 加断言：第 2 层的意义
   就在于"平台同意你的说法"。
4. 若断言依赖人工点击（同意闸门或身份闸门），就断言*上报的状态*而不是最终状态，
   并确保当效果不存在时状态永不为 `compliant`。
