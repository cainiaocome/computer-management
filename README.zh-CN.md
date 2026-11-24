# fleet

用声明式的方式管理一小批你自己拥有的机器 —— 自己的 Windows / macOS 机器，以及
你替家人维护的那台笔记本。

你描述期望状态（浏览器策略、软件包、哪个账号必须是普通用户），agent 负责收敛到
该状态，服务端告诉你哪里发生了漂移。

- 设计与硬性限制：[`docs/design.zh-CN.md`](docs/design.zh-CN.md)（[English](docs/design.md)）
- 各平台底层技术细节：[`docs/platforms.zh-CN.md`](docs/platforms.zh-CN.md)（[English](docs/platforms.md)）
- 装机步骤清单：[`docs/provisioning.zh-CN.md`](docs/provisioning.zh-CN.md)（[English](docs/provisioning.md)）
- 端到端测试：[`test/e2e/README.zh-CN.md`](test/e2e/README.zh-CN.md)（[English](test/e2e/README.md)）

## 能做什么

| 能力 | Windows | macOS | 实现方式 |
|---|---|---|---|
| Firefox 策略：强制安装扩展、锁定首选项 | ✅ | ✅ | `policies.json` |
| 用户无法卸载的强制扩展 | ✅ | ✅ | 浏览器策略 **+ 日常账号非管理员** |
| 把 Firefox 设为默认浏览器 | ⚠️ 一次性手动 | ⚠️ 一次性手动 | 检测漂移后提示 |
| 安装软件包 | `winget` | `brew` | 幂等 |
| 管理文件 / 配置 | ✅ | ✅ | 原子写入 |
| 确保日常账号不是管理员 | ✅ | ✅ | `user.account` |
| 上报 TCC 授权状态 | — | ✅ | `macos.consent` |
| 资产清单 + 合规状态 | ✅ | ✅ | 每 5 分钟轮询 |

不需要 Apple Developer Program，不需要 Intune，没有按设备收费。

### 刻意不做的事

- **没有远程 shell。** 协议上只承载固定模块集上的声明式策略。即使服务端被攻破，
  也无法在纳管设备上以 root 执行任意代码。
- **不"强制"默认浏览器。** Windows 11 的 `UCPD.sys` 会拒绝包括 SYSTEM 在内的非
  微软程序修改 `UserChoice` 键；macOS 上它属于按用户的会话状态。agent 只检测漂移
  并请人点一下（见 `docs/design.zh-CN.md` §2.2）。
- **不自我授予 TCC 权限。** 授权是装机时的一次性图形界面操作；agent 只上报它是否
  还在。

## 快速开始

```bash
# 1. 构建
make build

# 2. 启动控制面
export FLEET_ADMIN_TOKEN=$(openssl rand -hex 24)
./bin/fleet-server -addr :8080 -db fleet.db -key fleet-signing.key
```

```bash
# 3. 生成一次性注册令牌（1 小时有效）
curl -s -X POST localhost:8080/api/v1/admin/enrollment-tokens \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"label":"mom-laptop","ttl_seconds":3600,"max_uses":1}'
```

```bash
# 4. 定义某个组的策略
jq '{policies: .policies}' examples/parents-policy.json > /tmp/p.json
curl -s -X PUT localhost:8080/api/v1/admin/groups/parents \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d @/tmp/p.json
```

```bash
# 5. 在被管理机器上
sudo ./bin/fleet-agent enroll -server https://fleet.example.com -token <TOKEN>
sudo ./bin/fleet-agent run          # 或安装为系统服务，见下一节
```

```bash
# 6. 把设备加入分组，观察收敛
curl -s -X PUT localhost:8080/api/v1/admin/devices/<device_id> \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" \
  -d '{"group":"parents"}'
curl -s localhost:8080/api/v1/admin/devices \
  -H "Authorization: Bearer $FLEET_ADMIN_TOKEN" | jq
```

安装为系统服务：

```bash
sudo scripts/install-unix.sh -b ./bin/fleet-agent -s https://fleet.example.com -t <TOKEN>
```

```powershell
.\scripts\install-windows.ps1 -Binary .\fleet-agent.exe -Server https://fleet.example.com -Token <TOKEN>
```

## 策略模型

一个策略项是 `{id, type, class, enabled, config}`。**class** 用来保证 agent 不会
谎报结果：

| class | 含义 |
|---|---|
| `enforce` | root/SYSTEM 层完全可控；漂移会被改回 |
| `converge_user` | 由按用户的会话层执行 |
| `nudge` | 无法用程序修改，只能检测并提示 |
| `bootstrap_once` | 需要人工图形界面操作，只上报、绝不假装成功 |
| `report_only` | 只观测，不修改 |

策略按组下发、支持按设备覆盖，服务端用 Ed25519 签名并以递增的 generation 交付。
agent 在首次注册时钉住服务端公钥，拒绝任何未签名、验签失败或 generation 更旧的
策略包。

示例见 [`examples/parents-policy.json`](examples/parents-policy.json)。

## 架构

```
   服务端 (Go)                      agent (Go, root/SYSTEM)
   ├─ HTTP API            ◀──轮询── ├─ 验签，拒绝旧 generation
   ├─ SQLite 存储                  ├─ 执行模块（幂等）
   ├─ Ed25519 签名                 ├─ 校验实际结果
   └─ 审计日志            ──上报──▶└─ 上报每项状态
```

agent 只**向外**连接（不开放端口，NAT 后也能用）。策略每 5 分钟带抖动轮询一次。

## 安全设计

- 没有命令执行接口；策略只能是固定模块集。
- 策略包签名，签名私钥永不离开服务端。
- generation 严格递增（防回滚）。
- 设备密钥只存哈希；每台设备只能取自己的策略。
- 所有管理操作写入追加式审计日志。
- 未设置 `FLEET_ADMIN_TOKEN` 时管理接口整体关闭。

请把服务端放在 TLS 之后（反向代理或 Tailscale 之类的网状 VPN）。签名保证策略
完整性，TLS 保护设备密钥。

## 目录结构

```
cmd/server        控制面
cmd/agent         agent
internal/protocol 线协议 + 策略包签名
internal/policy   class/tier 词表、校验、合并
internal/agent    注册、轮询、执行、上报
internal/agent/modules  firefox、packages、file、browser.default、
                        macos.consent、user.account、inventory
internal/server   HTTP API、SQLite
deploy/           launchd、systemd、Windows 安装脚本
scripts/          安装与端到端脚本
test/e2e/         三层端到端测试（逻辑 / 真实系统 / 真实虚机）
docs/             设计、平台细节、装机清单（均有中文版）
```

## 开发

```bash
make help        # 列出目标
make test        # 单元测试（CI 在三个平台都跑）
make e2e         # 真实服务端 + 真实 agent + 真实文件
make vet fmt-check
make cross       # 交叉编译所有平台的 agent
```

CI 在 `ubuntu-latest`、`macos-latest`、`windows-latest` 上跑测试，并运行逻辑端到端、
真实系统端到端（第 2 层）与交叉编译检查。

## 状态

早期阶段。当前可用：注册、签名策略包、基于 generation 的变更检测、Firefox 策略、
软件包、文件、账号检查、默认浏览器提示、macOS 授权上报、资产清单、审计日志、
服务安装器。

下一步：agent 签名自升级、独立于 generation 的定时漂移检测、最小只读 Web 面板、
以及把 `converge_user` 模块真正放到按用户会话层执行。
