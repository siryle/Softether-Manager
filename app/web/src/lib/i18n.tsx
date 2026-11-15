"use client";

import React, { createContext, useContext, useState, useEffect, useCallback, type ReactNode } from "react";

export type Lang = "zh-CN" | "en";

const STORAGE_KEY = "sem_lang";

const zhCN: Record<string, string> = {
  // --- Auth & Setup ---
  "Set up this panel": "初始化管理面板",
  "SoftEther Manager": "SoftEther 管理器",
  "No account exists yet. The first one you create signs in from now on.":
    "当前尚未创建账户。在此创建的首个账户将作为管理员。",
  "Sign in to manage your VPN servers": "登录以管理您的 SoftEther VPN 服务器",
  "Username": "用户名",
  "Password": "密码",
  "Confirm password": "确认密码",
  "Create account": "创建管理员账户",
  "Sign in": "登录",
  "Sign out": "退出登录",
  "The two passwords do not match": "两次输入的密码不一致",
  "Something went wrong": "操作失败，请重试",

  // --- Shell & Navigation ---
  "Dashboard": "控制面板",
  "Users": "用户管理",
  "Connections": "在线连接",
  "Logs": "运行日志",
  "API console": "API 控制台",
  "Settings": "面板设置",
  "Virtual Hubs": "虚拟 HUB",
  "Server settings": "服务器设置",
  "Connect server": "连接服务器",
  "Theme": "外观主题",
  "Light": "浅色",
  "Dark": "深色",
  "Language": "界面语言",
  "English": "English",
  "Simplified Chinese": "简体中文",
  "Live": "实时",
  "Switch to light theme": "切换为浅色主题",
  "Switch to dark theme": "切换为深色主题",
  "Switch to light": "切换为浅色",
  "Switch to dark": "切换为深色",
  "Expand the sidebar": "展开侧边栏",
  "Collapse the sidebar": "折叠侧边栏",
  "Check for updates": "检查更新",
  "An update is running": "正在更新中",

  // --- Connect Screen ---
  "Connect to SoftEther": "连接到 SoftEther 服务器",
  "The management port and administrator password of the instance on this machine.":
    "配置目标 SoftEther 服务器的管理端口和管理员密码。",
  "Host": "主机地址",
  "127.0.0.1 for local, or LAN IP (e.g. 192.168.1.x) / hostname.":
    "本机请填 127.0.0.1，局域网请填内网 IP（如 192.168.1.x）或主机名。",
  "Management port": "管理端口",
  "5555 on a stock install; 443 and 992 also answer.":
    "默认安装为 5555 端口；443 与 992 端口通常也可应答。",
  "Administrator password": "管理员密码",
  "Administrator password (empty keeps the stored one)":
    "管理员密码（留空则保持当前已保存密码）",
  "Test connection": "测试连接",
  "Save & connect": "保存并连接",
  "Connected.": "已连接。",
  "answered on": "应答于",
  "No SoftEther on this machine yet? Install it first — the panel manages an existing instance; it does not install one.":
    "新安装的 SoftEther 服务器默认密码为空，可留空直接测试。本面板用于管理现有的 SoftEther 实例，不会自动安装服务端。",
  "VPN server": "VPN 服务器",
  "management address": "管理地址",
  "state": "状态",
  "not connected": "未连接",
  "Edit connection": "编辑连接",
  "The SoftEther administrator password itself is changed under Server settings → Encryption; changing it there keeps this connection in step automatically.":
    "SoftEther 管理员密码可在“服务器设置 → 加密”中修改；在此修改后将自动同步该连接密码。",

  // --- Dashboard ---
  "Good morning": "早上好",
  "Good afternoon": "下午好",
  "Good evening": "晚上好",
  "Good night": "夜深了",
  "Your VPN server is healthy.": "VPN 服务器运行状态良好。",
  "The VPN server is not answering.": "VPN 服务器未响应。",
  "Here's an overview of this machine and its VPN server.":
    "以下是当前服务器与 VPN 的运行概览。",
  "not connected to SoftEther yet": "尚未连接到 SoftEther 服务器",
  "Connect the server": "连接服务器",
  "Create hub": "创建虚拟 HUB",
  "Hub name": "虚拟 HUB 名称",
  "Hub password": "HUB 管理员密码",
  "Server health": "服务器监控",
  "CPU": "CPU",
  "Memory": "内存",
  "Disk": "磁盘存储",
  "Network": "网络流量",
  "Swap": "交换分区 (Swap)",
  "Uptime": "运行时间",
  "Online": "在线",
  "Offline": "离线",
  "Sessions": "活跃会话",
  "Transfer": "传输流量",
  "Active sessions": "在线会话",
  "Total users": "用户总数",
  "across all hubs": "跨所有虚拟 HUB",
  "accounts on the server": "服务器注册账户数",
  "Received": "已接收流量",
  "Sent": "已发送流量",
  "since the server started": "自服务启动以来",
  "1 hub on this server": "本服务器上共有 1 个虚拟 HUB",
  "{count} hubs on this server": "本服务器上共有 {count} 个虚拟 HUB",
  "where users, sessions and access control live": "用户、会话与访问控制所在的虚拟网络中心",
  "A Virtual Hub is where users, sessions and access control live. Create the first one.":
    "虚拟 HUB 是用户、会话和访问策略的核心容器。创建首个 HUB。",
  "{users} users · {sessions} sessions · {type}": "{users} 位用户 · {sessions} 个会话 · {type}",
  "This server": "当前服务器",
  "VPN uptime": "VPN 运行时间",
  "version unknown": "未知版本",
  "New hub": "新建 HUB",
  "Access protocols": "接入协议",
  "ways into this server": "连接至本服务器的 VPN 协议方式",
  "default hub": "默认 HUB",
  "default hub {hub}": "默认 HUB {hub}",
  "enabled": "已启用",
  "off": "已停用",
  "no listeners": "无监听端口",
  "Tools": "快捷工具",
  "TCP connections into the server, live": "实时入站 TCP 连接",
  "server, hub security and packet logs": "系统、安全与数据包日志",
  "every RPC method, callable raw": "底层原始 RPC 调用",
  "listeners, certificate, bridges…": "端口、SSL 证书、网桥…",
  "This machine": "宿主机硬件状态",
  "Resource monitoring is asleep.": "系统资源监控未启用。",
  "It runs where /proc exists — a Linux host.": "仅在存在 /proc 的 Linux 主机上运行。",

  // --- Resource Cards ---
  "Show more": "展开详情",
  "Show less": "收起详情",
  "Load 1 min": "1 分钟负载",
  "5 min": "5 分钟负载",
  "15 min": "15 分钟负载",
  "CPU User": "用户空间",
  "System": "系统内核",
  "I/O wait": "IO 等待",
  "Steal": "虚拟化抢占",
  "{running} running of {processes} processes": "{running} 个运行中 / 共 {processes} 个进程",
  "Used": "已用",
  "Available": "可用",
  "Total": "总量",
  "Free": "空闲",
  "Buffers": "缓冲区",
  "Cached": "页面缓存",
  "None configured. Fine with enough memory; without it, the kernel's only relief valve is killing processes.":
    "未配置交换空间。物理内存充足时无需担心；若内存耗尽，系统内核只能通过 OOM Killer 终止进程。",
  "No mounted filesystems reported.": "未报告挂载的文件系统。",
  "↓ Download": "↓ 实时下行",
  "↑ Upload": "↑ 实时上行",
  "total": "总计",

  // --- Status & Pills ---
  "online": "在线",
  "offline": "离线",
  "denied": "已禁止",
  "expired": "已过期",
  "connected": "已连接",
  "connecting": "连接中",
  "disconnected": "已断开",
  "listening": "监听中",
  "cannot open": "无法打开",
  "pending": "等待中",
  "running": "运行中",
  "succeeded": "成功",
  "failed": "失败",
  "canceled": "已取消",
  "probing": "检测中",
  "error": "错误",
  "disabled": "已禁用",
  "busy": "繁忙",
  "idle": "空闲",
  "initializing": "初始化中",
  "login": "登录中",
  "additional": "附加连接",
  "RPC / farm": "RPC / 集群通讯",
  "admin": "管理员管理",
  "management": "管理连接",
  "client": "客户端",
  "cascade": "级联连接",
  "bridge": "本地网桥",
  "L3 switch": "三层交换机",
  "Standalone": "独立运行模式",
  "Static (cluster)": "静态集群",
  "Dynamic (cluster)": "动态集群",

  // --- Common Buttons & Controls ---
  "Cancel": "取消",
  "Save": "保存",
  "Save changes": "保存更改",
  "Confirm": "确认",
  "Delete": "删除",
  "Edit": "编辑",
  "Refresh": "刷新",
  "Search": "搜索",
  "Close": "关闭",
  "Back": "返回",
  "OK": "确定",
  "Loading": "加载中",
  "loading": "加载中...",
  "Actions": "操作",
  "Status": "状态",
  "Name": "名称",
  "Type": "类型",
  "Kind": "类别",
  "Source": "来源",
  "Description": "说明",
  "Created": "创建时间",
  "Updated": "更新时间",
  "Reset": "重置",
  "Apply": "应用",
  "Bring it online now": "立即上线",
  "Letters, digits, - and _ are safe.": "支持英文字母、数字、短横线 - 与下划线 _。",
  "Optional — lets someone administer only this hub.": "选填 — 允许专职管理员单独管理此 HUB。",

  // --- Users Table & Detail ---
  "All Users": "全部用户",
  "Every user on this server.": "本服务器上的所有注册用户。",
  "{users} across {hubs} hubs": "共 {users} 位用户，分布在 {hubs} 个虚拟 HUB",
  "name, hub, group, note…": "搜索用户名、HUB、组、备注…",
  "name, group, note…": "搜索用户名、组、备注…",
  "New user": "新建用户",
  "Create user": "创建用户",
  "Edit user": "编辑用户",
  "Delete user": "删除用户",
  "Download .vpn": "下载 .vpn",
  "Download .vpn connection file": "下载 .vpn 配置文件",
  "Download connection file": "下载连接文件",
  "User": "用户",
  "Hub": "虚拟 HUB",
  "Group": "所属组",
  "Auth": "认证方式",
  "Last login": "最后登录",
  "Logins": "登录次数",
  "Limit": "限额",
  "Expires": "过期时间",
  "never": "永不过期",
  "nothing matches": "未找到匹配项",
  "no users yet": "暂无用户",
  "No user, hub, group or note matches that search.": "没有找到匹配该搜索条件的用户、HUB、组或备注。",
  "A user is one VPN identity, living in one Virtual Hub.": "用户是属于某个虚拟 HUB 的 VPN 身份凭证。",
  "Create the first user": "创建首个用户",
  "A user is one VPN identity: a name, a way to authenticate, and the policy that binds it.":
    "用户是具有名称、认证方式与安全策略的 VPN 身份凭证。",
  "group": "组",
  "data": "流量",
  "seen": "活跃",
  "limit": "限额",
  "logins": "次登录",
  "downloaded": "已下载",
  "uploaded": "已上传",
  "online now": "当前在线",
  "required CN": "要求的证书 CN",
  "RADIUS user": "RADIUS 用户名",
  "NT user": "NT 域用户名",
  "Traffic limit": "流量限额",
  "Usage": "用量趋势",
  "Live sessions": "在线会话",
  "Session history": "历史会话记录",
  "Security policy": "安全策略",
  "Save policy": "保存策略",
  "Discard": "放弃更改",
  "Change": "修改",
  "Edit profile": "编辑资料",
  "not connected right now": "当前未连接",
  "Sessions appear here the moment this user connects, and can be cut from here too.":
    "用户建立连接后将在此显示其实时会话，亦可在此将其断开。",
  "no logins recorded yet": "尚无登录记录",
  "Every connection this user makes lands here — client IP, when it started and ended, and the bytes it moved — kept for the retention window set in Settings.":
    "该用户的所有连接记录均保存在此（客户端 IP、起止时间与流量），保留时长可在设置中调整。",
  "Started": "开始时间",
  "Ended": "结束时间",
  "Duration": "连接时长",
  "Client IP": "客户端 IP",
  "Transferred": "传输流量",
  "Auth type": "认证类型",
  "Real name": "真实姓名",
  "Full name": "真实姓名",
  "Note": "备注",
  "Authentication": "认证方式",
  "Password (leave empty to keep)": "密码（留空保持不变）",
  "New password (leave empty to keep)": "新密码（留空保持不变）",
  "Reset transfer": "重置已用流量",
  "Cut sessions": "断开会话",
  "Disconnect": "断开连接",
  "Anonymous": "匿名认证",
  "User certificate": "用户证书认证",
  "Signed certificate": "CA 签名证书认证",
  "RADIUS": "RADIUS 认证",
  "NT domain": "Windows NT 域认证",

  // --- Hub Tabs & Subscreens ---
  "Overview": "概览",
  "Groups": "用户组",
  "Access control": "访问控制",
  "Access list": "访问控制 (ACL)",
  "SecureNAT": "虚拟 NAT & DHCP",
  "Cascade": "级联连接",
  "Cascades": "级联连接",
  "Security": "安全与证书",
  "Tables": "MAC/IP 映射表",
  "Hub settings": "HUB 设置",
  "Take offline": "下线 HUB",
  "Put online": "上线 HUB",
  "Delete hub": "删除 HUB",
  "Enable SecureNAT": "启用 SecureNAT",
  "Disable SecureNAT": "停用 SecureNAT",
  "Throughput": "流量吞吐",
  "Details": "详细信息",
  "lifetime traffic": "累计流量",
  "MAC entries": "MAC 映射数",
  "IP entries": "IP 映射数",
  "last comm": "最后通讯",
  "Hub online": "HUB 在线状态",

  // --- Connections ---
  "live TCP connections into the VPN server": "实时连接至 VPN 服务器的 TCP 连接",
  "no connections": "暂无活动连接",
  "Disconnect this connection?": "确认断开此连接？",

  // --- Logs ---
  "Files": "日志文件",
  "no log files": "暂无日志文件",
  "Logging may be switched off, or nothing has happened yet.": "日志功能可能未开启，或尚未产生日志记录。",
  "Read more": "读取更多",
  "Jump to tail": "跳至末尾",
  "size": "大小",
  "updated": "更新时间",

  // --- API Console ---
  "every documented RPC, raw": "直接调用所有官方 RPC 底层接口",
  "method name…": "搜索 RPC 方法名…",
  "pick a method": "请选择一个 RPC 方法",
  "The parameters template fills in with the documented example values; edit and run. Writes here are as real as anywhere else in the panel.":
    "选择方法后将自动填充参数模板；编辑参数后点击调用。请注意写入操作会实际修改服务器配置。",
  "parameters": "调用参数 (JSON)",
  "result": "返回结果",
  "Call {name}": "调用 {name}",

  // --- Server Settings ---
  "TCP listeners": "TCP 监听端口",
  "Listeners": "监听端口",
  "Add port": "添加端口",
  "Add listener": "添加监听端口",
  "Delete listener": "删除监听端口",
  "Encryption": "加密与算法",
  "L2TP / IPsec": "L2TP / IPsec",
  "OpenVPN & MS-SSTP": "OpenVPN 与 MS-SSTP",
  "VPN Azure": "VPN Azure 云穿透",
  "Dynamic DNS": "动态域名 (DDNS)",
  "Local Bridges": "本地网桥",
  "Layer-3 Switches": "三层交换机",
  "Clustering": "集群设置",
  "Syslog": "Syslog 日志",
  "Keep-alive": "网络保活",
  "Config backup": "配置备份",

  // --- Policy Groups & Fields ---
  "Access": "访问权限",
  "Bandwidth & sessions": "带宽与会话",
  "Address limits": "地址限制",
  "Filtering": "数据包过滤",
  "IPv6": "IPv6 策略",
  "Advanced": "高级策略",
  "Apply a security policy": "启用安全策略",
  "Allow access": "允许接入",
  "Concurrent logins": "最大并发登录数",
  "TCP connections per session": "每个会话的最大 TCP 连接数",
  "Idle timeout": "空闲断开超时",
  "Auto-disconnect": "自动断开时长",
  "Forbid password change": "禁止用户修改密码",
  "Forbid saving the password": "禁止客户端保存密码",
  "Upload limit": "上传带宽限制",
  "Download limit": "下载带宽限制",
  "Disable VoIP/QoS priority": "禁用 VoIP/QoS 优先级",
  "Unlimited broadcasts": "广播包不受限制",
  "Monitoring mode": "会话监听/镜像模式",
  "MAC addresses": "最大 MAC 地址数",
  "IPv4 addresses": "最大 IPv4 地址数",
  "IPv6 addresses": "最大 IPv6 地址数",
  "Enforce MAC limit": "严格限制 MAC 地址数",
  "Enforce IPv4 limit": "严格限制 IPv4 地址数",
  "Enforce IPv6 limit": "严格限制 IPv6 地址数",
  "Filter DHCP (IPv4)": "过滤 IPv4 DHCP 数据包",
  "Deny DHCP server role": "禁止充当 DHCP 服务器",
  "Require DHCP addresses": "强制仅允许 DHCP 分配的地址",
  "Deny bridging": "禁止网络桥接",
  "Deny routing (IPv4)": "禁止充当 IPv4 路由器",
  "Deny server role (IPv4)": "禁止充当服务端（拦截入站TCP）",
  "Privacy filter": "用户间相互隔离（隐私过滤器）",
  "Drop all IPv4": "丢弃所有 IPv4 数据包",
  "Drop all IPv6": "丢弃所有 IPv6 数据包",
  "Drop non-IP frames": "丢弃所有非 IP 协议帧",
  "Filter RS/RA": "过滤 IPv6 路由器通告 (RS/RA)",
  "Broadcasts: ARP/DHCP/ICMPv6 only": "广播包限制：仅限 ARP/DHCP/ICMPv6",
  "unlimited": "无限制",
  "server default": "遵循服务端默认",
};

