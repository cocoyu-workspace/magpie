import test from "node:test"
import assert from "node:assert/strict"
import { _internal } from "./index.mjs"

const { buildKiro, thinking, additionalFields, events } = _internal

function makeFrame(headers, payloadObj) {
  const payload = Buffer.from(JSON.stringify(payloadObj), "utf8")
  const headerChunks = []
  for (const [k, v] of Object.entries(headers)) {
    const kb = Buffer.from(k, "utf8")
    const vb = Buffer.from(v, "utf8")
    const h = Buffer.alloc(1 + kb.length + 1 + 2 + vb.length)
    h[0] = kb.length
    kb.copy(h, 1)
    h[1 + kb.length] = 7 // string header value type
    h.writeUInt16BE(vb.length, 2 + kb.length)
    vb.copy(h, 4 + kb.length)
    headerChunks.push(h)
  }
  const headersBuf = Buffer.concat(headerChunks)
  const totalLen = 12 + headersBuf.length + payload.length + 4
  const out = Buffer.alloc(totalLen)
  out.writeUInt32BE(totalLen, 0)
  out.writeUInt32BE(headersBuf.length, 4)
  headersBuf.copy(out, 12)
  payload.copy(out, 12 + headersBuf.length)
  return out
}

function streamFromFrames(frameBuffers) {
  return new ReadableStream({
    start(ctl) {
      for (const b of frameBuffers) ctl.enqueue(b)
      ctl.close()
    },
  })
}

test("claude-sonnet-4.5 keeps XML thinking tags and omits additionalModelRequestFields", () => {
  const req = {
    model: "claude-sonnet-4.5",
    system: "You are helpful.",
    messages: [{ role: "user", content: "hi" }],
  }
  const budget = thinking(req, req.model)
  assert.equal(budget, 50000)
  const body = JSON.parse(buildKiro(req, req.model, "", budget))
  assert.equal(body.additionalModelRequestFields, undefined)
  assert.match(
    body.conversationState.currentMessage.userInputMessage.content,
    /^<thinking_mode>enabled<\/thinking_mode><max_thinking_length>50000<\/max_thinking_length>/
  )
})

test("claude-opus-5.5 and claude-haiku-5.5 default requests keep XML thinking tags and server default effort", () => {
  for (const model of ["claude-opus-5.5", "claude-sonnet-5.5", "claude-haiku-5.5", "claude-opus-5"]) {
    const req = {
      model,
      system: "You are helpful.",
      messages: [{ role: "user", content: "hi" }],
    }
    const budget = thinking(req, model)
    assert.equal(budget, 50000)
    const body = JSON.parse(buildKiro(req, model, "", budget))
    assert.equal(body.additionalModelRequestFields, undefined)
    assert.match(
      body.conversationState.currentMessage.userInputMessage.content,
      /^<thinking_mode>enabled<\/thinking_mode><max_thinking_length>50000<\/max_thinking_length>/
    )
  }
})

test("explicit effort or budget_tokens populates additionalModelRequestFields on 4.6~5.5 models and clamps xhigh on 4.6", () => {
  const opusReq = {
    model: "claude-opus-5.5",
    output_config: { effort: "xhigh" },
    messages: [{ role: "user", content: "hi" }],
  }
  const opusBody = JSON.parse(buildKiro(opusReq, opusReq.model, "", thinking(opusReq, opusReq.model)))
  assert.deepEqual(opusBody.additionalModelRequestFields, {
    thinking: { type: "adaptive" },
    output_config: { effort: "xhigh" },
  })

  const sonnet46Req = {
    model: "claude-sonnet-4.6",
    output_config: { effort: "xhigh" },
    messages: [{ role: "user", content: "hi" }],
  }
  const sonnet46Body = JSON.parse(buildKiro(sonnet46Req, sonnet46Req.model, "", thinking(sonnet46Req, sonnet46Req.model)))
  assert.deepEqual(sonnet46Body.additionalModelRequestFields, {
    thinking: { type: "adaptive" },
    output_config: { effort: "max" },
  })

  const budgetReq = {
    model: "claude-haiku-5.5",
    thinking: { type: "enabled", budget_tokens: 10000 },
    messages: [{ role: "user", content: "hi" }],
  }
  const budgetBody = JSON.parse(buildKiro(budgetReq, budgetReq.model, "", thinking(budgetReq, budgetReq.model)))
  assert.deepEqual(budgetBody.additionalModelRequestFields, {
    thinking: { type: "adaptive" },
    output_config: { effort: "low" },
  })
})

test("disabled thinking sends thinking.type=disabled on 4.6~5/haiku-5.5 and falls back to adaptive+low on opus-5.5/sonnet-5.5", () => {
  for (const model of ["claude-haiku-5.5", "claude-opus-5", "claude-sonnet-5", "claude-opus-4.8", "claude-opus-4.7", "claude-sonnet-4.6"]) {
    const req = {
      model,
      thinking: { type: "disabled" },
      messages: [{ role: "user", content: "hi" }],
    }
    const budget = thinking(req, model)
    assert.equal(budget, 0)
    const body = JSON.parse(buildKiro(req, model, "", budget))
    assert.deepEqual(body.additionalModelRequestFields, {
      thinking: { type: "disabled" },
    })
  }

  for (const model of ["claude-opus-5.5", "claude-sonnet-5.5"]) {
    const req = {
      model,
      thinking: { type: "disabled" },
      messages: [{ role: "user", content: "hi" }],
    }
    const budget = thinking(req, model)
    assert.equal(budget, 0)
    const body = JSON.parse(buildKiro(req, model, "", budget))
    assert.deepEqual(body.additionalModelRequestFields, {
      thinking: { type: "adaptive" },
      output_config: { effort: "low" },
    })
  }

  assert.equal(typeof additionalFields, "function")
})

test("events suppresses reasoningContentEvent when budget is 0 (thinking disabled)", async () => {
  const frames = [
    makeFrame({ ":message-type": "event", ":event-type": "reasoningContentEvent" }, { text: "secret thought" }),
    makeFrame({ ":message-type": "event", ":event-type": "reasoningContentEvent" }, { signature: "sig-123" }),
    makeFrame({ ":message-type": "event", ":event-type": "assistantResponseEvent" }, { content: "final answer" }),
  ]

  const disabledOut = []
  for await (const e of events(streamFromFrames(frames), "claude-opus-5.5", 0)) {
    disabledOut.push(e)
  }
  assert.deepEqual(
    disabledOut.map((e) => e.kind),
    ["text", "stop"]
  )

  const enabledOut = []
  for await (const e of events(streamFromFrames(frames), "claude-opus-5.5", 50000)) {
    enabledOut.push(e)
  }
  assert.deepEqual(
    enabledOut.map((e) => e.kind),
    ["think", "sig", "text", "stop"]
  )
})
