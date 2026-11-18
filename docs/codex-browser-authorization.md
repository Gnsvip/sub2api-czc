# Codex 自动授权浏览器

普通 Codex 的 `local_worker` 密码/TOTP 任务使用独立的 Chrome 会话，直接进入官方
OAuth 页面，再完成密码、TOTP、工作区确认和 PKCE 回调。Excel/BPS/SIWC 与
Session Studio 保留原有流程。

## 运行入口

主程序嵌入 `backend/internal/reauthruntime/browser/` 两个运行文件，并按内容哈希
写入私有数据目录。托管运行器启动 `browser_worker.py`，它导入已校验的固定版本
基础 Worker，仅替换普通 Codex 的本地密码授权。旧运行包及其 `ready` 缓存不被覆盖。
所以升级主程序后，这部分修改实际进入 Worker，不依赖手工替换下载包。

首次 Codex 任务下载固定版本 `playwright-core`，执行前核验与前端锁文件一致的
SHA-512。只下载库，不自动安装浏览器、系统软件或修改沙箱。

## Linux 部署前置条件

- 安装带依赖的 Google Chrome，默认路径 `/opt/google/chrome/chrome`。
- 安装 `xvfb` 与 `xauth`。Linux 使用普通浏览器加独立虚拟显示，避免把 headless
  浏览器与普通浏览器的上游兼容性混为一谈；每个显示带独立 Xauthority，禁止 TCP 监听。
- 其他位置可设置 `OPENAI_CODEX_BROWSER_EXECUTABLE` 为可信的可执行文件绝对路径。
- 必须以非 root 用户运行，启用 Chromium 沙箱。Ubuntu 的 AppArmor 配置需要允许
  对应 Chrome 可执行文件使用 user namespace；不能使用 `--no-sandbox` 代替。
- 服务用户需要可写的临时目录。每个任务创建独立 HOME 和临时浏览器资料，正常完成、
  拒绝及超时退出均关闭浏览器并清理。任务上限四分钟，不录制视频、截图或 trace。
- 登录、回调令牌交换与后端连通验证使用原账号选定的代理。HTTP/HTTPS 代理支持
  用户名密码；SOCKS5 仅支持无认证模式，不支持的代理明确失败，不能静默直连。
- 缺浏览器、启动失败或依赖失败属于人工处理错误，不会触发自动循环登录。

密码、TOTP 和令牌通过内存管道传递，不进入命令行、环境变量、日志及调试产物。
OAuth 回调仅在该独立浏览器内拦截，不绑定公共端口，也不共享已有用户登录态。
遇到验证码、额外邮箱/手机验证或安全校验时停止，后台显示明确原因。

只有原有后端的邮箱、工作区、客户端、令牌有效期、账号快照/配置版本、连通性与
CAS 回写全部通过才显示“已授权”。浏览器拿到令牌本身不代表后台完成授权。

## 验证

`bps-session-check.yml` 验证嵌入运行入口、固定依赖完整性、Worker 路由与失败脱敏、
临时目录清理，并以真实沙箱 Chrome 运行合成的 OAuth 页面流程（不使用真实凭据）。
现有后端身份/代理/CAS 和前端单页添加回归仍运行。

上线前还需通过专用测试账号验证实际部署环境的完整登录、令牌回写和模型请求。
合成测试不能证明上游安全校验会放行；人工浏览器登录成功也不能替代 Worker 验收。
