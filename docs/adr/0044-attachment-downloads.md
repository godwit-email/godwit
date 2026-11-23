---
status: proposed
---

# 附件下载：打开、保存、拖出都是一个下载，由界面进程排队

今天阅读区取附件有三个入口，各写各的：「打开」（`openAttachment`）、「保存」（`saveAttachment`）、多选后的「全部保存」（`saveAttachments`，带同名冲突对话框）。每个都是一次 `a.do` 加 `GetAttachment` 或 `FetchAttachments`：没有进度，不能取消，失败了只有一条 toast。#821 想让附件 chip 能拖到访达，又遇到第四种写法：拖拽开始的那一刻就要交出一个已经存在的文件（mygo 的 `FileData` 只传已有路径，`transfer.Lazy` 必须在主线程上立刻返回），所以它只能在打开邮件时就把每个附件下载下来再复制一份。

这四件事其实是同一件：把一组附件从服务器取到本机缓存，再写到某个地方。差别只在「某个地方」怎么定：临时目录、保存面板、文件夹选择器，或者用户松手的位置。所以这里引入一个概念：**下载**。

## 决定

- **一个下载 = 一组附件 + 一个去处。** 去处有四种：
  - **打开**：`tempDir` 里的新目录（`Close` 时删除），写完后 `OpenPath`。
  - **保存**：用户在保存面板里选的路径，写法同今天的 `copyFile`。
  - **文件夹**：「全部保存」选的文件夹加冲突选择（保留两者、替换、跳过），写法就是今天的 `writeAttachmentsToDir`，冲突对话框不变。
  - **拖放目标**：松手时才知道的去处（第二阶段，见下）。

  写文件的规则一条不改：按附件名（`core.FileName`）写，加「来自互联网」标记（`markFromInternet`），同名时走 `copyFileNew` / `replaceFile`。
- **下载器在界面进程里（`app`），后端只负责把附件取进缓存。** 按 [0038](0038-backend-process-owns-the-engine.md)，附件不过 wire，界面直接读数据目录里的缓存路径。用户选的保存位置、拖拽的落点，也都只有界面进程知道。所以下载器调用已有的 `FetchAttachmentsStream`（`ui` 角色），拿到缓存路径后由界面进程复制到去处。后端不新增状态，下载也不持久化：单个附件最大 25 MiB（`attachLimit`），界面重启后用户再点一次就行。
- **一条队列，依次执行，可以取消。**
  - 同一账户的取件本来就在账户锁上排队（`attachmentFetcher`），并行只会让用户刚点的那个排到后面。所以只有一条先进先出的队列，拖放目标的下载插到队首，因为目标应用正在等。
  - 同一附件、同一种去处已经在队列里时，不再重复加入（「打开」连点两次只打开一次）。
  - 每个下载有自己的 `context`：取消就是取消它的 `FetchAttachmentsStream`。`App.Close` 取消全部。
  - 出错的处理同今天：流在第一个错误处停止，下载以一条错误 toast 结束；成功的保存仍然是 `reader.attachments.saved`。
