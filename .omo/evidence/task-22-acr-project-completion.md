# Task 22 deterministic evaluation demo

Command: `bash scripts/e2e/demo.sh --repeat 2 --out .omo/evidence/task-22-acr-project-completion.json`

- Result: `complete`; both repeats have identical task input hashes and packet
  IDs.
- Tasks: two complete fixture tasks and the controlled empty-evidence task.
- Traceable fixture packet IDs: `pkt_73d6c67e4ae1f4ce9fe7f0d8`,
  `pkt_a187cce94a802f7f0d888954`, and `pkt_44206692e5dff296a39b4e96`.
- Context metrics: factual-error rate `0/4`, citation precision `4/4`,
  irrelevant-item rate `0/4`, plan latency `7` logical steps, and token cost
  `314` estimated context tokens. File/test recall is not applicable because
  the public v1 corpus has no file or test identifier ground truth.
- The result is fixture-only and makes no live-agent outcome claim.
