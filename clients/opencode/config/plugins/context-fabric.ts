import { tool, type Plugin } from "@opencode-ai/plugin"
import { renderOfflineDoctorStatus, runOfflineDoctor } from "../lib/doctor-process.ts"

const plugin: Plugin = async () => ({
  tool: {
    context_fabric_status: tool({
      description: "Run the bounded offline ACR Context Fabric status check.",
      args: {},
      execute: async (_args, context) => ({
        output: renderOfflineDoctorStatus(await runOfflineDoctor(context.directory, context.abort)),
      }),
    }),
  },
})

export default plugin
