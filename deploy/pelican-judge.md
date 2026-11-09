# auto-pelican 视觉评分

后台「测试设置 → 鹈鹕测试」可开启多帧评分，默认关闭；通过线默认 70 分，可设 1–100。
评分仅展示，不会修改账号、Astra 判定、分组或调度。Astra 自动换组仍由独立探测触发。

开启评分后题目固定为「鹈鹕骑自行车」，以匹配原项目的评审标准。动画必须使用 SVG 内
CSS / SMIL；含 JavaScript、外部资源、SVG 外样式/HTML、多个画面或无法渲染的结果
显示「评分未完成」，不会算降智。代码块包裹单个完整 SVG 可以评分。
首帧和带时间标记的六帧拼图发给独立视觉评审模型，默认三次评审取平均。
评审模型需要支持图片输入，相关请求额外计费。API 超时、错误或无效评分均不计质量分。

## 运行环境

生成动画仍由现有 sub2api 测试执行。二进制内置固定版本的 auto-pelican 评分代码，
无需安装 Codex、Claude Code、浏览器或容器。额外需要 Python 3.12+ 和随包的依赖文件。
在部署环境中准备独立虚拟环境（下面是安装示例，不会由程序自动执行）：

```sh
python3.12 -m venv /opt/sub2api/pelican-venv
/opt/sub2api/pelican-venv/bin/python -m pip install -r pelican-judge-requirements.txt
```

在 systemd 服务读取的受保护环境文件中配置下列变量，部署时加载；不要提交真实密钥：

```ini
SUB2API_PELICAN_PYTHON=/opt/sub2api/pelican-venv/bin/python
JUDGE_PROTOCOL=chat_completions
JUDGE_BASE_URL=https://your-vision-provider.example/v1
JUDGE_MODEL=your-vision-model
JUDGE_API_KEY=replace-with-a-dedicated-key
JUDGE_SAMPLES=3
```

协议也支持 `responses` 和 `messages`，地址格式遵循各 SDK。
密钥仅在服务器运行环境中，不进入测试设置、配置快照或用户页。
后台就绪状态检查解释器路径和必填变量是否存在；依赖缺失、模型不支持图片等问题会在
首次测试中显示未评分。安装后先用单次测试验收，再开启自动测试。
每次评分最多 75 秒（含等待），同一进程最多两个评分子进程；评审失败不自动重试。
生成过程的原超时不变。评分子进程使用 Linux 内存/CPU 限制，不执行 SVG 内脚本。

## 分组多选兼容

正常分组可多选，降级分组仍为一个。降级时移出账号所有已选正常分组，一次加入目标组，
数据库变更与调度缓存通知在同一事务内。未选择的分组不会被移除，需选全应移出的组。
恢复时只放回该账号原先所属的分组，保留各组优先级和创建时间。
旧版单分组设置继续兼容。多组选项下，如果历史降级账号没有原归属记录，不猜测其分组，
需管理员手动安排；若恢复前取消选择了原分组，也保留在降级组，等待修正配置或手动处理。

## 验证和回退

GitHub Actions 安装上述 Python 依赖，用本地模拟评审端点验证渲染、六帧请求、评分、
脚本/外部资源拒绝及接口错误；不使用生产密钥或付费模型。
关闭视觉评分后停止新增评分，历史分数保留。无需数据库迁移。
回退旧二进制前应关闭 Astra 自动换组：旧版不理解多个正常组，不能安全继续自动恢复。

源码来自 [haowang02/auto-pelican](https://github.com/haowang02/auto-pelican)，
固定提交 `8f152aaeff6913a018d3edd5a696b438c47853ee`，MIT 许可见 `auto-pelican-LICENSE`。
