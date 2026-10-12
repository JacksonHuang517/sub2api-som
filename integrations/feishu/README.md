# SOM 飞书集成

独立 Go + SQLite 单活服务。生产开关默认关闭，真实发送另有用户白名单；不写业务库，不参与推理/计费。相关后端桥接需要独立升级授权。

本地：

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o build/som-feishu ./cmd/feishu
FEISHU_MODE=mock ./build/som-feishu
```

另一终端进入 `../../frontend`：

```sh
pnpm exec vite --config vite.feishu-preview.config.ts
```

访问 http://127.0.0.1:5186/ 。Mock 只能监听回环，余额与姓名均虚构，发送器不会调用真实飞书。生产构建不包含前端模拟入口；sidecar 只有显式 mock 模式启用模拟路由，systemd 固定 production。

详细架构、契约、测试与上线边界见工作区 `docs/handoff/som-feishu-integration/`。生产变量名称见 `.env.example`，不要将 secret 写入此文件。

## 订阅用量提醒

钱包余额与订阅额度独立开关、独立保存。订阅默认关闭，默认在 **已用 80% / 95% / 100%** 提醒；警戒与紧急只能是 `0 < warning_percent < critical_percent < 100` 的整数。

- `GET /api/v1/integrations/feishu/subscriptions/:user_id` 为 SOM 只读桥，使用现有 `FEISHU_BALANCE_READ_KEY`，不得对外暴露；单次读取包含该用户全部订阅及 daily/weekly/monthly 窗口。
- sidecar `GET/PUT /integrations/feishu/api/subscription-preferences` 使用现有短期 session proof、PUT Origin 守卫和版本乐观锁；`GET /me` 增加 `subscription_preferences`，`GET /subscriptions` 只返回会话本人快照。读取失败返回不可用，绝不补零。
- 仅监控 active、未到期、已激活且有限额的窗口。每日、每周、每月分别持久去重；真实起点变化重置周期。规则变更取消旧队列并保留已观察严重等级，同等级不会因编辑规则重发。
- `subscription_preferences`、`subscription_snapshots`、`subscription_states`、`subscription_outbox` 为新表，启动幂等建表；旧钱包表、旧钱包插入协议和现有绑定保持兼容。解绑与 namespace 更换同事务关闭订阅提醒并取消待发队列。
- 订阅扫描与 drain 有独立循环。发送前在与解绑/禁用串行的有界事务内重读完整快照，重新核对用户、订阅、窗口、额度、当前等级、身份及规则。过期/暂停/无限/消失/重置/恢复/升级均取消旧事件。错误与乱序快照不能改变用量状态；瞬时失败最多五次，崩溃过期 lease 和不确定发送只记 uncertain、不自动复制消息。
- `/admin/status` 增加 `subscription_outbox` 状态统计及 `subscription_*` 指标。订阅消息使用 text；组名中的飞书 mention 语法与控制符被中和，链接固定为 `/subscriptions` 与 `/profile`。钱包 interactive 卡片不变。

Mock 订阅是纯本地固定 fixture：每日 $100 限额、$81 已用，每周/月无限额；无真实网络和发送。发布、真实授权与消息端到端验证未在本地测试中执行。
