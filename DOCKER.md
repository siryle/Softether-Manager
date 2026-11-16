# SoftEther Manager Docker 部署与局域网管理指南

本项目已成功容器化移植为 Docker 项目，支持通过现代化的 Web 控制台集中管理**局域网（LAN）内的 SoftEther VPN 服务器**，亦可管理宿主机或同宿主其他容器内的 SoftEther 实例。

后端使用 Go 编译为原生可执行文件，运行镜像无需 Python。构建时分别编译 Go 后端和前端静态资源，容器使用 `softether-manager healthcheck` 检查服务状态。

升级后需重新登录；已有 VPN 用户登录哈希会自动加密。务必同时保留数据库和 `encryption.key`。SoftEther 管理口会保存首次连接的证书指纹，换证后须在“服务器 → 编辑”中独立核对并确认新指纹。

---

## 快速开始

### 方式一：使用 Docker Compose（推荐）

1. **克隆项目到本地**：
   ```bash
   git clone https://github.com/siryle/Softether-Manager.git
   cd Softether-Manager
   ```

2. **配置环境变量**：
   公网部署应在启动前设置 `SEM_ADMIN_USERNAME` 和 `SEM_ADMIN_PASSWORD`，让服务在开放端口前完成账号初始化；留空时，首个访问初始化页面的人能够创建管理员。

   你可以直接编辑 `docker-compose.yml`，或复制 `.env.example` 为 `.env`：
   ```bash
   cp .env.example .env
   ```
   修改关键参数（根据你的局域网实际情况）：
   ```env
   # 局域网内 SoftEther 服务器的 IP 地址
   SEM_SE_HOST=192.168.1.100
   SEM_SE_PORT=5555
   # 可选：SoftEther 服务器管理员密码（留空可在 Web 界面输入）
   SEM_SE_PASSWORD=your_softether_admin_password

   # 可选：面板管理员账号（留空首次打开网页会自动引导创建）
   SEM_ADMIN_USERNAME=admin
   SEM_ADMIN_PASSWORD=your_panel_password
   ```

3. **构建并启动容器**：
   ```bash
   docker compose up -d --build
   ```

4. **访问管理面板**：
   打开浏览器访问：`http://<运行Docker的设备IP>:8000`
   - 若未在环境变量设置密码，首次访问会进入引导页面：创建面板管理员 -> 连接局域网 SoftEther。
   - 若已在环境变量预设账号与密码，直接使用预设的管理员登录即可！

---

### 方式二：使用 `docker run` 直接运行

可以直接拉取 GitHub Packages 镜像运行：
```bash
docker run -d \
  --name softether-manager \
  --restart unless-stopped \
  -p 8000:8000 \
  -v $(pwd)/data:/data \
  -e SEM_SE_HOST=192.168.1.100 \
  -e SEM_SE_PORT=5555 \
  --add-host host.docker.internal:host-gateway \
  ghcr.io/siryle/softether-manager:latest
```

或使用本地源码构建镜像：
```bash
docker build -t softether-manager:latest .
```

---

## 局域网（LAN）三大连接场景配置

### 场景 A：SoftEther 在局域网其他独立设备上（NAS / PC / 路由器 OpenWrt 等）
- **拓扑**：Docker 容器运行在设备 A（如 `192.168.1.50`），SoftEther 运行在设备 B（如 `192.168.1.100`）。
- **配置**：
  - `SEM_SE_HOST`: 设置为 `192.168.1.100`。
  - `SEM_SE_PORT`: 默认 `5555`（或 443、992，取决于 SoftEther 的 Listener 端口）。
- **网络**：使用默认 bridge 网桥网络即可，Docker 会通过宿主机物理网卡直接与局域网设备通信。

### 场景 B：SoftEther 运行在 Docker 宿主机本机上
- **拓扑**：Docker 与 SoftEther 运行在同一台机器上。
- **配置方式 1**（推荐跨平台）：
  - 容器内已配置 `extra_hosts: ["host.docker.internal:host-gateway"]`。
  - `SEM_SE_HOST`: 设置为 `host.docker.internal`。
