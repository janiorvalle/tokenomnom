# HANDOFF — Night run: finish PR 10, then chain PRs 11 → 12 → 13, then STOP

You are Codex. Janior is asleep; Claude (reviewer) is offline until
morning. This steering document overrides ONE thing in the per-PR
handoffs — the base-branch/prerequisite rule — and adds autonomous-run
rules. Everything else in each handoff applies unchanged.

## The plan

Work through these stages IN ORDER. Each stage = implement per its
handoff, get CI green, open the PR, then move on. **Merge nothing,
ever.** Reviews happen in the morning.

1. **Finish PR 10** (`handoffs/HANDOFF-PR10.md`) — your in-progress work
   on branch `pr10-heatmap`. First: rebase the branch onto latest
   `origin/main` (it moved: commit `962f3cd` added handoffs + the
   `assets/why-*.png` files that PR 13 needs). Then finish, verify, open
   the PR against `main`.
2. **PR 11** (`handoffs/HANDOFF-PR11.md`) — branch `pr11-tui` created
   FROM `pr10-heatmap` (not main; PR 10 is unmerged). Open the PR with
   `--base pr10-heatmap` so it shows only its own diff.
3. **PR 12** (`handoffs/HANDOFF-PR12.md`) — branch `pr12-skill` from
   `pr11-tui`, PR base `pr11-tui`.
4. **PR 13** (`handoffs/HANDOFF-PR13.md`) — branch `pr13-ship` from
   `pr12-skill`, PR base `pr12-skill`.
5. **STOP.** No further work, no extra PRs, no cleanup sweeps, no
   opportunistic refactors.

The per-handoff line "Prerequisite: PRs 1–N merged to main" is satisfied
for tonight by "the previous stage's branch exists, its PR is open, and
its CI is green" — the chain substitutes for merged main. When Janior
merges in order tomorrow, GitHub retargets each stacked PR automatically.

## Autonomous-run rules

- **Gate between stages**: do not start stage N+1 until stage N's PR is
  open and its GitHub CI checks are green. If CI fails, fix it on that
  stage's branch first.
- **Blocked means stop, not improvise.** If a stage cannot be completed
  faithfully to its handoff (missing prerequisite, irreconcilable
  conflict, repeated CI failure you cannot diagnose), STOP THE CHAIN.
  Leave the branch pushed, open the PR as draft if partially done, and
  write what happened (see status reporting). Do not redesign around the
  blockage, do not skip a stage, do not touch the next stage.
- **Never touch `main`.** No direct pushes, no merges. All work on the
  stage branches.
- **Scope discipline doubles overnight**: no one is watching, so the
  handoffs' out-of-scope lists are hard walls. Deviations you would
  normally list in the PR body still go in the PR body — and anything
  you were tempted to do but did not, note it there too.
- Keep the existing worktree conventions: conventional commits, gofmt,
  `make verify` + `go test -race ./...` locally before opening each PR,
  walkthrough HTML files stay untracked.
- The real `~/.codex` / `~/.claude` / state dirs are read-only for you
  except where a handoff's acceptance criteria explicitly say otherwise
  (PR 12's real-machine skill install is the REVIEWER's gate, not yours
  — use temp dirs in tests; do NOT install the skill into the real agent
  dirs tonight).

## Status reporting

Maintain `NIGHT-RUN-STATUS.md` at the repo root (UNTRACKED — do not
commit it) and update it after every stage transition:

- per stage: started / PR opened (#N, CI state) / blocked (why, exactly
  where, what you tried)
- end of run: one summary block — stages completed, PRs opened with
  numbers, anything the morning review should look at first.

Every PR body additionally starts with `Stacked on #<previous>` (except
PR 10) so the review order is obvious.

## Morning handout (what done looks like)

Four open PRs — #10 `pr10-heatmap` → main, #11 `pr11-tui` →
`pr10-heatmap`, #12 `pr12-skill` → `pr11-tui`, #13 `pr13-ship` →
`pr12-skill` — all CI-green, deviations listed, NIGHT-RUN-STATUS.md
telling the story, and nothing merged. If the run stopped early, the
status file says exactly where and why, and later stages simply do not
exist rather than existing half-faithful.
