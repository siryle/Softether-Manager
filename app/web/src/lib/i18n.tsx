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
  "CPU": "CPU 负载",
  "Memory": "内存占用",
  "Disk": "磁盘使用",
  "Network": "网络吞吐",
  "Swap": "交换分区",
  "Uptime": "运行时间",
  "Online": "在线",
  "Offline": "离线",
  "Sessions": "活跃会话",
  "Transfer": "传输流量",
  "Active sessions": "在线会话",
  "Total users": "用户总数",

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
  "Description": "说明",
  "Created": "创建时间",
  "Updated": "更新时间",
  "Reset": "重置",
  "Apply": "应用",

  // --- Hub Tabs & Subscreens ---
  "Overview": "概览",
  "Groups": "用户组",
  "Access list": "访问控制 (ACL)",
  "SecureNAT": "虚拟 NAT & DHCP",
  "Cascades": "级联连接",
  "Security": "安全与证书",
  "Tables": "MAC/IP 映射表",
  "Take offline": "下线 HUB",
  "Put online": "上线 HUB",
  "Delete hub": "删除 HUB",
  "Enable SecureNAT": "启用 SecureNAT",
  "Disable SecureNAT": "停用 SecureNAT",

  // --- Users & Policies ---
  "All Users": "全部用户",
  "Create user": "创建用户",
  "Edit user": "编辑用户",
  "Delete user": "删除用户",
  "Download .vpn": "下载 .vpn 配置文件",
  "Auth type": "认证类型",
  "Security policy": "安全策略",
  "Real name": "真实姓名",
  "Note": "备注",
  "Group": "所属组",
  "Expires": "过期时间",
  "Traffic limit": "流量限额",
  "Reset transfer": "重置已用流量",
  "Cut sessions": "断开会话",
  "Disconnect": "断开",
  "Anonymous": "匿名认证",
  "User certificate": "用户证书认证",
  "Signed certificate": "CA 签名证书认证",
  "RADIUS": "RADIUS 认证",
  "NT domain": "Windows NT 域认证",

  // --- Server Settings ---
  "Listeners": "监听端口",
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