- **配置方式 2**（仅限 Linux）：
  - 在 `docker-compose.yml` 中开启 `network_mode: "host"`。
  - `SEM_SE_HOST`: 直接设置为 `127.0.0.1`。

### 场景 C：SoftEther 作为另一个 Docker 容器运行
- **拓扑**：SoftEther 服务端本身也是一个 Docker 容器（例如 `softether-server`）。
- **配置**：
  - 将两者挂入同一个 Docker 网络（如 `networks: [vpn_net]`）。
  - `SEM_SE_HOST`: 直接填写 SoftEther 容器的服务名（如 `softether-server`）。

---

## SoftEther 服务端前置确认事项

1. **版本要求**：
   - SoftEther VPN Server 需为 **2019 年 6 月（v4.29 build 9680）或更新版本**（包含 v5.x Developer Edition）。该版本起官方原生集成了 `/api/` 的 JSON-RPC 接口。
2. **防火墙与端口**：
   - 确保 SoftEther 所在服务器的防火墙（如 `ufw`, `firewalld`, `iptables`）放行了管理端口（默认 `5555`）。
3. **API 状态检查**：
   - 确认 SoftEther 服务端的 `vpn_server.config` 文件中未将 `DisableJsonRpcWebApi` 设置为 `true`（默认即为 `false` 开启状态）。

---

## 环境变量速查表

| 环境变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `SEM_SE_HOST` | `127.0.0.1` | 局域网目标 SoftEther 服务器 IP 或主机名 |
| `SEM_SE_PORT` | `5555` | 目标 SoftEther 管理端口（常用 5555, 443, 992） |
| `SEM_SE_PASSWORD` | *(空)* | SoftEther 管理员密码（填写后启动时自动连接） |
| `SEM_ADMIN_USERNAME` | *(空)* | 首次启动自动创建的面板管理员用户名 |
| `SEM_ADMIN_PASSWORD` | *(空)* | 首次启动自动创建的面板管理员密码 |
| `SEM_BIND_HOST` | `0.0.0.0` | 容器内服务监听地址 |
| `SEM_BIND_PORT` | `8000` | 容器内服务监听端口 |
| `SEM_WEB_PATH` | *(空)* | 隐蔽访问前缀（如 `admin` 则通过 `/admin` 访问） |
| `SEM_DATA_DIR` | `/data` | 持久化数据存储目录（映射为宿主机目录） |

---

## 容器运维与 CLI 工具 (`sem`)

容器内的 `sem` 是 Go 可执行文件的别名，可在容器外部免登录执行各种应急维护：

### 1. 检查面板与连接状态
```bash
docker exec -it softether-manager sem status
```

### 2. 重置或创建管理员密码
当忘记 Web 面板登录密码时，可随时通过命令行重置：
```bash
read -r -s -p '新密码: ' sem_password
printf '\n'
printf '%s' "$sem_password" | docker exec -i softether-manager sem password --username admin --password-stdin
unset sem_password
```

### 3. 查看已有管理员账号
```bash
docker exec -it softether-manager sem users
```

### 4. 重新指定 SoftEther 连接信息
```bash
read -r -s -p 'VPN 管理密码: ' sem_password
printf '\n'
printf '%s' "$sem_password" | docker exec -i softether-manager sem connect --host 192.168.1.100 --port 5555 --password-stdin
unset sem_password
```

### 5. 查看实时日志
```bash
docker compose logs -f
```

---

## 数据备份与恢复

所有持久化配置均保存在容器的 `/data` 目录（对应宿主机映射的 `./data` 目录）：
- `manager.db`：SQLite 数据库，包含账号、历史流量采样记录、配额设置、配置参数。
- 密钥与凭据文件。

从 Python 版容器升级时，停止旧容器，备份整个 `./data`，再执行 `docker compose up -d --build`。保留原有挂载及 `session.key`、`encryption.key`、`tls/`，即可继续使用既有账号、加密连接信息、配额和证书。

**备份**：只需备份宿主机的 `./data` 目录即可：
```bash
tar -czvf softether-manager-backup.tar.gz ./data
```

**恢复**：解压 `./data` 目录并重启容器即可无缝恢复所有配置。
