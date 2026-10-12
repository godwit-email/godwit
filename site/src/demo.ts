// The demo window's mailbox: the sample mail of the design (app/shots_test.go)
// and what the app's AI features and plugins make of it. The labels are the
// app's own strings (internal/i18n).
import type { Lang } from "./locales";

export type Card =
	| { kind: "flight"; meta: string; from: string; to: string; hint: string; action: string }
	| { kind: "otp"; meta: string; code: string; hint: string; action: string; done: string };

export interface Message {
	id: string;
	group: string;
	from: string;
	email: string;
	time: string;
	subject: string;
	preview: string;
	unread?: boolean;
	sent: string;
	body: string[];
	translated?: string[];
	summary?: string[];
	quick?: { kind: string; text: string }[];
	attachment?: { name: string; size: string };
	card?: Card;
	blocked?: { text: string; load: string; trust: string };
}

export interface Demo {
	label: string;
	hint: string;
	mail: string;
	wit: string;
	compose: string;
	allInboxes: string;
	starred: string;
	accounts: { name: string; unread?: number }[];
	folders: string[];
	addAccount: string;
	settings: string;
	inboxTitle: string;
	unreadCount: string;
	all: string;
	unread: string;
	synced: string;
	summarize: string;
	translate: string;
	moreSkill: string;
	summaryTitle: string;
	summaryMeta: string;
	translating: string;
	translated: string;
	showOriginal: string;
	quickTitle: string;
	quickSent: string;
	fromPlugin: string;
	witTitle: string;
	witSub: string;
	witPlaceholder: string;
	witThinking: string;
	witAsks: { q: string; a: string[]; open?: string; openLabel?: string }[];
	selected: string;
	messages: Message[];
}

