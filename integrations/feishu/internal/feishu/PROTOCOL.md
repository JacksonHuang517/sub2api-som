# 飞书协议 adapter 证据与边界

核对日期：2026-10-11。只使用本地 httptest 验证，未使用真实凭据、未发送真实消息。

## 协议来源

- OAuth v3 token：[飞书官方获取 user_access_token](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/authentication-management/access-token/get-user-access-token-v3)。2026-10-12 读取官方 Markdown 确认 `POST https://accounts.feishu.cn/oauth/v3/token`，接受 JSON 请求；包含 `grant_type`、`client_id`、`client_secret`、`code` 及与授权请求一致的 `redirect_uri`。授权码有效期5分钟、仅使用一次。v2为历史接口，adapter已迁移v3。
- [飞书官方获取授权码](https://open.feishu.cn/document/authentication-management/access-token/obtain-oauth-code)。使用 `https://accounts.feishu.cn/open-apis/authen/v1/authorize`，`client_id` / `response_type=code` / `redirect_uri` / `state`。网页未完整提取，真实应用的授权可用范围、回调白名单和登录流程仍需上线前人工 smoke test。没有请求 refresh token / 离线权限，没有保存用户 token。
- [官方 Go SDK authen v1 model](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/service/authen/v1/model.go)：`GetUserInfoRespData` 提供 `open_id`（应用内标识）、`tenant_key`、`name`，不提供必需的 `app_id` claim。[resource](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/service/authen/v1/resource.go) 对应 `/open-apis/authen/v1/user_info`。因此 app namespace 来自固定应用凭据换出的 token，不能假称从 user_info 验证了必需的 app_id。若服务响应额外包含 app_id 则严格比对；输出 identity 显式携带配置 AppID，tenant_key 必须由用户信息接口实际返回并保留；当前不限定企业归属，仅在显式配置可选 TenantKey 白名单时才要求匹配。
- [官方 Go SDK token manager](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/core/tokenmanager.go) 与 [constants](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/core/constants.go)：企业自建应用 `/open-apis/auth/v3/tenant_access_token/internal` 使用 `app_id` / `app_secret`；响应 `tenant_access_token` / `expire`。进程内 mutex 缓存，提前 60 秒过期（短 token 采用生命周期 10%），请求开始时计时；不向外部存储写 token。SDK constants 确认 99991663/99991664/99991671 是 token 无效。
- [发送消息官方文档](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/create) 正文提取为空；[官方 Go SDK IM model](https://github.com/larksuite/oapi-sdk-go/blob/v3_main/service/im/v1/model.go) 的 `CreateMessageReqBody` 明确 `receive_id` / `msg_type` / 字符串化 JSON `content` / `uuid`，支持 `interactive`；`CreateMessageRespData` 提供 `message_id`。SDK 注释明确：**相同 uuid 在 1 小时内至多成功发送一条消息**。这不是永久 exactly-once 保证。
- [官方会话接口错误说明](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/list) 可读取的错误表确认 230002（机器人不在群）、230006（机器人能力未开）、230013（收件人不在可用范围，离职也可能出现）。adapter 使用 open_id 私聊。

## 保守处理

HTTP 429 分类 `rate_limit`，解析 Retry-After；HTTP 401 / token 无效清缓存，**本次不自动重发**，避免无界重试。外层 worker 可将 `token` 错误作为有限重试处理，由下一次调用重新获取 token；adapter 本身不重试。HTTP 403 / 权限错误分类 `permission`；可用范围错误分类 `recipient`。未知业务错误为 `protocol`，必须人工查证后扩展映射，不能直接当作可无限重试。

发送阶段网络错误、超时、5xx、响应截断/无法解析或成功响应缺失 message_id 都归 `uncertain`：对端可能已经收下消息。外层 outbox 必须保存固定事件 UUID 与首次发送时间；跨进程恢复、超过 1 小时去重窗口时不得据此假定可以安全自动重发。adapter 不实现 outbox 或重试调度。

OAuth token 主机固定为 accounts.feishu.cn，其他 API 主机固定为 open.feishu.cn，唯一非生产注入允许显式 HTTPClient 配合数值 loopback HTTP httptest 地址。禁止 HTTP 跳转，避免凭据被转发。按钮只取启动配置 SiteURL 的站点 origin，固定跳转 `/usage`（查看用量）与 `/profile`（管理提醒），忽略 Message.SiteURL。卡片以 SOM AI HUB 为标题，级别 1/2/3 为钱包警戒/紧急/耗尽；明确 USD，微美元使用整数精确格式化（最多 6 位小数、去尾零），数据时间采用固定 UTC+8 的 Asia/Shanghai；时间缺失时明确未提供。钱包提示不推断订阅可用性。卡片不使用 DisplayName，也不使用 Markdown，避免姓名注入。错误仅包含稳定分类，不包含原始响应、请求、token 或密钥。

## 仍需上线验证

真实用户 OAuth v3 换码、授权可见范围、回调白名单、卡片渲染及首次私聊尚未验证。2026-10-12 已通过只读 API 确认机器人启用、发送权限已授权；基础 user_info 无必需权限。mock通过不代表真实用户链路已满足，真实消息仍需明确授权的测试接收人。
