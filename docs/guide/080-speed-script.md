## 检查与 AIFXO 的连接

先检查电脑能否连接 AIFXO，再检查 Key 和模型。以下命令只请求公开的健康检查接口，无需 API Key，不修改 hosts 或客户端配置。

### Windows PowerShell

```powershell
curl.exe --silent --show-error --include --connect-timeout 10 --max-time 20 https://api.aifxo.com/health
```

### macOS、Linux 或 WSL

```bash
curl --silent --show-error --include --connect-timeout 10 --max-time 20 https://api.aifxo.com/health
```

正常时应看到 HTTP `200`，正文包含 `"status":"ok"`。这只能说明当前电脑到站点的连接可用，不代表上游模型可用，也不能据此判断首字速度。

### 连接异常时

- **无法解析地址**：核对 `api.aifxo.com` 是否拼写正确，并检查本地 DNS 或代理设置。
- **证书错误**：核对电脑时间、证书提示和访问地址，不要用跳过证书验证来掩盖错误。
- **超时或连接失败**：检查本地网络和代理；记录发生时间与完整错误，联系管理员。
- **健康检查成功但模型失败**：按[检查 API Key 能不能用](#first-request)核对 Key 和模型，再参考[错误码含义与处理方案](#error-codes)。

配置客户端时使用[本站 API 地址](#domains)，无需安装修改 hosts 的测速工具。首字速度还受请求长度、所选模型、上游排队和网络状态影响，检查实际调用时请使用短问题并查看[用量记录](/usage)。
