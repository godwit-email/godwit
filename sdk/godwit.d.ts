// @godwit/sdk: types for Godwit plugins (spec 5.3). Types only; import them
// with `import type { Ctx } from "@godwit/sdk"`. The host provides no way to
// send mail.

/** A label: a plain string, or a default plus translations by language tag. */
export type Text = string | ({ default: string } & { [lang: string]: string })

export interface AttachmentMeta {
  name: string
  type: string
  size: number
}

export interface MailMessage {
  id: number
  subject: string
  from: string
  to: string[]
  cc: string[]
  /** RFC 3339 */
  date: string
  text: string
  html: string
  /** text or html was cut to the host's limit */
  truncated: boolean
  attachments: AttachmentMeta[]
}

export interface MailSummary {
  id: number
  subject: string
  from: string
  /** RFC 3339 */
  date: string
  snippet: string
}

export interface DraftInput {
  accountId: number
  to: string[]
  cc?: string[]
  subject: string
  html: string
}

export interface DraftUpdate {
  draftId: number
  /** the revision read; a draft changed since then is not updated */
  revision: number
  html: string
}

/** The new message a message.received handler gets. */
export interface ReceivedMessage {
  id: number
  accountId: number
  subject: string
  from: string
  /** RFC 3339 */
  date: string
  /** the message was deleted before delivery; id is still given */
  deleted: boolean
}

export interface DeliveryInfo {
  /** the same on every redelivery of this event */
  idempotencyKey: string
}

/** The draft a message.beforeSend handler checks. */
export interface OutgoingDraft {
  id: number
  accountId: number
  to: string[]
  cc: string[]
  subject: string
  html: string
}

/** warn: the user may still send. block: the user must edit the draft or disable the plugin. */
export type SendCheck = { warn: string } | { block: string }

type MaybePromise<T> = T | Promise<T>

export type ActionOn = "message" | "thread" | "selection" | "draft"

export interface ActionTarget {
  kind: ActionOn
  id: number
  /** the selected text, for "selection" */
  text?: string
}

export type ActionResult = string | { text?: string; html?: string; draftId?: number } | void

export interface Action {
  id: string
  label: Text
  on: ActionOn[]
  run(target: ActionTarget): MaybePromise<ActionResult>
}

export interface CardField {
  label: string
  value: string
}

export interface CardButton {
  label: string
  /** copy: value is copied; open-url: value is an http(s) URL; run-action: value is an action id of this plugin */
  kind: "copy" | "open-url" | "run-action"
  value: string
}

/** Declarative card data; the client renders it. */
export interface Card {
  title: string
  fields?: CardField[]
  buttons?: CardButton[]
}

export interface CardProvider {
  match(message: MailMessage): MaybePromise<boolean>
  render(message: MailMessage): MaybePromise<Card>
}

export interface Tool {
  /** ^[a-z][a-z0-9_]{0,40}$ */
  name: string
  description: string
  /** JSON Schema of the arguments */
  schema: Record<string, unknown>
  /** default true: every call is approved by the user first */
  write?: boolean
  run(args: Record<string, unknown>): MaybePromise<unknown>
}

export interface FetchInit {
  method?: string
  headers?: Record<string, string>
  body?: string
}

export interface FetchResponse {
  status: number
  headers: Record<string, string>
  text(): Promise<string>
}

export interface Mail {
  get(id: number): Promise<MailMessage>
  /** at most 50 results */
  search(query: string, limit?: number): Promise<MailSummary[]>
  setFlags(ids: number[], add: string[], remove: string[]): Promise<void>
  move(ids: number[], folderId: number): Promise<void>
  /** returns the new draft's id */
  createDraft(draft: DraftInput): Promise<number>
  /** returns the draft's new revision */
  updateDraft(update: DraftUpdate): Promise<number>
}

/**
 * a typed question for llm.decide, in the OpenAI Decisions API's shape whatever judge answers;
 * name matches ^[a-z][a-z0-9_]{0,40}$
 */
export type Question =
  | { name: string; type: "predicate"; instructions: string }
  /** 2 to 255 unique values */
  | { name: string; type: "choice"; instructions: string; choices: ChoiceOption[] }
  /** 2 to 10 levels, lowest first */
  | { name: string; type: "score"; instructions: string; levels: Level[] }

export interface ChoiceOption {
  value: string
  description?: string
}

export interface Level {
  label: string
  description?: string
}

/**
 * an answer in the Decisions API's shape: a predicate gives the probability it holds; a choice
 * its value, a score a 0-based (possibly fractional) level index, each with probabilities and
 * confidence when the judge gives them; a refusal nothing else
 */
