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
		title: "Godwit · 开源、AI原生的邮件客户端",
		description: "Godwit是开源的桌面邮件客户端，支持任何IMAP/SMTP邮箱，内置AI助手Wit，用你自己选的模型读信、总结、翻译、起草回复。",
		headline: ["开源、AI原生、", "由你扩展的邮件客户端"],
		sub: "支持任何IMAP/SMTP邮箱。内置助手Wit帮你读信、总结、翻译、起草回复，用的是你自己选的模型。macOS、Windows、Linux都能用。",
		download: "下载",
		downloadMac: "下载macOS版",
		downloadWindows: "下载Windows版",
		downloadLinux: "下载Linux版",
		otherPlatforms: "其他平台与安装方式",
		github: "GitHub",
		featuresTitle: "一个邮件客户端，加上一个懂你邮件的助手",
		features: [
			{
				title: "邮件",
				body: "IMAP/SMTP同步、离线修改、会话、全文搜索、HTML撰写。自动识别Gmail、Outlook、iCloud、QQ邮箱、163/126、腾讯企业邮、飞书、钉钉等邮箱的设置。",
			},
			{ title: "Wit", body: "回答关于你邮件的问题，能读PDF、Office、CSV附件，帮你起草回复。没有你的批准，它不会发出任何邮件。" },
			{ title: "AI操作", body: "总结、翻译、调整语气和长度、即时回复，作用于一封邮件、一段选中的文字或一份草稿。" },
			{
				title: "你的模型",
				body: "任何兼容OpenAI（Chat Completions或Responses）或Anthropic的服务，包括Ollama、LM Studio这样的本地模型。API Key只存在系统钥匙串里。",
			},
			{
				title: "扩展",
				body: "放一个SKILL.md就多一个按钮；TypeScript插件声明权限后，能加卡片、动作和Wit工具；MCP服务器给Wit接入外部工具。内置的AI功能走的也是同样的扩展点。",
			},
			{ title: "给其他Agent", body: "godwit mcp是一个MCP服务器：Claude、Cursor等agent可以读你的邮件、创建草稿，但不能发送。" },
		],
		privacy: {
			title: "你的邮件只去你允许的地方",
			sub: "没有Godwit账号，没有遥测，也没有转发邮件的中间服务器。下面就是Godwit会发起的全部连接。",
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
				cloudHost: "api.openai.com或你填的地址",
				localHost: "localhost:11434 · Ollama",
				cloudWhat: "只有你让AI处理的内容，发给你自己选的服务商。",
				localWhat: "模型跑在这台电脑上，邮件不出本机。",
			},
			rest: [
				{ name: "插件与MCP", host: "只限你批准的域名", what: "插件只能连它声明过、你批准过的域名；MCP的每次写操作都要你确认。" },
				{ name: "远程图片", host: "发件人的服务器", what: "默认拦截，你点「加载图片」才会连接。" },
				{ name: "检查更新", host: "dl.godwit.email", what: "只下载新版本，不上传任何东西。" },
			],
			none: { name: "Godwit的服务器", host: "不存在", what: "你的邮件、密码和对话，我们一样都收不到。" },
		},
		downloadTitle: "下载Godwit",
		downloadSub: "装好之后，Godwit会自己检查并安装更新。",
		appleSilicon: "Apple芯片",
		intel: "Intel",
		x64: "x64",
		arm64: "ARM64",
		macNote: "磁盘映像，需要macOS 12或更新版本。",
		windowsNote: "商店版由Microsoft Store安装和更新；独立安装包为当前用户安装，不需要管理员权限。",
		microsoftStore: "从Microsoft Store获取",
		installer: "x64独立安装包",
		otherVersions: "其他版本",
		windowsArm: "ARM64独立安装包",
		linuxNote: "Debian / Ubuntu软件包。",
		homebrew: "用Homebrew安装",
		installScript: "不用root，为当前用户安装",
		license: "以Apache-2.0许可证开源",
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
				body: "IMAP/SMTP sync, offline changes, conversations, full-text search and HTML compose. Setup is detected for Gmail, Outlook, iCloud, Yahoo, QQ Mail, 163/126, Lark/Feishu, DingTalk and more.",
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
