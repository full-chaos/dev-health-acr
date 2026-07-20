import { spawn } from "node:child_process"
import type { DoctorProcessDependencies } from "./doctor-process.ts"

const CLEANUP_TIMEOUT_MS = 1_000
const POSIX_ESCALATION_DELAY_MS = 100

type ChildProcess = ReturnType<typeof spawn>

export function closesWithin(reaped: Promise<void>, timeoutMs: number): Promise<boolean> {
  return new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), timeoutMs)
    reaped.then(() => {
      clearTimeout(timer)
      resolve(true)
    })
  })
}

export async function terminateDoctor(
  child: ChildProcess,
  dependencies: DoctorProcessDependencies,
  setEscalation: (timer: ReturnType<typeof setTimeout>) => void,
): Promise<boolean> {
  if (child.pid === undefined || child.killed) return true
  if (dependencies.platform === "win32") return terminateWindowsTree(child, dependencies.spawnProcess)

  try {
    process.kill(-child.pid, "SIGTERM")
  } catch (error) {
    if (!(error instanceof Error)) throw error
    return terminateProcess(child)
  }

  const timer = setTimeout(() => {
    if (child.pid === undefined) return
    try {
      process.kill(-child.pid, "SIGKILL")
    } catch (error) {
      if (!(error instanceof Error)) throw error
      terminateProcess(child, true)
    }
  }, POSIX_ESCALATION_DELAY_MS)
  timer.unref()
  setEscalation(timer)
  return true
}

async function terminateWindowsTree(child: ChildProcess, spawnProcess: typeof spawn): Promise<boolean> {
  if (child.pid === undefined) return true
  const cleanup = spawnProcess("taskkill.exe", ["/pid", String(child.pid), "/t", "/f"], {
    stdio: "ignore",
    windowsHide: true,
  })
  let cleanupExitCode: number | null = null
  let cleanupSpawnFailed = false
  const cleanupReaped = new Promise<void>((resolve) => {
    cleanup.once("close", (code) => {
      cleanupExitCode = code
      resolve()
    })
  })
  cleanup.once("error", () => {
    cleanupSpawnFailed = true
  })
  const cleanupSucceeded = await closesWithin(cleanupReaped, CLEANUP_TIMEOUT_MS)
    && !cleanupSpawnFailed
    && cleanupExitCode === 0
  if (cleanupSucceeded) return true

  await reapEmergencyCleanup(cleanup, cleanupReaped)
  await reapEmergencyDoctor(child)
  return false
}

async function reapEmergencyCleanup(cleanup: ChildProcess, reaped: Promise<void>): Promise<void> {
  terminateProcess(cleanup, true)
  await closesWithin(reaped, CLEANUP_TIMEOUT_MS)
}

async function reapEmergencyDoctor(child: ChildProcess): Promise<void> {
  const reaped = new Promise<void>((resolve) => {
    child.once("close", () => resolve())
  })
  terminateProcess(child, true)
  await closesWithin(reaped, CLEANUP_TIMEOUT_MS)
}

function terminateProcess(child: ChildProcess, force = false): boolean {
  const signal = force ? "SIGKILL" : "SIGTERM"
  try {
    return child.kill(signal)
  } catch (error) {
    if (error instanceof Error) return false
    throw error
  }
}
