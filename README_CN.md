# Kiro-Go VPS 部署教程

本仓库是基于 Kiro-Go 的 VPS 部署版本，包含：

- OpenAI `/v1/chat/completions` 兼容接口
- Anthropic `/v1/messages` 兼容接口
- 多账号池与自动 Token 刷新
- 出站代理池、账号代理绑定、错误退避和代理状态面板
- Kiro / CodeWhisperer / AmazonQ 端点选择与 fallback
- Web 管理面板、账号导入导出、用量统计

推荐在 VPS 上使用 Docker Compose 部署。源码运行也支持，但维护成本更高。


## 1. 服务器准备

建议配置：

- Ubuntu 22.04 / Debian 12
- 1 核 1G 起步
- 已开放 `22` 端口用于 SSH
- 如需公网访问管理面板或 API，开放 `8080` 或反向代理后的 `80/443`

更新系统：

```bash
sudo apt update
sudo apt upgrade -y
```

安装常用工具：

```bash
sudo apt install -y git curl ca-certificates ufw
```


## 2. 安装 Docker

如果 VPS 没有 Docker，执行：

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
```

把当前用户加入 Docker 组：

```bash
sudo usermod -aG docker $USER
```

然后退出 SSH 重新登录，再验证：

```bash
docker version
docker compose version
```


## 3. 拉取项目

```bash
cd /opt
sudo git clone https://github.com/scvxzf1/Kiro-Go.git
sudo chown -R $USER:$USER /opt/Kiro-Go
cd /opt/Kiro-Go
```


## 4. 初始化配置

创建持久化目录：

```bash
mkdir -p data
cp data/config.example.json data/config.json
chmod 600 data/config.json
```

编辑配置：

```bash
nano data/config.json
```

至少建议修改：

```json
{
  "password": "改成强密码",
  "port": 8080,
  "host": "0.0.0.0",
  "requireApiKey": true,
  "apiKey": "sk-改成你的API密钥",
  "preferredEndpoint": "auto",
  "endpointFallback": true,
  "proxyPool": []
}
```

说明：

- `password` 是 Web 管理面板密码。
- `requireApiKey` 建议设为 `true`，避免 API 被公开滥用。
- `apiKey` 是调用 OpenAI / Claude 兼容接口时使用的 Bearer Token。
- `proxyPool` 可以为空，后续也可以在管理面板配置。
- `data/config.json` 包含账号 Token，不要上传到公开仓库。


## 5. Docker Compose 启动

构建并启动：

```bash
docker compose up -d --build
```

查看状态：

```bash
docker compose ps
```

查看日志：

```bash
docker compose logs -f --tail=100
```

访问管理面板：

```text
http://你的VPS_IP:8080/admin
```

登录后可以添加账号、配置代理池、查看代理状态和用量。


## 6. 防火墙配置

如果直接暴露 `8080`：

```bash
sudo ufw allow OpenSSH
sudo ufw allow 8080/tcp
sudo ufw enable
sudo ufw status
```

如果你使用 Nginx / Caddy 反向代理到 HTTPS，只需要开放：

```bash
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw enable
```


## 7. 配置出站代理池

进入管理面板：

```text
设置 -> 出站代理设置
```

每行一条代理，支持格式：

```text
http://127.0.0.1:7890
socks5://user:pass@proxy.example.com:1080
host:port:user:pass
```

策略说明：

- 默认每个账号会从代理池绑定一个代理。
- 请求成功会保持绑定。
- 代理错误率过高时会进入冷却。
- 冷却期间账号会退避选择池中其他代理。
- 面板会显示代理可用、冷却、失败次数等状态。

保存后立即生效，不需要重启容器。


## 8. 添加账号

管理面板支持多种方式：

- AWS Builder ID 登录
- IAM Identity Center 登录
- SSO Token 导入
- 凭证 JSON 导入
- Kiro CLI 沙盒登录

推荐 VPS 上优先使用：

- 凭证 JSON 导入
- Builder ID / IAM 登录

Kiro CLI 沙盒登录见下一节限制说明。


## 9. Kiro CLI 沙盒登录说明

当前 Docker 镜像默认不内置 `kiro-cli` 和 `sqlite3`，所以 Docker 部署下的 Kiro CLI 沙盒登录默认不可用。

如果点击沙盒登录时看到：

```text
未检测到本机 kiro-cli
```

或：

```text
未检测到 sqlite3
```

这是预期现象。

可选方案：

1. 在本地电脑运行项目，使用沙盒登录导出账号，再把导出的账号 JSON 导入 VPS。
2. 在 VPS 宿主机安装 `kiro-cli` 和 `sqlite3`，用源码运行项目。
3. 自行制作包含 `kiro-cli` 和 `sqlite3` 的 Docker 镜像。

普通 API 服务、账号池、代理池和管理面板不受影响。


## 10. API 调用示例

OpenAI 兼容接口：

```bash
curl http://你的VPS_IP:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-改成你的API密钥" \
  -d '{
    "model": "claude-sonnet-4.5",
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```

Claude 兼容接口：

```bash
curl http://你的VPS_IP:8080/v1/messages \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-改成你的API密钥" \
  -H "anthropic-version: 2023-06-01" \
  -d '{
    "model": "claude-sonnet-4.5",
    "max_tokens": 1024,
    "messages": [
      {"role": "user", "content": "你好"}
    ]
  }'
