# inbox-zero 调研：可借鉴什么，不该学什么

调研于 2026-10-24，对象是 [elie222/inbox-zero](https://github.com/elie222/inbox-zero) 的 `109e216`（2026-10-23）。方法：按 7 个维度读源码（规则引擎、回复追踪与草稿、助手与安全、收件箱清理、客户端与同步、LLM 基础设施与评测、产品与工程实践），每个维度由第二人对照源码和 Godwit 的 ADR 复核，再做一轮跨维度查漏。下文的 Godwit 现状都在代码里核对过。文件路径不带前缀的是 inbox-zero 的路径，带 `core/` 等前缀的是 Godwit 的。

最后一节与另一份独立调研（codex，2026-10-17，`a281a64`）做了对比。

## 进展

这份报告按第 4 节的条目推进，每条落地或立项后在这里记一笔，表格里的条目原文不改。

| 条目 | 状态 | 链接 |
|---|---|---|
| 3 清洗给模型的正文 | 已完成 | #209 |
| 2 发件箱三件事 | 已完成 | #211、#227 |
| 4 自动标签离线 eval | 评测已就绪，待用真实 key 和用例跑出数据 | #217、#224 |
| 1 一键退订 | 已完成 | #218、#225 |
| 11 Wit 工具循环收尾、摘要按数据回灌 | 已完成 | #219、#223 |
| 12 AI 结果缓存保留期 | 已完成 | #220、#222 |
| 6 判断模型的输入与成本 | 兜底改走 SmallModel 已完成；labelState 的输入（To/Cc、附件名）待评测数据 | — |
| 9 发件人与订阅视图 | 已完成：统计、退订、全部归档（可撤销，跳过标星和回复过的线程）、屏蔽 | 本 PR |

**接下来的顺序**：第 1、2、3、11、12 条已完成，第 4 条的评测已就绪。下一步先用真实 key 和自己的用例跑一次 #217 的评测，拿到阈值和各判断方式的基线；第 6、7、8 条（判断输入、负向软提示、「试一下」）都要靠这组数据比较前后。第 9 条「发件人与订阅」视图可以直接开工，#225 已经开始抓 List-* 头。第 5 条回复状态价值最大，但要先写一条补充 ADR 0014 的新 ADR。其余条目按表格优先级排队。

**第 3 条落地时修正的认识**（#209 的评审）：
- 「模型读什么」要对齐阅读器，而不是对齐原始 HTML：阅读器显示的是 `Sanitize` 之后的 HTML，`mso-hide`、`visibility`、`hidden` 属性都会被剥掉，所以这些不算隐藏；`font-size:0` 挡不住自己设了字号的子元素，`max-height:0` 不配合 `overflow` 也不隐藏。`hidden()` 是给验证码识别用的宽松判断，不能直接拿来删模型正文。
- 有 HTML 时只读 HTML，纯文本部分不再给模型，因为没人看得到它。代价是只有图片的 HTML 邮件，模型读不到旁边的纯文本。
- `HTMLToText` 原来会把同一行的单元格拼在一起（「12」「34」变「1234」），已修复。
- 剩下的已知缺口：邮件 `<style>` 里按 class 隐藏的内容、`font` 简写设的零字号，代码里记了 `shortcut:`。

## 1. 它是什么

- **产品**：两部分，可以分开用。一是在 Gmail/Outlook 上后台运行的 AI 助手：规则分类、预写回复、Reply Zero（待我回复 / 等对方回复）、批量退订、冷邮件拦截、会前简报、附件归档、Slack/Telegram 聊天控制。二是 Superhuman 式的邮件客户端：Web 端加 Electron 桌面端。
- **架构**：云端 SaaS。Next.js、Prisma/Postgres、Redis、QStash/BullMQ。邮箱只走 Gmail API 和 Microsoft Graph，`EmailProvider` 只有这两种实现，不支持 IMAP。
- **许可**：AGPLv3，外加两条附加限制：不得商业变现；5 人以上组织需要企业许可。`apps/web/ee` 另有商业许可，贡献者签 CLA。**代码不能并入 Apache-2.0 的 Godwit，只能借鉴思路。**
- **演进**：它从「规则助手」转向邮件客户端。最近 50 个提交里有 23 个是 `mail:`；10 月 16 日起邮件应用免费，AI 单独收费。它新抽出了 `packages/mail-core` 和 `packages/mail-sqlite`（本地 SQLite、操作队列、乐观更新），但桌面端要设 `INBOX_ZERO_LOCAL_MAIL=1` 才用本地渲染（`apps/desktop/src/desktop.ts:147`），默认仍加载远程网站。同步和 OAuth 令牌都在服务器上。它说的「本地」是客户端缓存，和 Godwit 直连 IMAP 的本地优先不是一回事。

## 2. 关键机制

**规则引擎**（`apps/web/utils/ai/choose-rule/match-rules.ts`）。规则 = Rule 行里内联的条件 + 带类型的 Action 行。条件分四类：自然语言 instructions、静态 from/to/subject/body（glob）、指向 Group 的 learned pattern、日历 preset。新邮件经 Pub/Sub 推送到达，按 history 游标增量拉取，经三层去重（事件 uniqBy、Redis 锁、ExecutedRule 是否已存在），然后进入逐级降本的级联：

1. 冷邮件单独判定；
2. 线程门控；
3. learned pattern：命中包含项就清空这封邮件的全部 AI 候选；
4. 静态条件与 AI 条件按 AND/OR 组合；
5. 可选的 decision model（带概率的判断 API）：选中项概率低于 0.3 时退回 LLM；
6. LLM 选规则。

所有结果写进 ExecutedRule/ExecutedAction，`selectionMetadata` 记录每条规则为什么被跳过。注意：「每封邮件只调一次 AI」不成立，冷邮件、选规则、会话状态、动作参数、草稿各自都会调用模型。

**Reply Zero**（`apps/web/utils/reply-tracker`）。入站时先判断是不是真人往来，再把整条线程交给模型，在 TO_REPLY / AWAITING_REPLY / FYI / ACTIONED 里四选一。用户每发一封信，用 `userSentLastEmail=true` 重判一次，这时去掉 FYI 选项。跟进提醒是定时任务，按工作日计算，用 messageId + sentAt 保证只提醒一次。状态同时落在服务商标签、ThreadTracker、DraftSendLog 三处，一致性靠 Redis 锁兜底。

**草稿**（`apps/web/utils/ai/reply`）。最多并行收集 13 路上下文：历史检索 agent、日历 agent、知识库、记忆、MCP 等，一次起草至少 3 次模型调用。模型在同一次输出里自评置信度。草稿放在服务商草稿箱，所以「发没发出」只能推断（LIKELY_SENT）。用户实际发出的文本与 AI 原稿的相似度低于 0.95 时，抽取 FACT / PREFERENCE / PROCEDURE 三类带作用域的记忆；攒满 10 条 PREFERENCE 后压缩成写作风格。学到的内容只有一个只读的 debug 页能看到。

**助手**（`apps/web/utils/ai/assistant/chat.ts`）。单 agent 工具循环，22 到 31 个工具。时间预算用尽后（Web 端 720 秒，聊天集成 60 秒）关掉工具，让模型基于已有结果收尾。代码里强制确认的只有发信卡片、deleteRule、记忆来源校验、规则读后写校验四处。其余写操作（移到垃圾箱、按发件人批量归档、退订、updateRule）都以「用户的请求即确认」直接执行。

**收件箱清理**：

- **退订**依次尝试：one-click POST → GET 页面后由 AI 判断是否已退订 → 提交简单表单 → 可选的 gVisor 沙箱浏览器 worker。
- **冷邮件**先过一串确定性守卫：白名单 → 同组织 → learned pattern → 判断是否有过往来（判断不了时当作有往来），守卫都通过才问 LLM。
- **发件人统计**用一条 SQL 聚合。退订建议的条件：至少 3 封、阅读率低于 20%、有退订链接。
- **Split Inbox** 让 AI 把用户的描述转成确定性过滤条件，运行时不调用模型（`apps/web/utils/ai/split`）。

**LLM 基础设施**（`apps/web/utils/llms`）：

- 五个模型角色，映射到 44 个 use case。
- 必填的 `PromptHardening` 参数（标为 untrusted 时仍可选 `level:none`）。
- 可变内容放到 user prompt，让 system 前缀能命中缓存。
- 用量优先采用 provider 报告的成本。
- 评测分两层：`ai-regression` 是硬断言；`__tests__/eval` 是多模型矩阵，带 either 用例（两种答案都算对、不计分）、新旧 prompt 对比和结果缓存。
- 本地开发配了 LLM 模拟器，返回满足 schema 的最小答案，不需要任何 key。

## 3. 贯穿各处的取舍

1. **先跑确定性判断，模型兜底，拿不准时偏向不动用户的邮件。** 判断不了有没有往来，就当作有往来；冷邮件的 decision model 路径默认关闭，因为误判会把真邮件归档。这套阈值和不对称的默认值可以直接沿用。
2. **软提示稳，硬规则贵。** 同样是用户纠正，作为提示放进 prompt 的 ClassificationFeedback 基本没出过问题。会短路 AI 的 learned pattern 却陆续补了 USER 优先、按完整地址边界匹配、跨规则占用检查、按来源撤销，冷邮件一次误学还要写迁移来清理。这正好印证了 ADR 0014 不做按发件人确定规则的选择。
3. **AI 只生成可审阅的确定性配置**（Split Inbox）。这是这次调研里最符合 Godwit 气质的 AI 用法。
4. **安全大量押在提示词上，而不是代码网关上。** 收件人和正文都写死的 FORWARD/REPLY 规则被判为 LOW、免确认（`apps/web/utils/risk.ts:118-121`，有单测固定这一行为），而且只有 createRule 一处做这个检查。`mcp:write` 下可以创建转发、发信、webhook 规则，和文档里「不发送邮件」的说法矛盾。这正是 Godwit 不该退让的地方（ADR 0003/0004）。
5. **默认开启、默认外发。** 外发邮件默认插打开追踪像素，跟进草稿默认开，写作风格在应用加载时自动分析，冷邮件默认动作会给对方发通知。与 ADR 0010 相反。
6. **学到的东西用户看不见、改不了。** Godwit 如果做任何学习，查看、编辑、删除要在第一版就有，用户的编辑优先于自动结果。

## 4. 对 Godwit 的借鉴（按优先级）

| # | 做什么 | 为什么 | 落点 | 工作量 |
|---|---|---|---|---|
| 1 | **一键退订（严格按 RFC 8058）**：同步时补抓 `List-Unsubscribe`、`List-Unsubscribe-Post`、`List-Id`；只有 Post 头精确等于 One-Click 时才发 POST，用不跟随重定向的专用客户端，2xx 才算成功；只有网页链接时走 `openLink` 用系统浏览器打开；mailto 生成草稿 | Godwit 完全没有退订能力。inbox-zero 不检查 Post 头就发 POST、跟随重定向时把 POST 改成 GET、2xx 就判成功（`apps/web/utils/senders/unsubscribe.ts:247-258`），误判很多 | `core/mailsync/fetch.go` `headerSection`（目前只抓 REFERENCES / IN-REPLY-TO / REPLY-TO）、`core/mail/parse.go`、`app/links.go`、`internal/imagefetch` | M |
| 2 | **发件箱三件事**：撤销发送（outbox 加 `not_before`，撤销即 `queued→cancelled` 的 CAS，草稿在进入 sending 时才删）；SMTP 5xx 直接判失败，4xx 退避重试；排队超过 N 天的邮件不再自动发出，交给用户决定 | 现在点了发送就收不回；永久错误也会白白重试 5 次（`core/outbox.go:266-269`，`maxSendAttempts = 5`）；长期离线后，几周前的邮件可能悄悄发出 | `core/compose.go` Send、`core/outbox.go`、`core/send`、主窗口 Toast | M |
| 3 | **清洗给模型的正文**：在 `modelBody` 出口跳过 `display:none` 和 `font-size:0` 的子树（复用 `core/mail/signinstruct.go` 的 `hidden()`），去掉 Cf 字符（复用 `core/mail/parse.go` 的 `invisible()`，它已经保留 ZWNJ/ZWJ），把 Unicode 空白归一后再跑注入检测 | sanitize 保留了 display 和 font-size，用户看不见的文字模型却能读到；`injectionHint` 用 RE2 的 `\s`，只匹配 ASCII 空白，插一个 U+200B 或 NBSP 就能绕过（`core/ai/untrusted.go:19`） | `core/maillinks.go`、`core/ai/untrusted.go` | S |
| 4 | **自动标签的离线 eval**：JSONL 用例从外部目录读取，用 `GODWIT_LIVE_*` 控制是否运行；带 either 用例，覆盖中、英、日；判断 API 与 GenJudge 并排比较；只对判断 API 扫描阈值；不用 LLM 裁判 | 现在只有 `core/ai/judge_live_test.go` 的冒烟题。阈值 0.8 合不合适、改了预置说明是好是坏，都没有数据。第 3、6、7 条都需要它来验证 | `core/labeler.go` labelQuestions/labelState、`core/settings.go` JudgeThreshold | M |
| 5 | **本地回复状态与跟进视图**：新增线程级 `thread_status` 表；确定性基线是「按 message_id 去重后，最新一封是不是本账户发的」；可选再用判断模型出一道 4 选 1 的 Choice 题精化（独立开关）；手动设置优先；在统一视图里加「待跟进」；催回草稿只在用户点击时生成 | Reply Zero 的核心价值，大部分判断在本地一条 SQL 就够。**需要先写一条新 ADR**，因为 ADR 0014 写了「不给已发送分类」 | `core/mailsync/thread.go`、`core/threads.go`、`core/ai/judge.go`、`docs/adr` | L |
| 6 | **判断模型的输入与成本**：labelState 用已有的 `modelHead`（含附件名）替换 `mailHeader`，并加上 To/Cc；GenJudge 兜底时改走 SmallModel，而不是对话用的 DefaultModel | bill、order、newsletter 这类标签最需要收件人和附件信号；现在每封新邮件都用默认大模型判断（`core/models.go:974-983`） | `core/labeler.go`、`core/models.go` resolveJudge | S |
| 7 | **手动去标签作为软提示，只回流负向信号**：统计同一发件人上被手动去掉某标签的次数，由宿主生成一句话附进 `Question.Instructions`，不带任何邮件文本 | 同一发件人的下一封还会被打错，用户只能一封封改。只用负向信号可以规避 From 伪造。不生成确定规则，不违反 ADR 0014，但建议在 ADR 0014 里补一句软提示的边界 | `core/labeler.go` labelQuestions、`core/labels.go` | M |
| 8 | **标签说明「试一下」**：对最近 n 封邮件预览打标结果，只读，必须和真实打标共用同一个出题函数和 labelState | 标签说明就是用户写的自然语言规则，现在只能保存后回扫才看得到效果。inbox-zero 的 isTest 有两处与真实执行分叉，这是反面教训 | `app/settings_labels.go`、`core/labeler.go` | M |
| 9 | **本地「发件人与订阅」视图**：一条 GROUP BY 统计总数、未读、最近日期、是否有 List-Unsubscribe；提供退订、归档全部、屏蔽三个操作；批量移动可撤销（记下原文件夹，撤销就是反向 Move） | 批量清理的入口；inbox-zero 的批量归档没有撤销 | `core/messages.go` Move、`core/mailsync/ops.go`、`app/mail_list.go` | M |
| 10 | **即时回复补线程上下文**：用 `mailTarget("thread")` 取整个线程；跳过 newsletter、notice、otp 类邮件；吸收其中与语言无关的规则：不复述来信、按顺序逐条回答、配了签名就不写落款。写作风格做成按账户、按钮触发、可编辑的文本 | 现在只把单封来信交给模型（`core/aiservice.go` 里 QuickReplies 用 `mailTarget("message")`）。**不要照搬**它「直接给出用户最可能的答案」那条规则，见第 6 节 | `core/aiservice.go`、`core/assist.go` | M |
| 11 | **Wit 工具循环的收尾**：设明确的 MaxTurns，捕获 `ErrMaxTurnsExceeded` 后发一次不带工具的请求，让模型基于已有结果作答；读过邮件的会话，压缩摘要改成带「这是数据，不是指令」说明的 user 块回灌 | Wit 请求没设 MaxTurns，Genkit 跑满默认 50 轮后直接报错，用户拿不到回答；摘要现在以 model 角色插回，等于把邮件内容洗到了 untrusted 标记之外 | `core/conversations.go`、`core/ai/history.go`、`core/compact.go` | S |
| 12 | **给 ai_cache 定保留期** | 全仓没有任何对 ai_cache 的清理，它也不关联 message_id，用户删掉的邮件，其总结和翻译会永久留在本地库里 | `core/store/schema.go:127`，参照 `core/hooks.go` 清理 events_log 的先例 | S |
| 13 | **日历邀请卡片与 RSVP 草稿**：照搬它对 iTIP 的拒绝条件；回复生成挂在原会话下的草稿，附上 `method=REPLY` 的 ics，由用户自己发送 | IMAP 用户现在只能看到一个 .ics 附件 | `core/mail/parse.go`、`core/compose.go` | M |
| 14 | **搜索索引**：改用外部内容表或 contentless FTS5，去掉正文的重复副本；CJK 逐字索引 | 现在是 trigram 索引，3 个字以下的中文查询（「发票」「合同」）会退回全表 LIKE（`core/messages.go:185-201`）；inbox-zero 的 mail-sqlite 用 contentless FTS5，CJK 逐字索引 | `core/store/schema.go` messages_fts | M |
| 15 | **两份文档防线**：在 ADR 0003 里补一句，凡是之后会无人参与、自动外发的持久状态（规则里的收件人、URL、插件配置里的目的地），创建和修改都按外发处理；在 AGENTS.md 里加一节面向模型接口的准则 | inbox-zero 的权限名和实际能力对不上，这正是被注入后最容易利用的缝隙 | `docs/adr/0003-*.md`、`AGENTS.md` | S |

另有一项优先级低的：导出不含邮件内容的诊断包（各类计数、状态和 pprof），复用 `core/check.go` 的 CheckReport。

## 5. 不该学的

- **自动执行 REPLY / SEND / FORWARD / NOTIFY_SENDER 的规则**，以及「用户的请求即确认」的写操作：违反 ADR 0003/0004。
- **会短路 AI 的 learned pattern / 按发件人的类别表**：违反 ADR 0014，inbox-zero 自己也为此打了大量补丁。
- **Pub/Sub、QStash、Redis 锁、cron 这套分布式基础设施**：Godwit 是单一拥有者进程加 SQLite（ADR 0002），events_log 加 labeled_at 已经覆盖。
- **AI 草稿写进服务器草稿箱、回复状态写成服务商标签**：Godwit 的草稿在本地，发送在同一个事务里进入 outbox，能精确知道结果；标签只存本地（ADR 0014）。
- **每封来信都跑多 agent 起草管线、默认开启的自动分析和跟进草稿**：违反 ADR 0010，对自带 key 和本地模型的用户成本太高。
- **退订用的沙箱浏览器 worker、打开追踪像素、远程图片代理、APNs 推送、Slack/Telegram 机器人、计费档位、组织功能**：都是云端 SaaS 的东西，与本地优先和 Godwit 的隐私立场相反（Godwit 会剔除追踪像素，见 `core/mail/tracker.go`）。
- **正则 DLP 改写发给模型的内容**：会破坏验证码识别（`core/signincodes.go`），模型又是用户自己的。
- **部署级的多 provider 回退链**：会把邮件悄悄发给用户没选过的服务。

Godwit 已有、而且做得不比它差的：发送至多一次加「待核实」（ADR 0006）、期望位置与服务器位置（ADR 0005）、统一网关加 holds_mail 来源标记（ADR 0004）、判断 API 适配（`core/ai/judge.go` 的校验比它更严格，还支持 score 类型）。

## 6. 与 codex 调研的对比

codex 在 `a281a64` 上做了一轮较轻的调研，给出五点借鉴：会话级回复状态、自然语言配置配合结构化执行、自动化试跑与解释、从用户对草稿的修改中学习、用业务边界案例做评测。另有两点不能照搬：草稿 prompt 会替用户猜答案；许可证限制。

**一致的部分**：回复状态是最有价值的产品能力（两边都排在前列）；试跑与可解释性；用真实的边界案例做 eval，codex 特别提到要补中文样本；许可证不允许直接复用代码；mail-core 只适合拿来交叉检查边界，不必重造。

**codex 纠正了我的一处**：草稿 prompt 确实要求模型「state their most likely answer plainly」，示例是在没有依据时直接回答「Yes, $500 and Friday are both right」（`apps/web/utils/ai/reply/draft-reply.ts:40-62`，同文件 143 行另有一处）。我初稿第 10 条曾建议吸收这条规则，现已删去。Godwit 的即时回复本来就给出最多三条不同的回复，覆盖几种可能的答案，比替用户猜一个更稳妥。

**我与 codex 的分歧**：

- **「learned pattern 命中就跳过 AI，省成本」**：codex 把它列为值得学的点。实际上它是 inbox-zero 补丁最多的地方，也与 ADR 0014 冲突。可取的只有软提示（第 4 节第 7 条）。
- **「不能简单理解成 Electron 网页壳，它已有本地邮件引擎」**：引擎确实存在，但在 codex 用的提交上同样要 `INBOX_ZERO_LOCAL_MAIL=1` 才启用。默认加载远程网站，同步和令牌都在服务器上。
- **isTest 试跑**：codex 当作正面例子，复核发现它在会话状态和草稿两处与真实执行分叉。所以 Godwit 的预览必须和真实打标共用代码路径。
- **codex 没覆盖的**：退订、发件箱可靠性、给模型的正文清洗、inbox-zero 安全模型的缺口（写死收件人的转发规则判为 LOW、`mcp:write` 能建转发规则、自动起草时研究子代理无人值守调用第三方读工具），以及这次顺带发现的 Godwit 自身问题（下一节）。
- **排序**：codex 是「回复状态 → 试跑 → 从修改中学习」。我的建议是先做成本低、确定性高、又是后续改动前提的几项：清洗（S）、eval、发件箱。回复状态价值最大，但工作量 L，而且要先定 ADR。

## 7. 顺带发现的 Godwit 问题

- **AutoLabel 默认开启**（`core/settings.go:97`），要等用户配好模型才会运行。ADR 0010 的措辞是「用户明确开启、且有独立开关」，两者有出入，需要澄清。这一点也意味着，往 labelState 里加任何内容（第 6 条），都等于默认多外发一样东西。
- `core/outbox.go`：永久错误也重试 5 次（第 2 条）。
- `core/ai/untrusted.go`：注入检测可被 Unicode 空白绕过（第 3 条，已有测试证实，#209 已修复）。
- Wit 没有设 MaxTurns（第 11 条）。
- `ai_cache` 没有保留期（第 12 条，#220 已修复：30 天未使用即清理）。
- 即时回复只看单封来信（第 10 条）。
