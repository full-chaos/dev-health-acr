import { spawn } from "node:child_process"
import { closesWithin, terminateDoctor } from "./doctor-process-cleanup.ts"

const STATUS_TIMEOUT_MS = 5_000
const MAX_STATUS_OUTPUT_BYTES = 64 * 1024
const CLEANUP_TIMEOUT_MS = 1_000

export type OfflineDoctorOutcome =
  | { readonly kind: "completed"; readonly output: string }
  | { readonly kind: "timed_out" }
  | { readonly kind: "output_overflow" }
  | { readonly kind: "aborted" }
  | { readonly kind: "spawn_failed" }
  | { readonly kind: "non_zero_exit" }
  | { readonly kind: "cleanup_failed" }

export type DoctorProcessDependencies = {
  readonly platform: string
  readonly spawnProcess: typeof spawn
}

const productionDependencies: DoctorProcessDependencies = {
  platform: process.platform,
  spawnProcess: spawn,
}

export function renderOfflineDoctorStatus(outcome: OfflineDoctorOutcome): string {
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
    case "cleanup_failed":
      return "context_fabric_status failed: acr-mcp doctor cleanup could not be completed"
  }
}

export function runOfflineDoctor(directory: string, abort: AbortSignal): Promise<OfflineDoctorOutcome> {
  return runOfflineDoctorWithDependencies(directory, abort, productionDependencies)
}

export function runOfflineDoctorForTest(
  directory: string,
  abort: AbortSignal,
  dependencies: DoctorProcessDependencies,
): Promise<OfflineDoctorOutcome> {
  return runOfflineDoctorWithDependencies(directory, abort, dependencies)
}

async function runOfflineDoctorWithDependencies(
  directory: string,
  abort: AbortSignal,
  dependencies: DoctorProcessDependencies,
): Promise<OfflineDoctorOutcome> {
  if (abort.aborted) return { kind: "aborted" }

  const child = dependencies.spawnProcess("acr-mcp", ["doctor", "--offline"], {
    cwd: directory,
    detached: dependencies.platform !== "win32",
    stdio: ["ignore", "pipe", "pipe"],
  })
  const output: Buffer[] = []
  let outputBytes = 0
  let outcome: OfflineDoctorOutcome | undefined
  let exitCode: number | null = null
  let cleanup: Promise<boolean> | undefined
  let escalation: ReturnType<typeof setTimeout> | undefined
  let resolveStopped: (() => void) | undefined
  const stopped = new Promise<void>((resolve) => {
    resolveStopped = resolve
  })
  const reaped = new Promise<void>((resolve) => {
    child.once("close", (code) => {
      exitCode = code
      resolve()
    })
  })
  const stop = (next: OfflineDoctorOutcome): void => {
    if (outcome !== undefined) return
    outcome = next
    cleanup = terminateDoctor(child, dependencies, (timer) => {
      escalation = timer
    })
    resolveStopped?.()
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
  const onAbort = (): void => stop({ kind: "aborted" })
  const timeout = setTimeout(() => stop({ kind: "timed_out" }), STATUS_TIMEOUT_MS)
  const stdout = child.stdout
  const stderr = child.stderr

  if (stdout === null || stderr === null) {
    stop({ kind: "spawn_failed" })
  } else {
    stdout.on("data", append)
    stderr.on("data", append)
  }
  child.once("error", () => stop({ kind: "spawn_failed" }))
  abort.addEventListener("abort", onAbort, { once: true })
  if (abort.aborted) onAbort()

  try {
    await Promise.race([reaped, stopped])
    if (outcome !== undefined) {
      const cleanupFinished = await cleanup
      const childClosed = await closesWithin(reaped, CLEANUP_TIMEOUT_MS)
      if (!cleanupFinished || !childClosed) return { kind: "cleanup_failed" }
      return outcome
    }
  } finally {
    clearTimeout(timeout)
    if (escalation !== undefined) clearTimeout(escalation)
    abort.removeEventListener("abort", onAbort)
  }

  if (exitCode !== 0) return { kind: "non_zero_exit" }
  return { kind: "completed", output: Buffer.concat(output).toString("utf8") }
}
