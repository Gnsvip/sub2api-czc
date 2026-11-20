# 上传读取超时

默认上传读取空闲超时为 **180 秒**（原为 120 秒）。它限制单次阻塞读取，
不是整个上传的总时长，也不限制模型思考或 SSE 响应总时长。持续有数据的
上传可以超过 180 秒；停滞超过此窗口仍会返回 408，不接受半截正文。
内存预算、请求体大小限制、解压限制和客户端取消处理保持生效。

## systemd 二进制部署

已有显式 YAML 或环境变量配置不会被新默认值覆盖。部署时核对应用和入口：

```ini
# 应用实际使用的 EnvironmentFile
GATEWAY_REQUEST_BODY_READ_TIMEOUT_SECONDS=180
```

```nginx
# 处理 API 请求的现有 server/location 中；检查 include 的继承和覆盖关系
client_body_timeout 180s;
```

先备份对应配置和当前二进制。发布产物准备完成、获得部署授权后，再更新
应用配置并重启应用；修改 Nginx 后执行 `nginx -t`，通过后平滑 reload。
确认进程环境和 `nginx -T` 的生效值，分别测试正常上传、暂停后恢复、超时、
客户端取消及长 SSE。不要仅修改网页虚拟主机，也不要覆盖品牌或路由配置。
回滚时一并恢复配置和二进制；本变更没有数据库迁移。

不要因为上传超时而直接提高 `proxy_read_timeout`（等待上游响应），也不要
混淆 `proxy_request_buffering`（上传请求缓冲）和 `proxy_buffering`（响应缓冲）。
本版本不自动改变请求/响应缓冲或 CDN/WAF 配置。

## 故障日志

仅在原有请求体读取失败日志中增加数值，不缓存正文，不创建新的驻留诊断队列。
日志沿用现有轮转策略；需要单独采集时使用有限容量、1 小时保留的文件，不能
直接假设系统 journald 本身只保留 1 小时。

| 字段 | 含义 |
|---|---|
| `bytes_read` / `content_length_gap_bytes` | 应用读到的编码前解压输入字节 / 与声明长度的差值 |
| `read_ms` | 请求体读取阶段总耗时 |
| `body_read_calls` | 已完成的 Reader.Read 调用次数，包含失败的最后一次 |
| `body_first_byte_ms` | 第一次成功读到字节距阶段开始的时间 |
| `body_last_progress_ms` | 最后一次读到字节距阶段开始的时间 |
| `body_last_read_ms` | 最后一次 Read 调用耗时 |
| `body_idle_ms` | 失败时距最后一次读到字节的时间 |

没有读到任何字节时，不输出首字节/最后进度/idle 字段，避免把未知写成 0。
Read 同时返回字节和错误时仍计入字节数与进度。上述时间是应用读取观测，
不是客户端发包、CDN 或 Nginx 收包时间。要定位具体一跳，需要关联 request ID、
客户端 ID 和入口日志；不能把应用 `bytes_read` 当成客户端实际已发送字节数。

180 秒只增加对短暂停滞的容忍度，不承诺修复已经断开的链路。验证压缩时看
实际 `Content-Encoding` 和传输字节数；服务器到模型上游的压缩不能减少
客户端尚未上传到服务器的数据。

## 可选的 Nginx 入口诊断

仓库提供 `nginx/upload-diagnostics-http.conf`、
`nginx/upload-diagnostics-location.conf`、`upload_diagnostics.py` 和
`systemd/sub2api-upload-diagnostics.service`。它们不会随二进制更新自动启用。
只记录已识别 API 路径的 **POST 408 / 499**，不记录正文、查询参数、IP、
Authorization 或 API Key。域名和路由保存为固定标签，关联 ID 也限制格式。

采集器使用 Python 3 标准库和本机 Unix datagram socket，不新增网络监听。
数据写入 `/var/lib/sub2api-upload-diagnostics/`：

- 每分钟一个文件，单文件最多 32 KiB，最多 60 个有效分钟桶（约 1.88 MiB）。
- 服务运行时每分钟清理，无请求也会清理；分钟桶可能提前不足 1 分钟过期。
  停止服务期间不会清理，重启时立即清理过期数据。
- 桶写满就丢弃新记录，不阻塞业务、不开无限队列；下一条写入成功的记录带
  `dropped_due_to_capacity`。因此这是抽样诊断，不是完整审计记录。
- systemd 限制内存 32 MiB、CPU 10%，日志目录 0700、文件 0600。
  原有应用 / Nginx 常规日志的保留策略不由此工具修改。

新增一个长期 Python 回归文件，是因为容量限制、自动过期、字段过滤以及真实
Nginx 的 408 / 499 行为需要一起验证。现有 CI 的 `upload-diagnostics` 作业在
Linux 上运行实际 Nginx 和本机正文读取桩；不访问生产、没有用户密钥、不调用
模型。Sub2API 的读取与解压行为仍由已有 Go 回归覆盖。桩测试通过不等于生产
链路已验收。

### 安装与回滚（获得部署授权后）

1. 核实 Nginx worker 用户组；service 示例适用于 Ubuntu 的 `www-data`。
   保留 `nginx -T` 的原有配置、现有日志和所有 `proxy_set_header`。
2. 将 Python 脚本放到 `/opt/sub2api-tools/upload_diagnostics.py`，service 放到
   `/etc/systemd/system/sub2api-upload-diagnostics.service`；安装两个 Nginx
   片段。记录精确安装路径及配置备份路径。