export type Answer =
  | { type: "predicate"; name: string; probability: number }
  | { type: "choice"; name: string; choice: string; probabilities?: { value: string; probability: number }[]; confidence?: number }
  | { type: "score"; name: string; score: number; probabilities?: { value: number; label: string; probability: number }[]; confidence?: number }
  | { type: "refusal"; name: string }

/**
 * What a translator gets: the paragraphs of the mail (or selection) the user
 * translates, and the target language (BCP 47). source is "" (detect it).
 * The texts may be long: split them as your service requires.
 */
export interface TranslateRequest {
  texts: string[]
  source: string
  target: string
}

/** One translation per request text, in order, and the source language detected (BCP 47). */
export interface TranslateResult {
  texts: string[]
  source: string
}

/**
 * A translation channel the user can pick in Settings › General. The plugin
 * must declare "capabilities": ["translate"] in plugin.json and register in
 * its default function. It may fetch from every host in permissions.net
 * without asking each time; the user chose it and clicked Translate.
 */
export interface Translator {
  translate(req: TranslateRequest): MaybePromise<TranslateResult>
}

export interface Ctx {
  /** Register once during initialization; requires capabilities: ["models"]. */
  models: { register(provider: ModelProvider): void }
  on(event: "message.received", handler: (message: ReceivedMessage, info: DeliveryInfo) => MaybePromise<void>): void
  on(event: "message.beforeSend", handler: (draft: OutgoingDraft) => MaybePromise<SendCheck | void>): void

  actions: { register(action: Action): void }
  cards: { register(card: CardProvider): void }
  tools: { register(tool: Tool): void }
  /** needs "capabilities": ["translate"]; at most one */
  translators: { register(translator: Translator): void }

  /** the user's configuration, secrets included; read-only */
  readonly config: Readonly<Record<string, string | number | boolean | undefined>>
  /** needs mail:read; setFlags, move, createDraft and updateDraft need mail:write */
  mail: Mail
  /**
   * needs llm: true. complete runs on the user's default model service. decide runs on
   * the user's judge service, which may be another vendor's (else the default model).
   */
  llm: {
    complete(prompt: string): Promise<string>
    /** input and all question text at most 64 KB; at most 128 questions; at most 2 calls at once */
    decide(input: string, questions: Question[]): Promise<Answer[]>
  }
  /** https only, to hosts declared in permissions.net */
  net: { fetch(url: string, init?: FetchInit): Promise<FetchResponse> }
  /** this plugin's own key-value store */
  storage: {
    get(key: string): Promise<string | undefined>
    set(key: string, value: string): Promise<void>
    delete(key: string): Promise<void>
  }
}


export type GenerateProtocol =
  | "openai-completions"
  | "openai-responses"
  | "anthropic-message"

export type JudgeProtocol =
  | "typesafe-systemone"
  | "openai-decisions"

export type ModelProtocol = GenerateProtocol | JudgeProtocol

export interface ModelIdentity {
  /** 插件内唯一、稳定；更改显示名称不改变它。允许 org/model 形式。 */
  id: string
  name: string
}

export interface HTTPModel extends ModelIdentity {
  transport: "http"
  protocol: ModelProtocol
  baseURL: string
}

export interface PluginJudgeModel extends ModelIdentity {
  transport: "plugin"
  operation: "judge"
  /** registry 中的资产名 → 变体 ID；不是下载 URL。 */
  assets?: Record<string, string>
}

export type ProviderModel = HTTPModel | PluginJudgeModel

export interface ModelsResult {
  models: ProviderModel[]
}


export type JSONValue =
  | null | boolean | number | string
  | JSONValue[]
  | { [key: string]: JSONValue }

export interface ModelHTTPRequest {
  url: string
  headers: Record<string, string>
  /** 已按该模型的 HTTP 协议编码；实际发送方法固定为 POST。 */
  body: Record<string, JSONValue>
}

export interface PrepareModelRequest {
  model: HTTPModel
  /** 不包含业务数据的随机会话标识；工具续轮、审批恢复和重启后保持稳定。 */
  sessionId: string
  /** Unix 毫秒；和 sessionId 一起由宿主持久化。 */
  sessionCreatedAt: number
  /** 每次实际 HTTP 尝试唯一，重试会改变。 */
  requestId: string
  request: ModelHTTPRequest
}

export interface JudgeModelRequest {
  modelId: string
  input: string
  questions: Question[]
}

export interface JudgeModelResult {
  answers: Answer[]
}

export interface ModelProvider {
  listModels(): MaybePromise<ModelsResult>

  /** 可省略；省略就原样发送宿主编码的请求。 */
  prepareRequest?(
    request: PrepareModelRequest,
  ): MaybePromise<ModelHTTPRequest>

  /** 列表含 transport=plugin、operation=judge 的模型时必须实现。 */
  judge?(request: JudgeModelRequest): MaybePromise<JudgeModelResult>
}
