// The privacy policy, in the words of the app's own strings (internal/i18n)
// and what the code does (core, plugins, the mygo updater), so it promises no
// more than Godwit does. Change it together with the behavior it describes.

import type { Lang } from "./locales";

/** A heading and its paragraphs; a paragraph with a lead is "lead: text". */
interface Section {
	title: string;
	body: string[];
}

export interface Policy {
	title: string;
	description: string;
	updated: string;
	back: string;
	home: string;
	sections: Section[];
}

const repo = "https://github.com/godwit-email/godwit";

export const policy: Record<Lang, Policy> = {
	"zh-Hans": {
		title: "Godwit隐私政策",
		description: "Godwit在你的电脑上保存什么、会连接哪些服务，以及哪些数据永远不会到我们手里。",
		updated: "最后更新：2026年10月31日",
		back: "返回首页",
		home: "/zh-hans/",
		sections: [
			{
				title: "概要",
				body: [
					"Godwit是开源的桌面邮件客户端，由The Godwit Authors开发。这份政策适用于Godwit应用（包括从官网、Homebrew或Microsoft Store获得的版本）和godwit.email网站。",
					"Godwit没有账号体系，应用里没有遥测，也没有转发你邮件的服务器。你的邮件、密码和与AI的对话，不会到达我们。应用只会连接你自己配置的服务，以及下面列出的几个地方。",
				],
			},
			{
				title: "保存在你电脑上的数据",
				body: [
					"同步下来的邮件、草稿、附件、搜索索引、设置和图片缓存，都保存在本机的数据目录里：Windows是用户目录下的 .godwit，macOS是 ~/Library/Application Support/Godwit，Linux是 ~/.config/Godwit。",
					"邮箱密码、登录令牌和模型服务的API Key保存在系统钥匙串里，不会写进配置文件，也不会出现在日志里。",
					"这些数据只在你的电脑上。想清除它们，删除账号后再删除数据目录即可。",
				],
			},
			{
				title: "Godwit会连接哪些地方",
				body: [
					"你的邮件服务商：通过IMAP/SMTP直接收发邮件，不经过任何中转。Outlook账号用Microsoft登录，会连接Microsoft的登录服务。",
					"你配置的模型服务：只有在你添加了模型服务之后，Godwit才会把内容发给它。包括你让AI处理的内容（总结、翻译、写作、和Wit的对话），也包括自动分类：标签功能默认开启，会把收件箱的新邮件交给你配置的模型来贴标签，通知过滤和验证码识别同样用模型判断。可以在设置里关闭自动标签。使用本机模型（例如Ollama）时，内容不会离开这台电脑。模型服务商如何处理这些内容，由他们的政策决定。",
					"远程图片和发件人头像：默认自动加载。Godwit替你下载，不带Cookie和Referer；新邮件到达时就提前下载，所以发件人看不出你何时阅读，但他们仍会看到这台电脑的IP地址。旧邮件、缓存未命中或过期、手动加载时，读信时仍可能发起请求，对方可能因此推测阅读时间。头像可能向发件人的网站和Gravatar请求，向Gravatar查询时用的是发件人邮箱的SHA-256哈希，不是邮箱本身。垃圾邮件里的图片不会自动加载。可以在设置的「远程图片」里改成「仅可信发件人」或「每次询问」。",
					"插件和MCP：插件只能连接它声明过、并且你批准过的域名。MCP服务器是你自己添加的：stdio类型会在你的电脑上作为程序运行，只带PATH和你填写的环境变量；http类型连接你填的地址。每个服务器要先批准一次，每次写操作都要你确认。",
					"本地API：默认关闭，打开后只在本机监听，本机上的脚本和AI工具可以通过它访问Godwit。完整的本地API能读信、发信、修改账号和设置；通过godwit mcp接入的AI工具只能读邮件、创建草稿，不能发送。",
					"检查更新：从dl.godwit.email获取新版本信息和安装包，不上传任何东西。通过Microsoft Store安装的版本由商店更新：打开「关于」时，Godwit向Microsoft Store的公开目录（displaycatalog.mp.microsoft.com）查询最新版本号，不带任何账号信息。",
				],
			},
			{
				title: "网站",
				body: [
					"godwit.email是静态网站，通过Cloudflare提供服务，Cloudflare可能保留标准的访问日志，例如IP地址。网站（不是应用）使用Umami统计访问量和按钮点击，例如下载了哪个平台的版本，不使用Cookie，也不记录你输入或复制的内容；通过网站下载时，服务器会把平台、来源网站和浏览器信息（不含IP地址）记一次下载。应用本身不含任何统计或遥测。",
				],
			},
			{
				title: "我们不做的事",
				body: [
					"我们没有接收你邮件、密码或对话的服务器，不出售数据，不投放广告，也不会把你的数据提供给第三方，因为我们根本没有这些数据。",
				],
			},
			{
				title: "Microsoft Store",
				body: ["如果你从Microsoft Store安装Godwit，Microsoft会按它自己的隐私声明处理下载、安装和诊断信息。这些数据不会交给Godwit。"],
			},
			{
				title: "你的选择",
				body: [
					"你可以随时：删除账号；关闭自动标签；使用本机模型，或者不添加任何模型服务；把「远程图片」改为「每次询问」；关闭本地API；卸载插件和MCP服务器；删除数据目录。",
				],
			},
			{
				title: "儿童",
				body: ["Godwit不是面向儿童的产品，我们也不会有意收集儿童的信息。"],
			},
			{
				title: "变更与联系",
				body: [
					`这份政策随Godwit一起更新，修改历史公开在 ${repo}。重大变更会写进发布说明。`,
					`有问题请在 ${repo}/issues提出。`,
				],
			},
		],
	},
	en: {
		title: "Godwit Privacy Policy",
		description: "What Godwit keeps on your computer, which services it connects to, and what never reaches us.",
		updated: "Last updated: October 31, 2026",
		back: "Back to home",
		home: "/en/",
		sections: [
			{
				title: "Summary",
				body: [
					"Godwit is an open-source desktop email client made by The Godwit Authors. This policy covers the Godwit app, however you got it (the website, Homebrew or the Microsoft Store), and the godwit.email website.",
					"Godwit has no accounts, the app has no telemetry, and no server relays your mail. Your mail, passwords and chats with AI never reach us. The app connects only to the services you set up and the few places listed below.",
				],
			},
			{
				title: "What stays on your computer",
				body: [
					"Synced mail, drafts, attachments, the search index, settings and the image cache live in the data directory on this computer: .godwit in your user folder on Windows, ~/Library/Application Support/Godwit on macOS, ~/.config/Godwit on Linux.",
					"Mail passwords, sign-in tokens and model API keys live in the system keychain. They are never written to config files and never appear in logs.",
					"All of this stays on your computer. To remove it, delete your accounts and then the data directory.",
				],
			},
			{
				title: "Where Godwit connects",
				body: [
					"Your mail provider: mail is sent and received over IMAP/SMTP, directly, with nothing in between. Outlook accounts sign in with Microsoft, which connects to Microsoft's sign-in service.",
					"Your model services: Godwit sends content to a model only once you add one. That includes what you ask AI to work on (summaries, translation, writing, chats with Wit) and automatic classification: labeling is on by default and gives new Inbox mail to your configured model to label, and notification filtering and verification-code detection also use a model. You can turn automatic labels off in Settings. With a local model such as Ollama, the content never leaves this computer. What a model provider does with it is up to its own policy.",
					"Remote images and sender avatars: loaded automatically by default. Godwit downloads them for you, without cookies or a referrer, and fetches them as new mail arrives, so senders cannot tell when you read a message, but they still see this computer's IP address. For old messages, cache misses or expired entries, and manual loading, requests may still happen while you read, and senders may infer your reading time. Avatars may be requested from the sender's website and from Gravatar, which is queried with a SHA-256 hash of the sender's address, not the address itself. Images in Junk never load by themselves. In Settings › Remote images you can switch to trusted senders only, or ask each time.",
					"Plugins and MCP: a plugin reaches only the hosts it declared and you approved. MCP servers are ones you add: a stdio server runs as a program on your computer with only PATH and the environment variables you set; an http server connects to the address you enter. Each server is approved once, and every write call asks you first.",
					"Local API: off by default. When on, it listens only on this computer, and scripts and AI tools here can reach Godwit through it. The full Local API can read and send mail and change accounts and settings; AI tools connected through godwit mcp can only read mail and create drafts, never send.",
					"Update checks: new version information and installers come from dl.godwit.email; nothing is uploaded. A version installed from the Microsoft Store is updated by the Store; when About opens, Godwit asks the Store's public catalog (displaycatalog.mp.microsoft.com) for the latest version number, without any account information.",
				],
			},
			{
				title: "The website",
				body: [
					"godwit.email is a static website served through Cloudflare, which may keep standard access logs, such as IP addresses. The website, not the app, uses Umami to count visits and button clicks, such as which platform was downloaded, without cookies and without recording anything you type or copy; a download through the website is also counted by the server, with the platform, the referring site and the browser, but not your IP address. The app itself contains no analytics or telemetry.",
				],
			},
			{
				title: "What we do not do",
				body: [
					"We have no servers that receive your mail, passwords or chats. We do not sell data, show ads, or pass your data to third parties, because we do not have it.",
				],
			},
			{
				title: "Microsoft Store",
				body: ["If you install Godwit from the Microsoft Store, Microsoft handles download, installation and diagnostic information under its own privacy statement. That data is not passed to Godwit."],
			},
			{
				title: "Your choices",
				body: [
					"At any time you can: remove an account; turn off automatic labels; use a local model, or add no model service at all; set Remote images to ask each time; turn the Local API off; remove plugins and MCP servers; delete the data directory.",
				],
			},
			{
				title: "Children",
				body: ["Godwit is not directed to children, and we do not knowingly collect their information."],
			},
			{
				title: "Changes and contact",
				body: [
					`This policy is updated with Godwit, and its history is public at ${repo}. Material changes are noted in the release notes.`,
					`Questions: ${repo}/issues.`,
				],
			},
		],
	},
};