3. `http {}` 中只 include 一次 `upload-diagnostics-http.conf`；只在**原有**
   API 转发 location 中 include `upload-diagnostics-location.conf`。不要创建
   新路由、替换 `proxy_pass`，也不要改请求 / 响应缓冲或超时。
4. 注意 Nginx 继承：新增 location 级 `proxy_set_header` 会停止继承上层同类
   指令，必须把原本生效的 Host、真实 IP、Upgrade 等 header 放回同一层。
   新增 `access_log` 也会覆盖上层日志继承；需要的原有日志应保留在同一层。
   同一层若有 `access_log off`，会连新日志一起关闭，需精确调整这条指令。
5. 启动采集器后确认 socket 和目录权限，再执行 `nginx -t`；通过后平滑 reload。
   此诊断不需要重启 Sub2API 二进制。检查 service 内存、日志容量、业务健康，
   用下述合成上传验证，并关联下一次自然发生的失败。
6. 回滚先恢复原 Nginx include、日志和 header 配置，`nginx -t` 通过后 reload，
   再停用诊断 service。只清理这次安装的精确路径，不删业务日志、数据库或
   应用数据；停服务后遗留的诊断文件也应按精确路径清理。

截至 2026-11-20 的只读核查：API 域名的 API location 是 180 秒，而网站域名
继承共享片段的 15 秒。这个补丁**没有修改它们**。如确认用户确实通过网站域名
上传 API 请求，应另行调整实际 API 入口；不能全局提高网站超时，也不能用
这个差异解释所有应用侧的 180 秒失败。

### 怎样关联与判断

| 入口字段 | 用途与边界 |
|---|---|
| `edge_request_id` | Nginx 生成的 32 位十六进制 ID，作为 `X-Request-ID` 传给应用；当前应用接受并写入原有日志 |
| `app_request_id` | 应用返回的 ID；没有收到应用响应时为空，用 `edge_request_id` 查应用日志 |
| `incoming_client_request_id` / `app_client_request_id` | 分别是客户端传来的 UUID、应用返回的 UUID；应用可能重新生成，两者不能混为一谈 |
| `host` / `route` / `encoding` | 固定域名、API 路由标签及实际入站 Content-Encoding 分类 |
| `declared_body_bytes` | 入站 Content-Length；分块上传时可能为空 |
| `request_bytes_including_headers` | Nginx 的 request_length，**包含请求行和头，不等于正文已收字节** |
| `upstream_bytes_sent_including_headers` | Nginx 向应用发送的总字节，**包含头，不等于应用正文已读取字节** |
| `request_seconds` / `upstream_seconds` | 请求总耗时 / 连接应用一跳的耗时，不是逐包时间或最后停传时长 |
| `request_complete` | Nginx 请求完成标志，不能单凭它断言正文是否收全 |
| `status` / `upstream_status` | 对照入口状态与应用一跳状态；`upstream` 在此指 Sub2API，不是模型服务 |

入口 408、应用 408、客户端中途断开须分别判断。**499 也可能发生在上传完成
后的响应阶段**。字节数量只能结合声明长度、头开销、应用 `bytes_read` 和
停传日志分析；这些元数据不能单独证明具体哪段公网丢包。

## 合成上传对照工具

`upload_probe.py` 每次只发一个请求、不自动重试。正文第一个字符就是 `!`，
故意构造为无效 JSON；没有模型名、没有用户历史。使用有权限的专用 OpenAI
测试密钥，正常结果是 **400 / invalid_request_error / Failed to parse request body**，
表示经过读取和解压后止于正文解析。401、403、413、408 或其他 400 都不算
完整上传成功。无效密钥可能在读取正文前就被拒绝，不能作为上传对照。

密钥仅从 stdin 读入，不使用命令行参数、URL 或日志传递。由受控父进程从安全
存储读入并通过 stdin 传递；不要把密钥写入 shell 历史。命令形式：

```text
python3 upload_probe.py https://api.aifxo.com/v1/responses --size-bytes 1048576
python3 upload_probe.py https://api.aifxo.com/v1/responses --size-bytes 18815061
python3 upload_probe.py https://api.aifxo.com/v1/responses --encoding gzip
python3 upload_probe.py https://api.aifxo.com/v1/responses --pause-seconds 5
python3 upload_probe.py https://api.aifxo.com/v1/responses --connect-ip <已核实的源站IP>
```

先核实 DNS：域名若本来直连源站，指定源站 IP 不构成 CDN 对照。
`--connect-ip` 保留原 Host 和 TLS SNI，仍校验证书；不使用跳过 TLS 校验。
测试依次进行，不做并发压测；CI 用短超时验证真正停传的情况，生产先测正常
上传和短暂停顿。输出只含元数据，`sent_body_bytes` 表示本机 socket 接受的
字节，不保证对端收到。工具只验证 HTTP/1.1，不代表已覆盖客户端的 HTTP/2
或用户自己的代理链路。模拟正文使用随机 base64 类数据，gzip 压缩比例也不
代表某个真实用户的图片历史。

验证新诊断前须先通过 CI；生产对照结果应单独记录时间、域名、路径、声明 /
实际编码字节、请求 ID、返回状态和是否通过解析拒绝校验。不要把“工具已写好”
或桩测试成功写成“生产 408 已修复”。