interface I18nApi {
  lang: Lang;
  setLang: (lang: Lang) => void;
  toggleLang: () => void;
  t: (key: string, params?: Record<string, string | number>) => string;
}

const I18nContext = createContext<I18nApi>({
  lang: "zh-CN",
  setLang: () => {},
  toggleLang: () => {},
  t: (key, params) => formatText(key, params),
});

let currentLang: Lang = "zh-CN";

function formatText(text: string, params?: Record<string, string | number>): string {
  if (!params) return text;
  return Object.entries(params).reduce((res, [k, v]) => res.replaceAll(`{${k}}`, String(v)), text);
}

export function translate(text: string, params?: Record<string, string | number>): string {
  if (currentLang === "en") {
    return formatText(text, params);
  }
  const translated = zhCN[text] || text;
  return formatText(translated, params);
}

export const t = translate;

function getInitialLang(): Lang {
  if (typeof window === "undefined") return "zh-CN";
  try {
    const saved = localStorage.getItem(STORAGE_KEY) as Lang;
    if (saved === "zh-CN" || saved === "en") return saved;
  } catch {
    // ignore
  }
  return "zh-CN";
}

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(getInitialLang);

  const setLang = useCallback((nextLang: Lang) => {
    setLangState(nextLang);
    currentLang = nextLang;
    try {
      localStorage.setItem(STORAGE_KEY, nextLang);
      document.documentElement.lang = nextLang;
    } catch {
      // ignore
    }
  }, []);

  const toggleLang = useCallback(() => {
    setLang(lang === "zh-CN" ? "en" : "zh-CN");
  }, [lang, setLang]);

  useEffect(() => {
    currentLang = lang;
    try {
      document.documentElement.lang = lang;
    } catch {
      // ignore
    }
  }, [lang]);

  const tFunc = useCallback(
    (key: string, params?: Record<string, string | number>) => {
      if (lang === "en") return formatText(key, params);
      return formatText(zhCN[key] || key, params);
    },
    [lang]
  );

  return (
    <I18nContext.Provider value={{ lang, setLang, toggleLang, t: tFunc }}>
      {children}
    </I18nContext.Provider>
  );
}

export const useI18n = () => useContext(I18nContext);

/** A compact language toggle button for headers and footers */
export function LanguageButton({ className }: { className?: string }) {
  const { lang, toggleLang } = useI18n();
  return (
    <button
      type="button"
      className={className || "btn btn--sm"}
      onClick={toggleLang}
      title={lang === "zh-CN" ? "Switch to English" : "切换为简体中文"}
      style={{ minWidth: 44, fontSize: 13, fontWeight: 500 }}
    >
      {lang === "zh-CN" ? "中" : "EN"}
    </button>
  );
}
