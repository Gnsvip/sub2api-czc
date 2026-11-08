## 本站网址与 API 地址

本站名称：**AIFXO**。网站、登录和控制台入口：[https://www.aifxo.com/](https://www.aifxo.com/)。API 根地址：`https://api.aifxo.com`。

| 用途 | 填写地址 |
| --- | --- |
| 网站、登录和控制台入口 | `https://www.aifxo.com/` |
| API 根地址（客户端要求根地址时） | `https://api.aifxo.com` |
| Codex / OpenAI 兼容客户端的 Base URL | `https://api.aifxo.com/v1` |
| 检查模型列表的完整请求地址 | `https://api.aifxo.com/v1/models` |
| 无需密钥的连接检查 | `https://api.aifxo.com/health` |

“服务地址”也常被软件写成 `Base URL` 或 `base_url`，表示软件要连接的 API 入口。

- Codex 的 Responses 配置填写 `https://api.aifxo.com/v1`，客户端会继续拼接接口路径。
- Claude Code 等要求根地址的配置填写 `https://api.aifxo.com`，不要直接套用 Codex 的 `/v1`。
- 如果客户端自动添加 `/v1`，应按它的提示填写根地址，避免出现 `/v1/v1`。不要把 `/v1/models` 当作通用 Base URL。
- 推荐在[API Key 管理](/keys)点击“使用密钥”，选择对应客户端后复制生成的配置，并核对地址属于 `api.aifxo.com`。
- 教程中的站内链接，例如[控制台](/dashboard)、[API Key 管理](/keys)和[用量记录](/usage)，都在当前站点打开。
- 能打开网页或健康检查成功，不代表某个模型一定可用，也不保证生成速度。
