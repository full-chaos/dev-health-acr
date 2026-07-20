import { spawn } from "node:child_process"
import { readFile } from "node:fs/promises"
import { runOfflineDoctorForTest } from "./config/lib/doctor-process.ts"

const receiptPath = process.env.ACR_MCP_DOCTOR_RECEIPT
const expectedOutcome = process.env.ACR_MCP_EXPECTED_OUTCOME
const taskkillReceiptPath = process.env.ACR_MCP_TASKKILL_RECEIPT

if (receiptPath === undefined || expectedOutcome === undefined) {
  throw new Error("ACR_MCP_DOCTOR_RECEIPT and ACR_MCP_EXPECTED_OUTCOME are required")
}

const abort = new AbortController()
const execution = runOfflineDoctorForTest(process.cwd(), abort.signal, {
  platform: "win32",
  spawnProcess: spawn,
})
const doctorPid = await waitForReceipt(receiptPath)
abort.abort()
const outcome = await execution

if (outcome.kind !== expectedOutcome) {
  throw new Error(`expected ${expectedOutcome}, received ${outcome.kind}`)
}
await waitForExit(doctorPid)
if (taskkillReceiptPath !== undefined && taskkillReceiptPath !== "") {
  await waitForExit(await waitForReceipt(taskkillReceiptPath))
}
process.stdout.write(`DOCTOR_PROCESS_DRIVER_OK outcome=${outcome.kind}\n`)

async function waitForReceipt(path: string): Promise<number> {
  const deadline = Date.now() + 1_000
  while (Date.now() < deadline) {
    try {
      const content = await readFile(path, "utf8")
      if (!/^[1-9][0-9]*\n$/.test(content)) {
        throw new Error("doctor receipt is malformed before abort")
      }
      return Number(content.trim())
    } catch (error) {
      if (error instanceof Error && error.message === "doctor receipt is malformed before abort") {
        throw error
      }
      await delay(10)
    }
  }
  throw new Error("doctor receipt was not written before abort")
}

async function waitForExit(pid: number): Promise<void> {
  const deadline = Date.now() + 1_000
  while (Date.now() < deadline) {
    try {
      process.kill(pid, 0)
    } catch (error) {
      if (error instanceof Error && "code" in error && error.code === "ESRCH") return
      throw error
    }
    await delay(10)
  }
  throw new Error("doctor process remained alive after cancellation")
}

function delay(milliseconds: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, milliseconds))
}