export const demos: Record<Lang, Demo> = {
	"zh-Hans": {
		label: "Godwit 交互演示",
		hint: "点一点：切换邮件、总结、翻译、即时回复，或者问问 Wit。",
		mail: "邮件",
		wit: "Wit",
		compose: "写邮件",
		allInboxes: "全部收件箱",
		starred: "已标星",
		// hi@ is Godwit's own; E7 is the godwit whose 2007 non-stop flight from
		// Alaska to New Zealand was tracked by satellite, on the East Asian–Australasian Flyway.
		accounts: [{ name: "hi@godwit.email", unread: 2 }, { name: "e7@flyway.example" }],
		folders: ["收件箱", "草稿", "已发送", "归档", "已删除"],
		addAccount: "添加账户",
		settings: "设置",
		inboxTitle: "全部收件箱",
		unreadCount: "封未读",
		all: "全部",
		unread: "未读",
		synced: "已同步 · 刚刚",
		summarize: "总结",
		translate: "翻译",
		moreSkill: "更多 skill",
		summaryTitle: "总结",
		summaryMeta: "summarize · 你的模型",
		translating: "正在翻译…",
		translated: "已翻译为中文 · translate · 你的模型",
		showOriginal: "显示原文",
		quickTitle: "即时回复 · 选一封，进入撰写页再改",
		quickSent: "已放进撰写窗口，改好再发。",
		fromPlugin: "来自插件",
		witTitle: "问 Wit",
		witSub: "它读得到你同步下来的邮件；发信之前，一定先问你。",
		witPlaceholder: "问 Wit 任何关于邮件的事…",
		witThinking: "Wit 正在翻邮件…",
		witAsks: [
			{
				q: "这周有哪些邮件需要我回复？",
				a: [
					"有两封在等你：",
					"1. Lena Fischer 的 Q4 合作提案，她希望你周五前确认时间安排。",
					"2. 国航的值机提醒，CA4518 周六 14:05 从浦东 T2 起飞，现在可以在线值机。",
				],
				open: "lena",
				openLabel: "打开 Lena 的邮件",
			},
			{
				q: "帮我起草给 Lena 的回复，说 11 月可以",
				a: ["草稿已经写好，放在撰写窗口里，等你看过再发：", "「Hi Lena, thanks for the proposal. November works for us, happy to move ahead.」"],
			},
		],
		selected: "lena",
		messages: [
			{
				id: "otp",
				group: "今天",
				from: "Code Host",
				email: "noreply@codehost.example",
				time: "09:41",
				subject: "[Code Host] 你的登录验证码",
				preview: "有人正在尝试登录你的账户，验证码 482913，10 分钟内有效。",
				unread: true,
				sent: "发给 我 · 今天 09:41",
				body: ["有人正在尝试登录你的 Code Host 账户。", "你的验证码是 482913，10 分钟内有效。", "如果不是你本人在操作，忽略这封邮件即可。"],
				card: { kind: "otp", meta: "验证码 · 来自插件 otp-card", code: "482913", hint: "10 分钟内有效", action: "复制", done: "已复制" },
			},
			{
				id: "flight",
				group: "今天",
				from: "中国国际航空",
				email: "checkin@airchina.example",
				time: "08:15",
				subject: "值机提醒：CA4518 上海 → 成都",
				preview: "你的航班 10 月 17 日（周六）14:05 起飞，现已开放在线值机。",
				unread: true,
				sent: "发给 我 · 今天 08:15",
				body: ["尊敬的旅客：", "你的航班 CA4518（上海浦东 T2 → 成都双流 T2）将于 10 月 17 日（周六）14:05 起飞，17:20 到达，现已开放在线值机。", "祝你旅途愉快。"],
				card: { kind: "flight", meta: "航班 CA4518 · 10-17 周六 14:05 → 17:20 · 来自插件 flight-cards", from: "PVG", to: "CTU", hint: "浦东 T2 → 双流 T2 · 空客 A321 · 座位 32A", action: "在线值机" },
				blocked: { text: "已阻止 3 张远程图片，防止发件人追踪你是否打开了邮件", load: "加载图片", trust: "始终信任此发件人" },
				quick: [
					{ kind: "确认", text: "好的，已经值机了，谢谢。" },
					{ kind: "改签", text: "你好，我想把航班改到后一天，请问怎么操作？" },
				],
			},
			{
				id: "lena",
				group: "昨天",
				from: "Lena Fischer",
				email: "lena@partner.example",
				time: "昨天",
				subject: "Q4 partnership proposal",
				preview: "Hi, Following up on our call last week, I put together a draft…",
				sent: "发给 我 · 昨天 21:03",
				body: [
					"Hi,",
					"Following up on our call last week, I put together a draft of the Q4 partnership proposal. The highlights are a joint launch in November and a co-hosted webinar.",
					"Could you take a look by Friday and let me know whether the timeline works for your team?",
					"Best, Lena",
				],
				translated: [
					"你好，",
					"接着上周的通话，我整理了一份 Q4 合作的提案草稿。要点是 11 月联合发布，以及一场联合网络研讨会。",
					"能否在周五前看一下，告诉我时间安排对你们来说是否可行？",
					"祝好，Lena",
				],
				summary: ["对方发来 Q4 合作提案草稿（附件 PDF）。", "要点：11 月联合发布、一场联合网络研讨会。", "需要你在周五前确认时间安排是否可行。"],
				quick: [
					{ kind: "同意", text: "Hi Lena, thanks for the proposal. November works for us, happy to move ahead." },
					{ kind: "同意但调整", text: "Hi Lena, November works, but could we move the webinar one week later?" },
					{ kind: "稍后答复", text: "Hi Lena, thanks! I will review it with the team and get back to you by Friday." },
				],
				attachment: { name: "Q4-proposal.pdf", size: "218 KB" },
			},
			{
				id: "finance",
				group: "本周",
				from: "财务部",
				email: "finance@flyway.example",
				time: "周一",
				subject: "10 月报销单已审批通过",
				preview: "你提交的 10 月差旅报销单已审批通过，款项将于…",
				sent: "发给 e7@flyway.example · 周一 10:12",
				body: ["你好，", "你提交的 10 月差旅报销单已审批通过，款项将于 3 个工作日内打到你的工资卡。", "财务部"],
			},
			{
				id: "receipt",
				group: "更早",
				from: "Acme Cloud",
				email: "billing@acme.example",
				time: "10/02",
				subject: "Your receipt for October",
				preview: "Thanks for your payment. Amount: $12.00",
				sent: "发给 我 · 10 月 2 日",
				body: ["Thanks for your payment.", "Amount: $12.00 · Plan: Starter · Period: October 2026"],
			},
		],
	},
	en: {
		label: "Interactive Godwit demo",
		hint: "Try it: switch messages, summarize, translate, pick a quick reply, or ask Wit.",
		mail: "Mail",
		wit: "Wit",
		compose: "Compose",
		allInboxes: "All inboxes",
		starred: "Starred",
		accounts: [{ name: "hi@godwit.email", unread: 2 }, { name: "e7@flyway.example" }],
		folders: ["Inbox", "Drafts", "Sent", "Archive", "Trash"],
		addAccount: "Add account",
		settings: "Settings",
		inboxTitle: "All inboxes",
		unreadCount: "unread",
		all: "All",
		unread: "Unread",
		synced: "Synced · just now",
		summarize: "Summarize",
		translate: "Translate",
		moreSkill: "More skills",
		summaryTitle: "Summary",
		summaryMeta: "summarize · your model",
		translating: "Translating…",
		translated: "Translated to English · translate · your model",
		showOriginal: "Show original",
		quickTitle: "Quick replies · pick one, then edit it in compose",
		quickSent: "In the compose window, ready for your edits.",
		fromPlugin: "from plugin",
		witTitle: "Ask Wit",
		witSub: "It reads the mail you have synced, and asks you before anything is sent.",
		witPlaceholder: "Ask Wit anything about your mail…",
		witThinking: "Wit is reading your mail…",
		witAsks: [
			{
				q: "Which emails need a reply this week?",
				a: [
					"Two are waiting for you:",
					"1. Lena Fischer's Q4 partnership proposal: she would like your answer on the timeline by Friday.",
					"2. Air China's check-in reminder: CA4518 leaves Pudong T2 on Saturday at 14:05, and check-in is open.",
				],
				open: "lena",
				openLabel: "Open Lena's email",
			},
			{
				q: "Draft a reply to Lena saying November works",
				a: ["The draft is in a compose window, waiting for you to read it before it goes out:", "“Hi Lena, thanks for the proposal. November works for us, happy to move ahead.”"],
			},
		],
		selected: "lena",
		messages: [
			{
				id: "otp",
				group: "Today",
				from: "Code Host",
				email: "noreply@codehost.example",
				time: "09:41",
				subject: "[Code Host] Your sign-in code",
				preview: "Someone is signing in to your account. Your code is 482913, valid for 10 minutes.",
				unread: true,
				sent: "To me · Today 09:41",
				body: ["Someone is signing in to your Code Host account.", "Your code is 482913. It is valid for 10 minutes.", "If this wasn't you, you can ignore this email."],
				card: { kind: "otp", meta: "Sign-in code · from plugin otp-card", code: "482913", hint: "Valid for 10 minutes", action: "Copy", done: "Copied" },
			},
			{
				id: "flight",
				group: "Today",
				from: "Air China",
				email: "checkin@airchina.example",
				time: "08:15",
				subject: "Check-in open: CA4518 Shanghai → Chengdu",
				preview: "Your flight leaves on Sat, Oct 17 at 14:05. Online check-in is now open.",
				unread: true,
				sent: "To me · Today 08:15",
				body: ["Dear passenger,", "Your flight CA4518 (Shanghai Pudong T2 → Chengdu Shuangliu T2) leaves on Saturday, October 17 at 14:05 and arrives at 17:20. Online check-in is now open.", "Have a pleasant trip."],
				card: { kind: "flight", meta: "Flight CA4518 · Sat Oct 17 · 14:05 → 17:20 · from plugin flight-cards", from: "PVG", to: "CTU", hint: "Pudong T2 → Shuangliu T2 · Airbus A321 · Seat 32A", action: "Check in" },
				blocked: { text: "3 remote images blocked, so the sender can't tell whether you opened this email", load: "Load images", trust: "Always trust this sender" },
				quick: [
					{ kind: "Confirm", text: "Thanks, I'm checked in." },
					{ kind: "Rebook", text: "Hi, I'd like to move my flight to the next day. How do I do that?" },
				],
			},
			{
				id: "lena",
				group: "Yesterday",
				from: "Lena Fischer",
				email: "lena@partner.example",
				time: "Yesterday",
				subject: "Q4-Partnerschaftsvorschlag",
				preview: "Hallo, im Anschluss an unser Gespräch letzte Woche habe ich…",
				sent: "To me · Yesterday 21:03",
				body: [
					"Hallo,",
					"im Anschluss an unser Gespräch letzte Woche habe ich einen Entwurf für den Q4-Partnerschaftsvorschlag zusammengestellt. Die Eckpunkte sind ein gemeinsamer Launch im November und ein gemeinsames Webinar.",
					"Könnten Sie bis Freitag einen Blick darauf werfen und mir sagen, ob der Zeitplan für Ihr Team passt?",
					"Viele Grüße, Lena",
				],
				translated: [
					"Hi,",
					"Following up on our call last week, I put together a draft of the Q4 partnership proposal. The highlights are a joint launch in November and a co-hosted webinar.",
					"Could you take a look by Friday and let me know whether the timeline works for your team?",
					"Best, Lena",
				],
				summary: ["Lena sent a draft Q4 partnership proposal (PDF attached).", "Highlights: a joint launch in November and a co-hosted webinar.", "She needs your answer on the timeline by Friday."],
				quick: [
					{ kind: "Agree", text: "Hi Lena, thanks for the proposal. November works for us, happy to move ahead." },
					{ kind: "Agree, adjust", text: "Hi Lena, November works, but could we move the webinar one week later?" },
					{ kind: "Later", text: "Hi Lena, thanks! I will review it with the team and get back to you by Friday." },
				],
				attachment: { name: "Q4-proposal.pdf", size: "218 KB" },
			},
			{
				id: "finance",
				group: "This week",
				from: "Finance",
				email: "finance@flyway.example",
				time: "Mon",
				subject: "October expense report approved",
				preview: "Your October travel expense report was approved. The payment…",
				sent: "To e7@flyway.example · Mon 10:12",
				body: ["Hi,", "Your October travel expense report was approved. The payment reaches your account within 3 business days.", "Finance"],
			},
			{
				id: "receipt",
				group: "Earlier",
				from: "Acme Cloud",
				email: "billing@acme.example",
				time: "10/02",
				subject: "Your receipt for October",
				preview: "Thanks for your payment. Amount: $12.00",
				sent: "To me · Oct 2",
				body: ["Thanks for your payment.", "Amount: $12.00 · Plan: Starter · Period: October 2026"],
			},
		],
	},
};
