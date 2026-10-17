export default function (ctx: any) {
  ctx.models.register({
    listModels: () => ({ models: [
      { id: "m-1", name: "Demo", transport: "http", protocol: "openai-completions", baseURL: "https://models.godwit.test/v1" },
      { id: "judge", name: "Judge", transport: "plugin", operation: "judge" },
    ] }),
    judge: (call: any) => ({ answers: call.questions.map((q: any) => ({ name: q.name, type: "predicate", probability: 0.8 })) }),
  })
}
