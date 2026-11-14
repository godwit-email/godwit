# @godwit/magpie-agent

Connects [Godwit](https://godwit.email) to [magpie](https://usemagpie.ai): Godwit's Wit can then use magpie's models, the MCP servers in magpie's library (memory, web search, browser and the tools of magpie's plugins), its skills and its instructions.

```sh
magpie plugin add @godwit/magpie-agent
```

Then turn Godwit on in magpie's Agents page and choose a model (or `magpie godwit <model>`). In magpie's Library, give Godwit the MCP servers, skills and instructions you want it to have: like every agent, it gets only those given to it.

## What it writes

magpie writes into `~/.godwit/external/`, and Godwit (of this package's version or later) mirrors it:

| File | In Godwit |
|---|---|
| `models.json` | A model service named `magpie`, with every model magpie gives Godwit, written as magpie describes them; Godwit takes their names and context windows (routing groups included). The model chosen in magpie is Godwit's default model while connected; Godwit reads it from the file and keeps your own default beneath it. |
| `mcp.json` | magpie's MCP servers. Each starts without a grant: Godwit asks you to grant it in Settings → Extensions before it runs. |
| `skills/` | magpie's skills (linked from magpie's library), beside your own. A skill of yours with the same name wins. |
| `AGENTS.md` | Your instructions from magpie's library, added to Wit's system prompt. |

Godwit shows each of them as managed by magpie: change them in magpie, not in Godwit. Requests to the model go through magpie's gateway on this computer, and Godwit masks them the same as for any model service.

## Disconnecting

Turn Godwit off in magpie (or `magpie godwit default`): magpie restores the file, Godwit removes the `magpie` model service, and your own default model is the default again. The library's MCP servers, skills and instructions follow magpie's Library, as for any agent: take them from Godwit there and Godwit removes them too.
