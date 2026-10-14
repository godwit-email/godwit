// The site's text, in the words of the README and the app's own strings
// (internal/i18n), so the page promises no more than the app does.

import type { Lang } from "./locales";

type Feature = { title: string; body: string };

/** One place Godwit connects to: where, and what goes there. */
type Connection = { name: string; host: string; what: string };

/**
 * Every place the app connects to, as the code does it (core, plugins, the
 * mygo updater): no telemetry, no account, no relay. The model row toggles
 * between a cloud service and a local one.
 */
interface Privacy {
	title: string;
	sub: string;
	local: Feature[];
	listTitle: string;
	mail: Connection;
	model: { name: string; cloud: string; local: string; cloudHost: string; localHost: string; cloudWhat: string; localWhat: string };
	rest: Connection[];
	none: Connection;
}

export interface Strings {
	title: string;
	description: string;
	/** Phrases that wrap as wholes. */
	headline: string[];
	sub: string;
	download: string;
	downloadMac: string;
	downloadWindows: string;
	downloadLinux: string;
	otherPlatforms: string;
	github: string;
	featuresTitle: string;
	features: Feature[];
	privacy: Privacy;
	downloadTitle: string;
	downloadSub: string;
	appleSilicon: string;
	intel: string;
	x64: string;
	arm64: string;
	macNote: string;
	windowsNote: string;
	microsoftStore: string;
	installer: string;
	otherVersions: string;
	windowsArm: string;
	linuxNote: string;
	homebrew: string;
	installScript: string;
	license: string;
	privacyPolicy: string;
	/** The newest release, which src/worker.ts fills in: "v1.2.0" goes after released/before whatsNew. */
	release: { out: string; whatsNew: string; latest: string; on: string; notes: string };
	/** Where the name comes from, the README's sentence. */
	name: string;
	copy: string;
	copied: string;
}

