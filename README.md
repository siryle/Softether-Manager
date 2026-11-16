# SoftEther Manager

[English](README_EN.md) | **简体中文**

现代化、自托管的 [SoftEther VPN Server](https://www.softether.org/) 网页管理控制台。完整支持 SoftEther 官方全部 133 个 JSON-RPC 管理 API，具备直观优雅的交互界面、宿主机内核级健康监控、全功能流量配额统计，已全面支持 **Docker 容器化部署** 及 **局域网（LAN）设备远程管理**。

![控制面板](docs/screenshots/zh-CN/dashboard.png)

预览图由当前 Go 后端与前端实际运行后截取，使用独立的演示数据；本页展示中文界面，英文文档展示英文界面。

---

## 🌟 核心功能特性

### 🖥️ 宿主机实时监控
- **内核直读性能指标**：直接从 Linux `/proc` 读取核心状态，包含 CPU（各核心占用、系统负载及排查 VPS 隐形性能瓶颈的关键指标 —— Steal 虚拟化抢占时间）、内存与 Swap 占用、真实物理文件系统以及网络实时吞吐走势图。

### 🖥️ 多服务器切换管理
- 顶部切换器按名称或管理地址搜索，在不同服务器之间切换；每个浏览器标签页独立选择管理对象。
- 服务器列表支持新增、编辑、测试与移除连接，显示真实连接状态、会话及 HUB 数量。
- HUB、用户、会话、流量历史、配额、保存的 VPN 凭据及审计记录按服务器隔离；面板账号、域名与 HTTPS 设置共用。
- CPU、内存及宿主机网卡监控始终来自面板所在机器；切换远程 VPN 不改变这些监控数据的来源。

### 🌐 完整的 SoftEther 功能矩阵
面板基于官方原生 JSON-RPC 接口协议打造，将底层 133 个管理方法组织为逻辑清晰的控制台：

| 模块 | 功能说明 |
| :--- | :--- |
| **用户管理** | 创建、编辑与删除用户；支持密码、用户证书、CA 签名证书、RADIUS、Windows NT 域认证；用户组划分；账户过期时间；每个用户可配置 40+ 项细粒度安全策略；支持一键生成并下载定制 **.vpn 客户端连接文件**（可自动内置哈希凭证）；跨 Hub 全局用户搜索与排序。 |
| **流量配额限制** | 支持针对每个配置及虚拟 HUB 设置流量上限（MB / GB / TB），支持单算上行、单算下行或双向合计；达到限额自动切断会话或将 HUB 下线；调高限额或重置后自动恢复策略。支持真正的**流量清零**统计。 |
| **用户组管理** | 创建、编辑、删除组，组内成员自动继承组级别安全策略。 |
| **活跃会话** | 实时会话列表，展示客户端 IP、连接协议、加密算法与累计传输量；支持查看各会话的独立流量曲线；支持强制断开会话，具备连接审计日志。 |
| **访问控制 (ACL)** | 强大的包过滤器列表（规则优先级、协议、源/目标 IP、端口、用户、模拟网络丢包/抖动/延迟）以及源 IP 连接速率限制。 |
| **HUB 安全机制** | 信任 CA 证书、CRL 证书吊销列表、RADIUS 委托认证、HUB 管理员密码。 |
| **SecureNAT** | 开启/关闭虚拟 NAT 与 DHCP 服务，实时查看 NAT 转换表与 DHCP 租约记录。 |
| **级联连接** | 管理与其他 VPN 服务器的 Hub-to-Hub 级联链路，实时展示连通状态。 |
| **交换与网络** | 查看 MAC 地址表与 IP 映射表、配置物理网卡本地网桥、管理虚拟三层交换机与路由表项。 |
| **服务器全局设置** | 监听端口管理、VPN-over-ICMP/DNS、L2TP/IPsec、OpenVPN 与 MS-SSTP 协议集成、VPN Azure 云穿透、动态域名 DDNS、加密算法与证书管理、集群配置、网络保活、Syslog、运行与安全日志浏览、配置备份与恢复。 |
| **面板自身安全** | 自定义域名、内置 Let's Encrypt 证书全自动申请与续期、HTTPS 端口自定义，支持“仅允许通过该域名访问”的安全防护阻断。 |

### 📈 真实流量曲线与采样
SoftEther 本身只保留自开机以来的累计流量计数，没有历史数据。面板内置后台采样调度器，自动将流量增量计算为吞吐曲线图（支持按 Hub、按用户、按会话多维度分析）。

![用户详情](docs/screenshots/zh-CN/user.png)

### 📱 移动端与自适应支持
屏幕宽度低于 960px 时，界面将自动切换为专为移动设备优化的 App 级交互体验：集成顶部状态栏、底部快捷 Tab 栏、手势底栏抽屉（Bottom Sheets），在手机或平板上管理 VPN 服务器丝滑顺畅。

<p align="center">
  <img src="docs/screenshots/zh-CN/mobile-dashboard.png" width="24%" alt="移动端仪表盘" />
  <img src="docs/screenshots/zh-CN/mobile-users.png" width="24%" alt="移动端用户管理" />
  <img src="docs/screenshots/zh-CN/mobile-user.png" width="24%" alt="移动端用户详情" />
  <img src="docs/screenshots/zh-CN/mobile-hub.png" width="24%" alt="移动端HUB视图" />
</p>

### 🎨 深色与浅色主题
精心调优的深色（Dark）与浅色（Light）两套现代化拟物与卡片式主题，支持跟随操作系统自动切换。

<details>
<summary><b>展开查看更多界面截图</b></summary>

#### 用户列表
![用户管理](docs/screenshots/zh-CN/users.png)

#### 活跃会话
![活跃会话](docs/screenshots/zh-CN/hub-sessions.png)

#### 虚拟 HUB 概览
![HUB 概览](docs/screenshots/zh-CN/hub-overview.png)

#### 虚拟 HUB 列表
![虚拟 HUB 列表](docs/screenshots/zh-CN/hubs.png)

#### 运行日志
![运行日志](docs/screenshots/zh-CN/logs.png)

#### 服务器设置
![服务器设置](docs/screenshots/zh-CN/server-settings.png)

#### 浅色模式
![浅色模式](docs/screenshots/zh-CN/hub-overview-light.png)
</details>

---

## 🚀 部署方式

### 方式一：Docker 部署（推荐，支持局域网管理）

已针对 Docker 环境深度优化，支持跨架构（`linux/amd64` 与 `linux/arm64`），镜像已发布至 GitHub Packages。

#### 1. 使用 Docker Compose（最便捷）
克隆仓库后直接启动：
```bash
git clone https://github.com/siryle/Softether-Manager.git
cd Softether-Manager
docker compose up -d
```

或在任意目录新建 `docker-compose.yml`：
```yaml
services:
  softether-manager:
    image: ghcr.io/siryle/softether-manager:latest
    container_name: softether-manager
    restart: unless-stopped
    ports:
      - "8000:8000"
    environment:
      # --- 局域网 SoftEther 连接配置 ---
      - SEM_SE_HOST=192.168.1.100  # 局域网 SoftEther 服务器 IP，同机填写 host.docker.internal
      - SEM_SE_PORT=5555           # 管理端口（默认 5555）
      - SEM_SE_PASSWORD=           # SoftEther 管理员密码（可选，留空可在网页输入）

      # --- 初始面板管理员（可选，若填写启动后自动创建） ---
      - SEM_ADMIN_USERNAME=admin
      - SEM_ADMIN_PASSWORD=

      # --- 访问配置 ---
      - SEM_BIND_PORT=8000
      - SEM_WEB_PATH=              # 隐蔽访问路径（可选，如设置为 mysecret，则通过 /mysecret 访问）

    volumes:
      - ./data:/data               # 数据持久化目录（SQLite 数据库与证书）
    extra_hosts:
      - "host.docker.internal:host-gateway"
```

#### 2. 使用 `docker run` 命令行
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

#### 3. 局域网三种连接拓扑说明
- **局域网独立设备（NAS / PC / 路由器 OpenWrt 等）**：设置 `SEM_SE_HOST` 为对应局域网 IP（例如 `192.168.1.100`）。
- **SoftEther 运行在同一宿主机**：使用 `SEM_SE_HOST=host.docker.internal`（借助 `extra_hosts` 互通）或在 Linux 下使用 `network_mode: "host"`。
- **SoftEther 作为另一个 Docker 容器**：两者挂在同一 Docker 网络下，`SEM_SE_HOST` 直接填写对方容器名。

更多环境变量与局域网网络配置细节可查阅 [DOCKER.md](DOCKER.md)。

---

### 方式二：传统 Linux 宿主机原生安装（Systemd）

以 root 身份在运行 SoftEther 的 Linux 服务器上执行：

```bash
bash <(curl -Ls https://raw.githubusercontent.com/siryle/Softether-Manager/master/scripts/install.sh)
```

安装脚本会引导配置用户名、密码、端口与路径，下载对应 CPU 架构的 Go 可执行文件，并配置 Systemd 服务及开机自启。运行面板无需 Python、虚拟环境或 Go 编译器。

非交互式无人值守安装：
```bash
bash <(curl -Ls https://raw.githubusercontent.com/siryle/Softether-Manager/master/scripts/install.sh) \
  --non-interactive --username admin --password 'your_password' --port 8443 --path mysecret --json
```

安装后可通过内置的管理命令 `sem` 进行运维：
```bash
sem status               # 查看版本、服务状态与访问地址
sem update               # 在线更新到最新版本
sem password             # 重置面板登录密码
sem users                # 列出所有管理员账户
sem port / sem path      # 更改面板监听端口或隐蔽访问前缀
sem domain               # 域名与 HTTPS 证书管理
sem logs -f              # 查看实时运行日志
sem uninstall            # 卸载面板服务
```

---

## 🛠️ 容器内运维管理命令 (`sem`)

使用 Docker 部署时，可在宿主机通过 `docker exec` 直接调用内置运维工具：

```bash
# 查看面板运行状态及与 SoftEther 的连通性
docker exec -it softether-manager sem status

# 重置或新建面板管理员密码
docker exec -it softether-manager sem password --username admin --password 新密码

# 查看当前已注册的管理员账号
docker exec -it softether-manager sem users

# 更改或重新指定连接的 SoftEther 服务器
docker exec -it softether-manager sem connect --host 192.168.1.100 --port 5555 --password 管理密码
```

---

## 🔒 域名、HTTPS 与安全性

1. **Let's Encrypt 自动证书**：
   在 **面板设置 $\rightarrow$ 域名与 HTTPS** 中输入您的域名，面板通过 Go ACME 客户端完成 80 端口验证、证书获取与到期前 30 天自动续期，无需额外安装 Certbot 或启动定时任务。
2. **防扫描与仅限域名访问**：
   开启“仅允许通过该域名访问”后，任何通过未授权域名或直接输入 IP 访问的请求将被一律返回 404 隐蔽状态码，阻断嗅探与非法探测。
3. **敏感凭据安全加密**：
   SoftEther 管理密码采用 Fernet 加密存储于本地 SQLite 数据库中；面板登录密码使用 scrypt 哈希，会话令牌使用 HMAC 签名；会话 Cookie 具备 `HttpOnly` 与 `SameSite=Lax` 保护。

---

## 🏗️ 架构设计

```
/proc ───────────┐
                  ├──  Go net/http 后端  ──►  Next.js 15 前端单页应用
SoftEther ───────┘   (internal/manager)             (app/web)
 JSON-RPC API
```

- **`cmd/softether-manager/`**：Go 服务和命令行入口，同一个可执行文件提供服务、健康检查、账号重置与连接配置。
- **`internal/manager/`**：基于标准库 `net/http` 的后端，提供 `/api/v1/` 接口、SoftEther JSON-RPC 客户端、纯 Go SQLite 存储、资源监控、流量与会话采样、配额、ACME 证书和更新管理。RPC 定义随程序嵌入，无需外部 Python 模块。
- **`app/web/`**：基于 Next.js 15 构建的纯客户端 Hash 路由静态单页，资源引用全部相对化处理，可在任意子路径或反向代理后运行，原生支持浅色/深色主题与**中英文实时无刷新切换**。

从 Python 版升级时，先停止旧服务并备份整个数据目录，再运行新版安装脚本或替换容器。继续使用原来的 `manager.db`、`session.key`、`encryption.key` 和 `tls/`；账号、加密连接信息、当前版本的历史采样、配额及证书无需手动转换。请勿同时运行两个版本写入同一数据库。单服务器数据库会在事务中迁移为多服务器结构，保留已有账号、凭据、配额及历史记录；仅缺少 HUB 名称的早期采样表会重建。

---

## 💻 本地开发与编译

需要 `go.mod` 指定版本的 Go，以及 Node.js 22 和 npm。

克隆后运行一次 `git config --local core.hooksPath .githooks`，启用提交时自动更新版本号。每次提交已暂存的修改时，`VERSION` 的补丁号自动加一并包含在同一次提交中，例如 `1.0.13 → 1.0.14`。手动暂存的新版本号会被保留，提交失败后重试也不会重复递增。

```bash
# 1. 在项目根目录运行后端 (端口 8000)
go run ./cmd/softether-manager

# 2. 在另一个终端运行前端开发服务器 (端口 3000)
cd app/web
npm ci
NEXT_PUBLIC_API_BASE=http://localhost:8000/ npm run dev

# 3. 构建前端静态资源
npm run build

# 4. 回到项目根目录，构建 Go 可执行文件并检查后端
cd ../..
go build -o softether-manager ./cmd/softether-manager
go test -race ./...
go vet ./...

# 5. 运行编译后的服务（同时提供已构建的前端）
./softether-manager serve
```

默认数据目录为 `./data`，可通过 `SEM_DATA_DIR` 修改。其余 `SEM_*` 环境变量及前端 API 路径保持原有约定。Linux 系统指标通过 `/proc` 读取，macOS 开发环境会显示资源监控不可用。

发布包通过 `bash scripts/build-release.sh --version vX.Y.Z` 构建，包含 Linux amd64、arm64 两种架构的 Go 可执行文件、前端静态资源和安装脚本。

未指定版本时，本地打包与 CI 优先使用当前提交的标签，否则读取 `VERSION`。Docker 和面板也读取该文件。发布 GitHub Release 仍需推送 `vX.Y.Z` 标签。

---

## 📄 开源许可证

本项目遵循 MIT 开源许可证。
SoftEther VPN 是 SoftEther Project / 筑波大学的注册商标。
