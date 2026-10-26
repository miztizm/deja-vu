import { chmodSync, mkdirSync, mkdtempSync, realpathSync, writeFileSync } from "node:fs"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { test } from "node:test"
import assert from "node:assert/strict"

// deja_recall, deja_fix and deja_blame ran deja from the plugin process's own
// directory, which is wherever the gateway was started, so recall ranked that
// project first for every session the gateway served (#5580). They are now
// tool factories: OpenClaw builds them per run with the run's context, and deja
// runs in that context's workspaceDir.
test(
  "the agent tools run deja in the session's workspace",
  { skip: process.platform === "win32" && "the stub deja is a shell script" },
  async () => {
    const dir = mkdtempSync(join(tmpdir(), "deja-openclaw-ws-"))
    const stub = join(dir, "deja")
    writeFileSync(stub, "#!/bin/sh\npwd -P\n")
    chmodSync(stub, 0o755)
    const work = join(dir, "work")
    mkdirSync(work)

    const prev = process.env.OPENCLAW_STATE_DIR
    process.env.OPENCLAW_STATE_DIR = dir
    const registered = []
    try {
      const { default: plugin } = await import("../index.mjs?" + Math.random())
      plugin.register({
        id: "deja-vu",
        pluginConfig: { bin: stub, autoRecall: false },
        on() {},
        logger: { warn() {} },
        registerTool: (tool, opts) => registered.push({ tool, opts }),
      })
    } finally {
      if (prev === undefined) delete process.env.OPENCLAW_STATE_DIR
      else process.env.OPENCLAW_STATE_DIR = prev
    }

    const params = { deja_recall: { query: "zorbit" }, deja_fix: { error: "boom" }, deja_blame: { path: "a.go" } }
    assert.deepEqual(registered.map((r) => r.opts && r.opts.name), Object.keys(params))
    for (const { tool, opts } of registered) {
      assert.equal(typeof tool, "function", `${opts.name} is not built per run`)
      const built = tool({ workspaceDir: work, sessionKey: "agent:main:main" })
      assert.equal(built.name, opts.name)
      const out = await built.execute("call-1", params[opts.name])
      assert.equal(out.content[0].text, realpathSync(work), `${opts.name} did not run in the workspace`)
      // A host that gives no workspace keeps the old behaviour.
      const bare = await tool(undefined).execute("call-2", params[opts.name])
      assert.equal(bare.content[0].text, realpathSync(process.cwd()), `${opts.name} without a workspace`)
    }
  },
)
