# Astra 账号检测控制

账号列表 Astra 圆点之后显示最近检测时间，悬停显示完整日期。没有运行时数据时使用已保存的最近有效样本时间；未知结果保留原有圆点，重启后仍按已有规则展示已保存的有效历史。

点击间隔可保存逐账号常规检测间隔（1–1440 分钟），或恢复系统默认（未覆盖时通常为 10 分钟，以服务配置为准）。确认检测、限流、错误退避、并发限制和每小时检测预算仍按原规则执行，因此实际下次检测可能延后。修改间隔不会修改自动开关、历史、凭据或分组；无需数据库迁移。

“立即检测”复用现有 Astra / xhigh HTTP 探测、判定和结果保存流程。自动开关关闭时仍可手动检测一次，自动开关保持关闭。重复点击与正在运行的同账号探测合并；不绕过停用、过期、限流等账号保护。手动结果按既有结果持久化与自动换组规则处理；关闭自动检测时既有换组保护仍生效。

## 截图上方的智商检测入口

2026-11-13 只读核实：这是 Nginx 在 `/etc/nginx/sites-enabled/aifxo.com` 注入的独立 ModelTrace 插件 `/custom/modeltrace.js?v=v0.2.70`，实际文件为 `/opt/sub2api-custom/modeltrace.js`，不属于此仓库的 Astra 组件。

部署时待确认的精确变更：从该站点 `sub_filter '</body>'` 替换内容中移除以下标签，保留同一行的 `aifxo-hide-footer-links` 标签及 `</body>`：

```html
<script defer src="/custom/modeltrace.js?v=v0.2.70"></script>
```

对应最小补丁为 `deploy/remove-modeltrace-injection.patch`。先备份并核对当前 Nginx 文件，再验证 `nginx -t`、重载 Nginx，检查 `https://www.aifxo.com/` 和 `https://aifxo.com/` 的响应 HTML 不再包含该脚本。API 专用域名及品牌映射不涉及此变更。移除注入入口不等于删除独立插件的历史数据或停止其后台服务。

本 PR 只提交代码与 GitHub Actions 验证，未执行上述服务器变更，未发布或部署。
