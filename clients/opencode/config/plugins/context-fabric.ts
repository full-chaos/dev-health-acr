import { spawn } from "node:child_process"
import { tool, type Plugin } from "@opencode-ai/plugin"

const STATUS_TIMEOUT_MS = 5_000
const MAX_STATUS_OUTPUT_BYTES = 64 * 1024
const CLEANUP_TIMEOUT_MS = 1_000

type StatusOutcome =
  | { readonly kind: "completed"; readonly output: string }
  | { readonly kind: "timed_out" }
  | { readonly kind: "output_overflow" }
  | { readonly kind: "aborted" }
  | { readonly kind: "spawn_failed" }
  | { readonly kind: "non_zero_exit" }

const plugin: Plugin = async () => ({
  tool: {
    context_fabric_status: tool({
      description: "Run the bounded offline ACR Context Fabric status check.",
      args: {},
      execute: async (_args, context) => ({
        output: renderStatus(await runOfflineDoctor(context.directory, context.abort)),
      }),
    }),
  },
})

function renderStatus(outcome: StatusOutcome): string {
  switch (outcome.kind) {
    case "completed":
      return outcome.output
    case "timed_out":
      return "context_fabric_status failed: timed out after 5 seconds"
    case "output_overflow":
      return "context_fabric_status failed: combined output exceeded 64 KiB"
    case "aborted":
      return "context_fabric_status failed: cancelled"
    case "spawn_failed":
      return "context_fabric_status failed: acr-mcp doctor could not start"
    case "non_zero_exit":
      return "context_fabric_status failed: acr-mcp doctor returned a non-zero status"
  }
}

async function runOfflineDoctor(directory: string, abort: AbortSignal): Promise<StatusOutcome> {
  if (abort.aborted) return { kind: "aborted" }
  const child = spawn("acr-mcp", ["doctor", "--offline"], {
    cwd: directory,
    detached: process.platform !== "win32",
    stdio: ["ignore", "pipe", "pipe"],
  })
  const output: Buffer[] = []
  let outputBytes = 0
  let outcome: StatusOutcome | undefined
  let exitCode: number | null = null
  let termination: Promise<boolean> | undefined
  let escalation: ReturnType<typeof setTimeout> | undefined
  let escalationFailed = false
  const stop = (next: StatusOutcome): void => {
    if (outcome !== undefined) return
    outcome = next
    termination = terminate(child, (timer) => { escalation = timer }, () => { escalationFailed = true })
  }
  const append = (chunk: Buffer): void => {
    if (outcome !== undefined) return
    outputBytes += chunk.length
    if (outputBytes > MAX_STATUS_OUTPUT_BYTES) {
      stop({ kind: "output_overflow" })
      return
    }
    output.push(chunk)
  }
  const timeout = setTimeout(() => stop({ kind: "timed_out" }), STATUS_TIMEOUT_MS)
  const onAbort = (): void => stop({ kind: "aborted" })

  const stdout = child.stdout
  const stderr = child.stderr
  if (stdout === null || stderr === null) {
    stop({ kind: "spawn_failed" })
  } else {
    stdout.on("data", append)
    stderr.on("data", append)
  }
  child.once("error", () => stop({ kind: "spawn_failed" }))
  const reaped = new Promise<void>((resolve) => {
    child.once("close", (code) => {
      exitCode = code
      resolve()
    })
  })
  abort.addEventListener("abort", onAbort, { once: true })
  if (abort.aborted) onAbort()

  try {
    const closed = await waitForClose(reaped)
    if (escalation !== undefined) clearTimeout(escalation)
    if (!closed || escalationFailed || (termination !== undefined && !(await termination))) return { kind: "spawn_failed" }
  } finally {
    clearTimeout(timeout)
    abort.removeEventListener("abort", onAbort)
  }

  if (outcome !== undefined) return outcome
  if (exitCode !== 0) return { kind: "non_zero_exit" }
  return { kind: "completed", output: Buffer.concat(output).toString("utf8") }
}

function waitForClose(reaped: Promise<void>): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), STATUS_TIMEOUT_MS + CLEANUP_TIMEOUT_MS)
    reaped.then(() => { clearTimeout(timer); resolve(true) })
  })
}

async function terminate(child: ReturnType<typeof spawn>, setEscalation: (timer: ReturnType<typeof setTimeout>) => void, reportEscalationFailure: () => void): Promise<boolean> {
  if (child.pid === undefined || child.killed) return true
  if (process.platform === "win32") {
    return terminateWindowsTree(child.pid)
  }
  try {
    process.kill(-child.pid, "SIGTERM")
    const timer = setTimeout(() => {
      if (child.pid === undefined) return
      try { process.kill(-child.pid, "SIGKILL") } catch (error) {
        if (!(error instanceof Error) || !error.message.includes("ESRCH")) reportEscalationFailure()
      }
    }, 100)
    timer.unref()
    setEscalation(timer)
    return true
  } catch (error) {
    if (error instanceof Error) {
      child.kill(signal)
      return true
    }
    throw error
  }
}

function terminateWindowsTree(pid: number): Promise<boolean> {
  const cleanup = spawn("taskkill.exe", ["/pid", String(pid), "/t", "/f"], {
    stdio: "ignore",
    windowsHide: true,
  })
  return new Promise((resolve) => {
    const timer = setTimeout(() => { cleanup.kill("SIGKILL"); resolve(false) }, CLEANUP_TIMEOUT_MS)
    cleanup.once("error", () => { clearTimeout(timer); resolve(false) })
    cleanup.once("close", (code) => { clearTimeout(timer); resolve(code === 0) })
  })
}

export default plugin
