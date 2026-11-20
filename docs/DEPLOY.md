# 部署示例：systemd + Nginx

以下示例基于 Ubuntu 24.04。命令和配置中的**域名、路径、账号都是占位值**，请替换成你自己的。

> 重要：本服务**没有内置登录**。直接把端口暴露到公网，等于把「往你的语雀写内容」的能力公开。下面第 4 步的认证配置是必须的，不是可选项。

## 1. 准备目录与用户

建议用非 root 的独立用户运行：

```bash
sudo useradd --system --create-home --shell /usr/sbin/nologin relay
sudo mkdir -p /opt/knowledge-relay/{data,inbox,archive,profiles,logs}
sudo chown -R relay:relay /opt/knowledge-relay
```

## 2. 获取代码并编译

在任意有 Go 环境的机器上编译，或者直接在服务器上编译：

```bash
cd /opt/knowledge-relay
sudo -u relay git clone https://github.com/<你的账号>/yuque-knowledge-relay.git src
cd src/goapp
CGO_ENABLED=0 go build -trimpath -o /opt/knowledge-relay/knowledge-relay .
```

如果服务器上没装 Go，也可以本地交叉编译后上传：

```bash
# 在本地执行（示例为 Linux x86_64）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o knowledge-relay .
scp knowledge-relay user@服务器:/opt/knowledge-relay/
```

## 3. 放置配置与档案

```bash
sudo -u relay cp /opt/knowledge-relay/src/config.example.yaml /opt/knowledge-relay/config.yaml
sudo -u relay cp -r /opt/knowledge-relay/src/profiles/. /opt/knowledge-relay/profiles/
sudo -u relay cp -r /opt/knowledge-relay/src/app /opt/knowledge-relay/app     # 静态资源
sudo -u relay cp -r /opt/knowledge-relay/src/goapp/templates /opt/knowledge-relay/goapp-templates
```

> 模板按相对路径读取，服务的工作目录必须是**含 `templates/` 的目录**。下面 systemd 示例把工作目录设为 `src/goapp`，直接用仓库里的模板，省去复制。

创建密钥文件：

```bash
sudo -u relay tee /opt/knowledge-relay/.env >/dev/null <<'EOF'
YUQUE_TOKEN=你的语雀Token
LLM_API_KEY=你的模型密钥
EOF
sudo chmod 600 /opt/knowledge-relay/.env
sudo chown relay:relay /opt/knowledge-relay/.env
```

`.env` 里的值会覆盖 `config.yaml` 中的同名字段，这样配置文件可以放心放进仓库或备份。

## 4. systemd 服务

创建 `/etc/systemd/system/knowledge-relay.service`：

```ini
[Unit]
Description=Yuque Knowledge Relay
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=relay
Group=relay
WorkingDirectory=/opt/knowledge-relay/src/goapp
Environment="TZ=Asia/Shanghai"
EnvironmentFile=/opt/knowledge-relay/.env
ExecStart=/opt/knowledge-relay/knowledge-relay -addr 127.0.0.1:8010 -root /opt/knowledge-relay
Restart=on-failure
RestartSec=5

# 基础加固：只允许写自己的数据目录
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/knowledge-relay

[Install]
WantedBy=multi-user.target
```

