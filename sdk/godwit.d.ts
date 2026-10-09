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

export interface Ctx {
  on(event: "message.received", handler: (message: ReceivedMessage, info: DeliveryInfo) => MaybePromise<void>): void
  on(event: "message.beforeSend", handler: (draft: OutgoingDraft) => MaybePromise<SendCheck | void>): void

  actions: { register(action: Action): void }
  cards: { register(card: CardProvider): void }
  tools: { register(tool: Tool): void }

  /** the user's configuration, secrets included; read-only */
  readonly config: Readonly<Record<string, string | number | boolean | undefined>>
  /** needs mail:read; setFlags, move, createDraft and updateDraft need mail:write */
  mail: Mail
  /** needs llm: true; runs on the user's chosen model service */
  llm: { complete(prompt: string): Promise<string> }
  /** https only, to hosts declared in permissions.net */
  net: { fetch(url: string, init?: FetchInit): Promise<FetchResponse> }
  /** this plugin's own key-value store */
  storage: {
    get(key: string): Promise<string | undefined>
    set(key: string, value: string): Promise<void>
    delete(key: string): Promise<void>
  }
}