- **状态显示在附件 chip 上。** 排队中或下载中的 chip 显示转圈，悬停时可以取消；进度按文件推进（流每交出一个文件算一格）。单个文件不超过 25 MiB，所以先不做字节进度。以后需要时，给 `FetchAttachmentsStream` 加进度事件即可，0038 里已经有以 `*Stream` 形式带进度的先例。不做下载列表面板，也不做 Dock 进度条。
- **界面隐藏后先等下载完再退出。** 主窗口隐藏满 `hiddenExitAfter` 时如果还有下载在进行，就像还开着撰写窗口时一样（`mainCloseAction`），等它结束再退出。⌘Q 会取消排队中和正在取件的下载。
- **拖出分两个阶段。**
  - **第一阶段（不改 mygo）：只有已在缓存里的附件能拖。**
    - 新增一个 `ui` 角色专用的方法 `CachedAttachments(ids []int64) map[int64]string`：只读，不联网，返回其中已缓存附件的缓存路径。它不更新 `last_used`，因为显示一个 chip 不算使用。
    - 对已缓存的附件，在 UI 线程之外按原名放一份到临时目录，按附件 ID 记在 `App` 上，本次运行内复用。放这一份用 `clonefile`（`golang.org/x/sys/unix`，APFS 上写时复制，不额外占盘），别的文件系统退回 `copyFile`。之后 chip 就是 `DragData(transfer.FileData(path))` 拖拽源，只允许复制。
    - 没有缓存的 chip 不是拖拽源，悬停提示「打开或保存后可以拖出」。打开或保存一次之后附件就进了缓存，chip 随之变成可拖。
    - 打开邮件时不下载任何附件。
  - **第二阶段（mygo 加文件承诺）：没缓存的附件也能拖。** 拖拽开始时只声明文件名；目标来要内容时，下载器把一个去处为拖放目标的下载插到队首，内容取到后交给目标。
    - 提议的 mygo 接口：`transfer.PromisedFile(name string, open func(ctx context.Context) (io.ReadCloser, error))`。`open` 在 goroutine 上调用，可以阻塞到下载完成；目标放弃时取消 `ctx`。
    - 用流而不是「给一个目录」：macOS 的 `NSFilePromiseProvider` 给的是目标 URL，由 mygo 把流写进去；Windows 的 `CFSTR_FILEDESCRIPTORW` 加 `CFSTR_FILECONTENTS` 是由资源管理器去读的 `IStream`，根本不告诉来源目标在哪；Linux 的 XDS 给一个 URI。三者都能接的最小形状是流。
    - 不支持文件承诺的平台或目标，退回第一阶段的规则。
    - **待定**：通过承诺写出的文件，「来自互联网」标记由谁来加。macOS 上文件由 mygo 写，`app` 不知道路径。可以让 mygo 在 `DragOptions.Done` 里交回写出的路径，也可以在 Info.plist 里打开 `LSFileQuarantineEnabled`。落地前要定下来。
- **服务面**：第一阶段只新增 `CachedAttachments`，放进 `rpcbind` 的 `uiOnly`，钉在 `rpcbind/testdata/ui_methods.txt`。它返回的是数据目录里的文件路径，只对同机的界面有用；`full` 要附件已经有 `GetAttachment`。
- **外发**：没有。下载是从用户自己的邮件服务器取回自己的邮件，不发送任何数据；保存和拖出写的是用户选定的本地位置。Wit 读附件（`ReadAttachment`）不经过下载器，照旧。

## Considered Options

- **打开邮件就预先下载并复制（#821 的写法）**：每打开一封邮件都会联网下载它的全部附件，并在临时目录里多占一份磁盘，而好处只有拖拽一项。第一阶段用缓存加 clone，第二阶段用文件承诺，都不需要预先下载。
- **下载器放在后端**：好处是界面退出后下载还能继续。但去处（保存面板选的路径、拖放目标）在界面进程，放到后端就要跨进程交接路径，多一层；附件又小，不值得为跨重启设计。
- **直接拖缓存文件**：缓存文件按 ID 命名、没有扩展名；而且接收方可能改写它（比如拖进 Preview 改完再存），改动会直接写进缓存。
- **悬停或按下时开始下载**：是在猜用户的意图，仍然会下载用户并不需要的文件；大文件第一次拖时也未必准备好。
- **像浏览器那样的下载列表面板**：附件不超过 25 MiB，通常几秒就完成，chip 上的状态已经够用。以后支持更大的附件再考虑。
- **「自动下载附件」设置（全部 / 最近的 / 不下载，像 Apple Mail）**：这是流量和磁盘之间的产品取舍，不在本 ADR 的范围内。有了下载器以后，它可以作为一种低优先级、去处为「只进缓存」的下载加进来。

## Consequences

- 新增 `app/downloads.go`：队列、去重、取消、每个下载的状态；附件 chip 读这些状态。`openAttachment`、`saveAttachment`、`saveAttachments` 改为提交下载，冲突对话框和 toast 文案不变。
- #821 改成第一阶段：去掉打开邮件即下载，改用 `CachedAttachments` 加 clone，按附件 ID 复用。
- 新增一条界面文案（未缓存 chip 的悬停提示）。
- GLOSSARY 的「邮件」一节加一条 **下载**：一组附件加一个去处，由界面进程排队。
- 文件承诺作为 mygo 的功能需求单独提出（先做 macOS）。落地前，没有缓存的附件只能先打开或保存，之后才能拖出。
- 测试：队列去重、取消、出错停止；第一阶段只有已缓存的附件是拖拽源，且临时目录里的那一份能复用；`ui_methods.txt` 钉住 `CachedAttachments`。
