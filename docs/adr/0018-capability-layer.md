# 插件只在能力层统一，宿主 API 只给 JS 插件

脚本插件和原生插件通过同一个**能力层**接入应用：manifest 里声明 `capabilities`（如 `translate`、`models`），请求、响应和错误码对两种运行时完全相同，各有一个适配器（`jsTranslator`、`nativeTranslator` 等）。设置页据此列出渠道，不需要先加载插件。安装、授权、启停、配置、卸载也是同一张 `plugins` 表和同一组 Service 方法。

`ctx.mail` / `ctx.net` / `ctx.llm` / `ctx.storage` 这些宿主 API **只给 JS 插件**。它们存在的意义是让沙箱里的代码在受控范围内办事；原生代码本来就能直接读文件、联网，给它这些 API 不增加任何约束，只扩大 ABI 面。真有需要（比如原生插件要调用户选的模型）时再加宿主回调，子进程协议是双向的，到时只是多一种消息。

## Consequences

模型翻译不塞进 `translator` 接口：它是流式的，插件是一次性返回。分流点就是 `runSkill` 里的一个判断，「翻译方式」下拉框对用户是统一的。