启用并启动：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now knowledge-relay
sudo systemctl status knowledge-relay --no-pager
curl -fsS http://127.0.0.1:8010/healthz
```

输出 `ok` 就说明服务已起来。

**只监听 `127.0.0.1`**：对外访问全部经过 Nginx，不要改成 `0.0.0.0`。

## 5. Nginx 反向代理与认证

### 5.1 生成密码文件

```bash
sudo apt-get install -y apache2-utils nginx
sudo htpasswd -c /etc/nginx/.htpasswd yourname
```

### 5.2 站点配置

创建 `/etc/nginx/sites-available/knowledge-relay`：

```nginx
server {
    listen 80;
    server_name relay.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl http2;
    server_name relay.example.com;

    ssl_certificate     /etc/letsencrypt/live/relay.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/relay.example.com/privkey.pem;

    # 文章可能较长，放宽请求体
    client_max_body_size 20m;

    # 全站基础认证
    auth_basic           "Knowledge Relay";
    auth_basic_user_file /etc/nginx/.htpasswd;

    location / {
        proxy_pass         http://127.0.0.1:8010;
        proxy_http_version 1.1;
        proxy_set_header   Host              $host;
        proxy_set_header   X-Real-IP         $remote_addr;
        proxy_set_header   X-Forwarded-For   $proxy_add_x_forwarded_for;
        proxy_set_header   X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
    }

    # 简悦推送：单独用一个密钥，避免把网页登录口令写进简悦
    location = /ingest/simpread {
        auth_basic           off;
        # 只允许你自己的随机串
        if ($http_x_ingest_key != "换成你自己的随机串") {
            return 401;
        }
        proxy_pass         http://127.0.0.1:8010;
        proxy_http_version 1.1;
        proxy_set_header   Host $host;
        proxy_read_timeout 60s;
    }
}
```

启用：

```bash
sudo ln -s /etc/nginx/sites-available/knowledge-relay /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
```

### 5.3 证书

```bash
sudo apt-get install -y certbot python3-certbot-nginx
sudo certbot --nginx -d relay.example.com
```

### 5.4 为什么 `X-Ingest-Key` 要放在代理层

Go 服务本身**不校验**这个头，它只是被设计成由反向代理拦截。这样做的原因是：

- 网页端用基础认证（浏览器会记住），简悦用请求头，两套互不影响；
- 密钥不进代码、不进配置，泄露了改 Nginx 一处即可；
- 服务保持无状态，换代理方案不需要改代码。

如果你希望应用层自己校验，可以在 `simpreadHandler` 里比较 `r.Header.Get("X-Ingest-Key")` 与一个环境变量。

## 6. 防火墙

```bash
sudo ufw allow 80,443/tcp
sudo ufw deny 8010/tcp      # 确保后端端口不对外
sudo ufw enable
```

## 7. 备份

需要备份的只有两个目录：

```bash
# 数据库（含所有文章、词条、状态）
/opt/knowledge-relay/data/
# 分类档案
/opt/knowledge-relay/profiles/
```

`.env` 与 `config.yaml` 也建议单独保管，但**不要**和备份放在同一个公开位置。

SQLite 使用 WAL 模式，在线备份请用 SQLite 自带命令，不要直接拷贝正在写入的 `.db` 文件：

```bash
sqlite3 /opt/knowledge-relay/data/kb.db ".backup /backup/kb-$(date +%F).db"
```

## 8. 升级

```bash
cd /opt/knowledge-relay/src
sudo -u relay git pull
cd goapp
sudo -u relay bash -c 'CGO_ENABLED=0 go build -trimpath -o /opt/knowledge-relay/knowledge-relay.new .'
sudo systemctl stop knowledge-relay
sudo mv /opt/knowledge-relay/knowledge-relay.new /opt/knowledge-relay/knowledge-relay
sudo systemctl start knowledge-relay
```

升级前建议先跑一次迁移检查：

```bash
sudo -u relay /opt/knowledge-relay/knowledge-relay \
  -root /opt/knowledge-relay -check /opt/knowledge-relay/data/kb.db
```

程序启动时会自动补齐新增的列，一般不需要手动改表。服务重启后会恢复未完成的写回作业。

## 9. 排错

| 现象 | 排查方向 |
|---|---|
| 启动即退出，提示读 config.yaml 失败 | `-root` 是否指向包含 `config.yaml` 的目录 |
| 启动即退出，提示加载模板失败 | 工作目录是否包含 `templates/`（需在 `goapp/` 下启动） |
| 页面 502 | 后端是否在跑：`systemctl status`、`curl 127.0.0.1:8010/healthz` |
| 简悦推送 401 | Nginx 里的 `X-Ingest-Key` 与简悦配置是否一致 |
| 分类总是进待定 | 档案是否存在、模型是否可用、阈值是否过高 |
| 写回提示冲突 | 语雀里是否手改过正文；确认后可强制覆盖 |
| 图片不显示 | 图片代理是否需要出网、源站是否防盗链 |

日志：

```bash
sudo journalctl -u knowledge-relay -f
```
