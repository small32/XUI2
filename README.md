# XUI2

XUI2 将原面板拆成管理端和被控端，使用 HTTPS Web API 管理多台被控端。管理端保存账号、共享流量限额、计费方式与历史汇总；被控端运行 Xray，在本机执行账号操作并记录本机流量。两种角色使用同一份程序，由安装时写入的 `XUI_ROLE` 决定。此项目按全新部署设计，不读取旧版 xui 的远程 SSH 配置。

## 安装

支持 Linux amd64、arm64，以 root 运行：

```bash
bash <(curl -fLsS https://raw.githubusercontent.com/small32/XUI2/main/install.sh)
```

安装脚本提示选择 `1) 管理端` 或 `2) 被控端`。更新时默认保留当前角色；已有数据库不能原地切换角色。脚本优先下载 XUI2 Release；没有可用 Release 时，会从 `main` 克隆源码、下载 Go 工具链与 Xray 并在本机编译。首次安装会生成 HTTPS 证书，设置管理员用户名、密码和面板端口。

被控端安装结束会显示 API 令牌和证书 SHA256 指纹。令牌也保存在仅 root 可读的 `/etc/xui/agent.env`；证书和私钥位于 `/etc/xui/panel.crt` 与 `/etc/xui/panel.key`。在管理端的“被控端管理”填写被控端的 `https://主机:面板端口`、令牌、订阅地址和指纹。使用公开可信的证书时可不填指纹。请先确保管理端可以连接被控端的 HTTPS 面板端口。

管理端不会启动 Xray。被控端只接受管理员网页登录；端口号账号只能登录管理端，用户名为端口号、密码为该入站的服务密码。入站用户只可查看自己的账号、订阅、流量汇总、节点明细和月度历史。

## 账号、流量与重试

管理员在管理端创建、修改或删除入站后，管理端为每台启用的被控端记录独立同步任务并立即发送。失败任务留在数据库中，下一次心跳重试，页面会列出节点和错误。新增被控端时，会下发当前全部账号。被控端用管理端账号 ID 定位本地记录；修改端口时原位更新，保留已有流量。各被控端证书路径从自身面板设置提取。

默认每 10 分钟读取一次所有被控端流量，可设置更长间隔，也可在页面立即刷新。流量限额由所有被控端共同消耗；合计上传和下载达到管理端账号限额时，管理端向所有被控端下发停用。未超限时不修改启用状态。过期账号同样停用。手动停用与超限停用分别记录原因。某个节点断线、账号版本落后或数据过期时，页面显示“未确认”及最近错误；断线期间实际用量可能超过共享限额，连接恢复后会补采并处理。

按月计费的账号在每月切换时留档并重置计数；累计计费账号不会自动清零。被控端会独立保存月度原始快照，管理端断线后可通过幂等重置接口补取并汇总历史。若被控端本身跨月离线、无法还原某个月的原始流量，接口明确报错，不会把多个自然月的数据静默归入一个月。月度重置全部完成后，原先因超限停用的账号才会恢复启用。管理端数据库中的操作队列及被控端的重载标记用于处理网络故障和 Xray 重启失败。

已有节点的 API 地址不可直接修改。更换服务器时，先删除旧节点并确认远端账号清理完成，再添加新节点；修改同一服务器的证书指纹不会清空待同步任务。正常删除被控端时，管理端先删除其受管账号；如果节点不可达或还有待同步任务，节点仍保留在管理端。旧服务器已无法连接时，可使用“强制移除”，但必须自行停机或清理旧服务器上的账号；存在未完成的月度重置任务时不能强制移除，以免丢失历史流量。

## 被控端 API

API 位于被控端 HTTPS 面板的 `/api/v1`，使用独立的 `Authorization: Bearer <API令牌>`。明文 HTTP、缺失令牌和错误令牌均被拒绝。自签名证书由管理端按 SHA256 指纹校验。API 仅供管理端后端调用；浏览器不保存被控端令牌。

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/capabilities` | 检查 API 版本 |
| GET | `/inbounds` | 查询全部受管账号 |
| GET | `/inbounds/{port}` | 查询指定端口账号 |
| PUT | `/inbounds/{port}` | 按管理端账号 ID 新增或修改 |
| DELETE | `/inbounds/{port}` | 删除指定账号 |
| GET | `/traffic` | 查询各账号本机流量及状态 |
| POST | `/inbounds/{port}/disable` | 按超限或过期原因停用 |
| POST | `/inbounds/{port}/traffic/reset` | 幂等重置并返回重置前流量 |

新增或修改示例：

```http
PUT /api/v1/inbounds/34872
Authorization: Bearer <token>
Idempotency-Key: <operation-id>
Content-Type: application/json

{"managerAccountId":42,"managerRevision":"<64位配置摘要>","port":34872,"protocol":"trojan","settings":"{\"clients\":[{\"password\":\"secret\"}]}","streamSettings":"{}","sniffing":"{}","remark":"客户 A","enable":true,"total":107374182400,"monthlyReset":true}
```

流量响应示例：

```json
{"observedAt":1790121600,"inbounds":[{"accountId":42,"port":34872,"up":1200000,"down":3400000,"enabled":true,"disabledBy":"","managerRevision":"<64位配置摘要>"}]}
```

月度重置请求包含 `accountId`、上一自然月编号 `period`（如 `202609`）及唯一 `operationId`。被控端对同一月份保存一次快照，重复调用返回相同的重置前用量，不会再次清零。

## 开发

```bash
go test ./...
go build ./...
bash -n install.sh
```

Release 工作流仅在推送版本标签或手动触发时运行。`config/version` 当前为 `0.1.5`；提交到 `main` 不会自动发布 Release。