```


## 11. 使用域名和 HTTPS

推荐用 Caddy，配置简单。

安装：

```bash
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update
sudo apt install -y caddy
```

编辑：

```bash
sudo nano /etc/caddy/Caddyfile
```

示例：

```text
api.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

重载：

```bash
sudo systemctl reload caddy
```

然后访问：

```text
https://api.example.com/admin
```


## 12. 更新项目

进入项目目录：

```bash
cd /opt/Kiro-Go
```

拉取更新并重建：

```bash
git pull
docker compose up -d --build
```

查看日志：

```bash
docker compose logs -f --tail=100
```


## 13. 备份与迁移

最重要的文件是：

```text
data/config.json
```

备份：

```bash
cd /opt/Kiro-Go
mkdir -p backup
cp data/config.json backup/config-$(date +%F-%H%M%S).json
chmod 600 backup/*.json
```

迁移到新服务器：

```bash
scp /opt/Kiro-Go/data/config.json root@新服务器IP:/opt/Kiro-Go/data/config.json
```

恢复后重启：

```bash
docker compose restart
```


## 14. 常用运维命令

启动：

```bash
docker compose up -d
```

停止：

```bash
docker compose down
```

重启：

```bash
docker compose restart
```

查看日志：

```bash
docker compose logs -f --tail=100
```

查看端口：

```bash
ss -ltnp | grep 8080
```

进入容器：

```bash
docker compose exec kiro-go sh
```


## 15. 源码运行方式

不使用 Docker 时：

```bash
sudo apt install -y golang sqlite3 git
git clone https://github.com/scvxzf1/Kiro-Go.git
cd Kiro-Go
mkdir -p data
cp data/config.example.json data/config.json
go build -o kiro-go .
CONFIG_PATH=data/config.json ./kiro-go
```

后台运行：

```bash
nohup ./kiro-go > kiro-go.log 2>&1 &
```

源码运行适合需要在宿主机使用 `kiro-cli` 沙盒登录的场景。


## 16. 安全建议

- 必须修改默认管理密码。
- 公开 API 时建议开启 `requireApiKey`。
- 不要把 `data/config.json` 上传到 GitHub。
- 管理面板尽量放在 HTTPS 后面。
- 代理池里如果含用户名密码，不要截图或公开日志。
- 定期备份 `data/config.json`。


## 17. 故障排查

容器启动失败：

```bash
docker compose logs --tail=200
```

管理面板打不开：

```bash
docker compose ps
ss -ltnp | grep 8080
sudo ufw status
```

账号请求失败：

- 检查账号是否启用。
- 检查 Token 是否过期，尝试刷新账号。
- 检查代理池状态，确认代理未全部冷却。
- 尝试把端点设置为 `auto` 并开启 fallback。

代理不可用：

- 检查代理格式。
- 确认 VPS 能连通代理服务器。
- 观察代理状态面板中的冷却和失败次数。


## 免责声明

本项目仅供学习和研究目的使用，与 Amazon、AWS 或 Kiro 没有任何关联。使用者需自行确保符合相关服务条款和当地法律法规。


## License

[MIT](LICENSE)