export const strings: Record<Lang, Strings> = {
	"zh-Hans": {
		title: "Godwit · 开源、AI 原生的邮件客户端",
		description: "Godwit 是开源的桌面邮件客户端，支持任何 IMAP/SMTP 邮箱，内置 AI 助手 Wit，用你自己选的模型读信、总结、翻译、起草回复。",
		headline: ["开源、AI 原生、", "由你扩展的邮件客户端"],
		sub: "支持任何 IMAP/SMTP 邮箱。内置助手 Wit 帮你读信、总结、翻译、起草回复，用的是你自己选的模型。macOS、Windows、Linux 都能用。",
		download: "下载",
		downloadMac: "下载 macOS 版",
		downloadWindows: "下载 Windows 版",
		downloadLinux: "下载 Linux 版",
		otherPlatforms: "其他平台与安装方式",
		github: "GitHub",
		featuresTitle: "一个邮件客户端，加上一个懂你邮件的助手",
		features: [
			{
				title: "邮件",
				body: "IMAP/SMTP 同步、离线修改、会话、全文搜索、HTML 撰写。自动识别 Gmail、Outlook、iCloud、QQ 邮箱、163/126、腾讯企业邮、飞书、钉钉等邮箱的设置。",
			},
			{ title: "Wit", body: "回答关于你邮件的问题，能读 PDF、Office、CSV 附件，帮你起草回复。没有你的批准，它不会发出任何邮件。" },
			{ title: "AI 操作", body: "总结、翻译、调整语气和长度、即时回复，作用于一封邮件、一段选中的文字或一份草稿。" },
			{
				title: "你的模型",
				body: "任何兼容 OpenAI（Chat Completions 或 Responses）或 Anthropic 的服务，包括 Ollama、LM Studio 这样的本地模型。API Key 只存在系统钥匙串里。",
			},
			{
				title: "扩展",
				body: "放一个 SKILL.md 就多一个按钮；TypeScript 插件声明权限后，能加卡片、动作和 Wit 工具；MCP 服务器给 Wit 接入外部工具。内置的 AI 功能走的也是同样的扩展点。",
			},
			{ title: "给其他 Agent", body: "godwit mcp 是一个 MCP 服务器：Claude、Cursor 等 agent 可以读你的邮件、创建草稿，但不能发送。" },
		],
		privacy: {
			title: "你的邮件只去你允许的地方",
			sub: "没有 Godwit 账号，没有遥测，也没有转发邮件的中间服务器。下面就是 Godwit 会发起的全部连接。",
			local: [
				{ title: "邮件存在这台电脑上", body: "同步下来的邮件、草稿和搜索索引都在本机的数据目录里。" },
				{ title: "密码只存在系统钥匙串", body: "不会写进配置文件，也不会出现在日志里。" },
			],
			listTitle: "网络连接",
			mail: { name: "你的邮件服务器", host: "imap.qq.com · smtp.qq.com", what: "收发邮件，直接连接，不经过任何中转。" },
			model: {
				name: "你的模型",
				cloud: "云端",
				local: "本机",
				cloudHost: "api.openai.com 或你填的地址",
				localHost: "localhost:11434 · Ollama",
				cloudWhat: "只有你让 AI 处理的内容，发给你自己选的服务商。",
				localWhat: "模型跑在这台电脑上，邮件不出本机。",
			},
			rest: [
				{ name: "插件与 MCP", host: "只限你批准的域名", what: "插件只能连它声明过、你批准过的域名；MCP 的每次写操作都要你确认。" },
				{ name: "远程图片", host: "发件人的服务器", what: "默认拦截，你点「加载图片」才会连接。" },
				{ name: "检查更新", host: "dl.godwit.email", what: "只下载新版本，不上传任何东西。" },
			],
			none: { name: "Godwit 的服务器", host: "不存在", what: "你的邮件、密码和对话，我们一样都收不到。" },
		},
		downloadTitle: "下载 Godwit",
		downloadSub: "装好之后，Godwit 会自己检查并安装更新。",
		appleSilicon: "Apple 芯片",
		intel: "Intel",
		x64: "x64",
		arm64: "ARM64",
		macNote: "磁盘映像，需要 macOS 12 或更新版本。",
		windowsNote: "商店版由 Microsoft Store 安装和更新；独立安装包为当前用户安装，不需要管理员权限。",
		microsoftStore: "从 Microsoft Store 获取",
		installer: "x64 独立安装包",
		otherVersions: "其他版本",
		windowsArm: "ARM64 独立安装包",
		linuxNote: "Debian / Ubuntu 软件包。",
		homebrew: "用 Homebrew 安装",
		installScript: "不用 root，为当前用户安装",
		license: "以 Apache-2.0 许可证开源",
		privacyPolicy: "隐私政策",
		release: { out: "已发布", whatsNew: "查看更新", latest: "最新版本", on: "，发布于 ", notes: "更新日志" },
		name: "名字来自斑尾塍鹬（bar-tailed godwit），不经停飞行距离最远的鸟。",
		copy: "复制",
		copied: "已复制",
	},
	en: {
		title: "Godwit · An open-source, AI-native email client",
		description: "Godwit is an open-source desktop email client for any IMAP/SMTP account, with an AI assistant, Wit, that reads, summarizes, translates and drafts mail with the model you choose.",
		headline: ["An open-source, AI-native", "email client you can extend"],
		sub: "Works with any IMAP/SMTP account. Its assistant, Wit, reads, summarizes, translates and drafts mail with the model you choose. For macOS, Windows and Linux.",
		download: "Download",
		downloadMac: "Download for macOS",
		downloadWindows: "Download for Windows",
		downloadLinux: "Download for Linux",
		otherPlatforms: "Other platforms and ways to install",
		github: "GitHub",
		featuresTitle: "An email client, and an assistant that knows your mail",
		features: [
			{
				title: "Mail",
				body: "IMAP/SMTP sync, offline changes, conversations, full-text search and HTML compose. Setup is detected for Gmail, Outlook, iCloud, Yahoo, QQ Mail, 163/126, Feishu, DingTalk and more.",
			},
			{ title: "Wit", body: "Answers questions about your mail, reads attachments (PDF, Office, CSV) and drafts replies. It never sends without your approval." },
			{ title: "AI actions", body: "Summarize, translate, adjust tone and length, and quick replies, on a message, a selection or a draft." },
			{
				title: "Your models",
				body: "Any OpenAI-compatible (Chat Completions or Responses) or Anthropic-compatible service, including local ones like Ollama and LM Studio. API keys stay in the system keychain.",
			},
			{
				title: "Extensions",
				body: "Drop in a SKILL.md and a button appears. TypeScript plugins with declared permissions add cards, actions and Wit tools; MCP servers give Wit external tools. The built-in AI features use the same extension points.",
			},
			{ title: "For other agents", body: "godwit mcp is an MCP server that lets Claude, Cursor and other agents read your mail and create drafts, but never send." },
		],
		privacy: {
			title: "Your mail goes only where you let it",
			sub: "No Godwit account, no telemetry, no server relaying your mail. Below is every connection Godwit makes.",
			local: [
				{ title: "Your mail stays on this computer", body: "Synced mail, drafts and the search index live in the data directory on this computer." },
				{ title: "Passwords live only in the system keychain", body: "Never written to config files, never shown in logs." },
			],
			listTitle: "Network connections",
			mail: { name: "Your mail server", host: "imap.gmail.com · smtp.gmail.com", what: "Sends and receives your mail, directly, with nothing in between." },
			model: {
				name: "Your model",
				cloud: "Cloud",
				local: "Local",
				cloudHost: "api.openai.com, or the address you enter",
				localHost: "localhost:11434 · Ollama",
				cloudWhat: "Only what you ask the AI to work on, sent to the provider you chose.",
				localWhat: "The model runs on this computer; your mail never leaves it.",
			},
			rest: [
				{ name: "Plugins and MCP", host: "Only hosts you approved", what: "A plugin reaches only the hosts it declared and you approved; every MCP write asks you first." },
				{ name: "Remote images", host: "The sender's server", what: "Blocked by default, loaded only when you click Load images." },
				{ name: "Update checks", host: "dl.godwit.email", what: "Downloads new versions; uploads nothing." },
			],
			none: { name: "Godwit's servers", host: "None", what: "Your mail, passwords and chats never reach us." },
		},
		downloadTitle: "Download Godwit",
		downloadSub: "Once installed, Godwit checks for updates and installs them itself.",
		appleSilicon: "Apple silicon",
		intel: "Intel",
		x64: "x64",
		arm64: "ARM64",
		macNote: "Disk image, macOS 12 or later.",
		windowsNote: "The Store installs and updates it for you; the standalone installer is for the current user, without admin rights.",
		microsoftStore: "Get it from the Microsoft Store",
		installer: "x64 installer",
		otherVersions: "Other versions",
		windowsArm: "ARM64 installer",
		linuxNote: "Debian / Ubuntu package.",
		homebrew: "With Homebrew",
		installScript: "For the current user, no root",
		license: "Open source under the Apache-2.0 license",
		privacyPolicy: "Privacy Policy",
		release: { out: "is out", whatsNew: "What's new", latest: "Latest version", on: ", released ", notes: "Release notes" },
		name: "Named for the bar-tailed godwit, which holds the record for the longest non-stop bird flight.",
		copy: "Copy",
		copied: "Copied",
	},
};
