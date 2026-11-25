# Web 界面

[English](./web-ui.md) | 简体中文

控制面自带一个小型 Web 界面。它与 JSON 管理 API 使用同一个二进制、同一个数据库、
同一批策略对象：界面是控制面的另一种视图，而不是第二个控制面。

## 为什么监听在另一个端口

agent 需要从笔记本所在的任何网络访问服务端，因此 `FLEET_ADDR`（默认 `:8080`）
常常是暴露在外的。管理 API 与界面则不是：它们能改动每台机器的策略，因此监听
`FLEET_ADMIN_ADDR`（默认 `127.0.0.1:8081`）。

```bash
FLEET_ADMIN_TOKEN=$(openssl rand -hex 24) \
  ./fleet-server -addr :8080 -admin-addr 127.0.0.1:8081
# 界面：  http://127.0.0.1:8081/ui/
# agent： http://<host>:8080
```

通过 SSH 端口转发或 Tailscale 访问。如果把 `FLEET_ADMIN_ADDR` 设为空字符串或与
`FLEET_ADDR` 相同，两者会在同一端口提供服务，服务端会打印警告——那是 1.0 之前的
布局，仅为兼容保留，不是推荐做法。

## 登录

管理 token 是唯一的凭据。它会被换成会话 cookie，绝不保存在浏览器里：

- `HttpOnly`，JavaScript 读不到
- `SameSite=Strict`，其他站点无法发起会改变状态的请求
- 请求经 TLS 到达时自动加 `Secure`
- 12 小时过期，用由管理 token 派生的密钥做 HMAC 签名，因此轮换 token 会让所有会话
  失效

会改变状态的表单提交还会额外校验 `Origin` 头。

## 它能做什么

| 页面 | 用途 |
|---|---|
| **Devices** | 每台已注册机器、所属组、策略 generation、最后心跳，以及按状态计数。`attention` 表示至少一项是 `drifted`、`action_required` 或 `error`；`stale` 表示没有报告，或 7 天没有心跳 |
| **Device** | 每个策略项的最新报告及 detail JSON；分配组；按设备的覆盖 JSON（保存前先校验）；**撤销**，即删除设备，使其密钥在下一次心跳时失效 |
| **Groups** | 策略包，以 JSON 编辑，走与 API 相同的 `policy.Validate` 校验（未知类型、与执行层级不符的强制类、启用但无 config 的项） |
| **Enrolment** | 生成带标签、有效期与可用次数的注册 token。明文只显示一次：库里只存 SHA-256 哈希 |
| **Audit** | 最近 200 条：登录、策略变更、token 生成、撤销 |

界面所做的每件事都是已有的控制面操作，因此都会以 actor `ui` 记入审计日志，无论
来自界面还是 API 都同样可见。

## 它刻意不做什么

- **不执行命令。** 没有"运行脚本"按钮、没有控制台、没有文件浏览器。线协议只承载
  声明式策略项；一个能执行命令的界面会重新制造本设计要避免的失败模式（被攻破的
  服务端仍能做什么，见 `design.md` §7.1）。
- **没有"立即生效"。** 保存策略会提升 generation，每台设备在下次心跳时取走。其他
  做法需要 agent 并不具备的推送通道。
- **撤销不等于擦除。** 它删除设备记录，使密钥失效。机器保留当前状态。
- **没有多用户。** 只有一个管理身份，没有角色、没有 2FA、没有按组管理员；对家庭
  机群来说这是有意的非目标。

## 实现说明

服务端渲染 HTML（`html/template`）加一份小样式表，用 `//go:embed` 打包。没有构建
步骤、没有 npm、没有 JavaScript 框架，也没有内联脚本——CSP 为
`default-src 'none'; style-src 'self'; form-action 'self'`，因此主机名或策略消息里
被注入的字符串无法执行。

也评估过独立 SPA 方案，最终否决：它需要给管理 API 加 CORS、把 token 放进浏览器
存储、多一个待部署产物和一套构建工具链，而这个界面只有五个表格和两个文本框。

文件：

```
internal/server/session.go              cookie 签名、常量时间 token 比较、Origin 校验
internal/server/webui.go                处理器、视图模型、模板注册表
internal/server/webui/templates/*.html  每页一个文件，外加 layout
internal/server/webui/static/app.css    全部样式
internal/server/webui_test.go           认证、CSRF、校验、token 只显示一次
```

## 需要知道的限制

- 没有 CSRF token：`SameSite=Strict` 加 `Origin` 校验覆盖了浏览器场景；token 只能
  再多防一层同站子域。
- 界面假定 TLS 由反向代理终止。请在前面终止 TLS，或使用 SSH 隧道。
- 策略项的 `detail` JSON 以转义形式渲染，安全但对超大报告不够美观。
