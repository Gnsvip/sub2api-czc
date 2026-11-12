# 上游整合记录（2026-11-13）

生产基线：0.2.21.3 / czc-v2026.11.13.1 / 672272e18592d72be4747a9ed1ab8185d4eb8d4f。
上游：Wei-Shaw/sub2api main 530c5e3a4f9c73ca2b0881191d0ef8e0d82c5484，包含 v0.2.21。
同步分支：`sync/upstream-20261113`；[PR #92](https://github.com/Gnsvip/sub2api-czc/pull/92)。
上游以 `cc56b1c2d` 合并提交进入同步分支，保留提交祖先关系。尚未合并 `main-czc` / `dev-czc`，未发布、未部署，VERSION 保持 0.2.21.3。部署必须再次获得用户确认。

## 整合范围

1. 错误处理：非法 UTF-8 API Key、认证取消、Grok Voice 上传分类、413 自定义透传规则、图片管理员测试参数、强制路由跳过 Responses 探测。
2. Responses 输出：非流式/缓冲转换保留推理文本，完成消息保留内容与拒绝字段。历史 reasoning.content 的本站批量恢复仍保留。
3. 资源限制：请求体内存预算、按实例内存计算默认值、上传读空闲超时、下游写超时、断连 drain、SSE 缓冲、通知工作池、有界会话缓存。本站兼容桥也保持原身份逻辑并使用有界缓存。
4. 性能：请求补丁批量处理、JSON 快速路径、RPM 与分组认证快照复用、CSV 每页 1000 条及有限 429 重试；保留本站精确 24 小时筛选。
5. 渠道兼容：OAuth 空 instructions 与回显、WebSocket HTTP bridge 指纹、Terra 描述/effort、Claude setup-token 周额度、Kimi 并发 403 短冷却、Cloudflare REST、Grok agent_message、Bedrock 帧限制。
6. 图片：GPT Image 2.6 原生链路与参数、输出兜底，账号 1K/2K/4K 尺寸范围与回退。
7. 模型与导出：Claude Fable/Mythos 5.2 目录、Bedrock 映射、Codex 默认 Sol、live web search、认证配置。保留本站模型目录发现与导出设置。目录支持不代表上游许可。
8. 计费：30m/12h 缓存写入字段、价卡、展示与完整分项；Terra 订阅长上下文豁免、Sonnet 5.5 兜底缓存读取价、免费输入 Fast 倍率。本站 Terra/Sol/Luna/Astra 基础价与倍率继续生效；已有渠道统一缓存写入价在未配置 12h 时保持统一价。
9. 支付：EasyPay 可选退款成功码，充值倍率保留六位小数。
10. 管理：OAuth 管理 MCP（默认关闭）、管理员密钥统计/额度/OAuth/邀请权限、全权兼容模式确认、邮箱精确黑名单、传输错误冷却开关。
11. 依赖与基线：Go 压缩库、vue-i18n 与覆盖依赖更新；Claude Code 内置基线更新。

## Cyber 定制适配

- 保留显式会话优先、API Key 隔离、IP/客户端 scope 门控、根 instructions 身份、模型历史约束，以及本站统一内容审核接线。
- 按真正已封禁的前缀长度计算哈希；超过 256 项不再直接返回拒绝。
- 保留原 v3 transcript 哈希。旧 scope 与新 scope 分离，旧记录在原 TTL 内按每批至多 128 个前缀精确查询，避免升级丢失封禁。
- 新长度索引最多 1024 个不同长度，超过后使用分批精确匹配；索引饱和本身不代表命中。
- 存储故障沿用 fail-open 策略；不因查询失败新增策略封禁。

## 保留的本站行为

图标、域名、教程、鹈鹕和 Astra、自动换组、充值档位、禁用思考规则、原生 OAuth/续接、reasoning.content 恢复、首字完整链路诊断和上传字节诊断均不整体覆盖。
用户关闭的首输出超时不重新开启。已删除的旧审核 worker、Promo 和影子账号逻辑不重新引入。

## 合并冲突与本站适配

- Cyber：保留 v3 历史哈希与旧封禁记录；新增长度索引和有界查询，去掉仅凭历史长度触发的误拦。
- WebSocket 转 HTTP：复用本站入口已按账号、API Key、原始请求体计算的身份，不重复应用上游新增的身份变换；请求头和 client_metadata 使用同一组 ID，设备 ID 仅保留在请求体。回归覆盖请求头/请求体会话、重复请求、不同 API Key、不同账号、off 模式，以及真实 WS 后续轮次 429 换号续接。
- 生图尺寸路由：保留底层调度器已经决定的粘性等待/逃逸，不因尺寸偏好无条件换号。
- 413：新增自定义透传规则优先级，同时保留本站默认 400 context_length_exceeded / 流式 response.failed，供客户端压缩历史。
- 上传：Grok Voice 与统一读取分类接线，继续保留本站 408 双语提示、上传字节和耗时；明确断连按原分类处理。
- 计费：新缓存 TTL 与长上下文豁免接入本站价格策略；修订上游测试的基价预期，没有更改本站倍率来迁就测试。
- OAuth：空 instructions 和回显兼容继续覆盖原生路径；测试隔离 Tibo 后台探测，避免测试响应被探测消费。
- 前端与管理：保留本站导出设置、教程、图标和 Google 服务构造链；适配 vue-i18n 新接口与 Claude Code 有效版本测试基线。
- Tibo：测试禁用入口先等待探测循环退出，避免 fixture 初始化的读写竞态，生产路由开关行为不变。

## 数据与启用方式

新增迁移 `249_mcp_oauth.sql` 与 `250_openai_cache_write_ttl.sql`，只在未来切换程序时由现有迁移流程应用。本轮没有操作生产数据库或服务。
新增管理功能不自动启用、不自动禁用已有用户、不修改已保存渠道价卡。

## 验证范围与记录

所有编译、测试、浏览器检查和打包在 GitHub Actions 执行。本地仅做源码核对与 diff 检查。最终结果汇总见 [PR #92](https://github.com/Gnsvip/sub2api-czc/pull/92)。

- [完整验证（92fdf935f）](https://github.com/Gnsvip/sub2api-czc/actions/runs/43286089277)：Google/Astra 与错误恢复专项、全部后端 unit、数据库/订阅/UsageLog TTL 集成、原生续接/Cyber/缓存/MCP/上传竞态、前端 lint/3,366 项测试/build/教程浏览器检查、迁移及管理员权限 PostgreSQL 验证，最后构建 Linux amd64 嵌入前端二进制并校验归档白名单和校验和。
- [生成与兼容专项（92fdf935f）](https://github.com/Gnsvip/sub2api-czc/actions/runs/43286049849)：Ent、Wire、gofmt 差异、全包编译、Claude 版本/上传/OAuth 回显/计费/图片尺寸/WS 桥接专项及管理员权限数据库检查。
- [首字与上传回归（92fdf935f）](https://github.com/Gnsvip/sub2api-czc/actions/runs/43286060753)：上传读取、首字诊断、流式 keepalive、HTTP/2 与传输的竞态检查。
- 后续仅调整本文和 CI 清理时，不改动上述已验证的 backend/frontend 源码；PR 上自动检查继续记录最终提交。

验证期间发现并修复过编译接线、旧测试版本/计费/提示文本预期、图片粘性等待、WS 身份重复处理和测试初始化竞态。失败记录保留在 Actions 历史，不将它们计作通过；被新提交替代而取消的运行也不计作通过。

环境跳过项需区分：常规 unit 中的 24 项专用 PostgreSQL 检查由单独的管理员权限数据库步骤补跑；其余 13 项为钉钉占位测试、本站未注册的 TypeSafe 三项、外网 TLS 三项、历史 NULL 行 SQLite 不适用项，以及需要真实上游密钥、Cyber 历史对话、Codex 流量、插件包、实时审核样本的五项。没有为补跑这些外部检查而使用生产凭据或调用线上模型。

迁移演练仅使用表结构与合成业务数据，检查重复执行幂等、历史 checksum 和合成用户/余额/订单/订阅/用量/Google 配置/品牌保留。演练基线较旧，因此记录中包含 52 个待执行迁移；本轮相对生产基线只新增上述两份迁移。

`git diff --check` 仅保留上游 `250_openai_cache_write_ttl.sql` 文件末尾空行提示；为保持迁移 checksum，不改动上游原文件字节。图标和静态品牌目录与生产基线无差异。

本轮不创建发布标签或 GitHub Release，不将验证构建安装到服务器。正式部署仍需先完成主线晋级、版本递增、发布及用户确认。
