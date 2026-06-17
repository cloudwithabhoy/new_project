# Jenkins seed job — generate all 13 pipelines from `.ci/jobs.groovy`

This creates one **Pipeline** job per microservice automatically, instead of clicking through the UI
13 times. You run the seed once; it generates/updates all jobs from [`jobs.groovy`](./jobs.groovy).

## One-time setup

1. **Install the plugin:** Manage Jenkins → Plugins → Available → **Job DSL** → install.

2. **Create the seed job:**
   - New Item → name `seed` → **Freestyle project** → OK.
   - **Source Code Management → Git:**
     - Repository URL: `https://github.com/cloudwithabhoy/new_project.git`
     - Credentials: `git-cred`
     - Branch: `*/main`
     - *(This makes the repo — including `.ci/jobs.groovy` — available in the workspace.)*
   - **Build Steps → Add build step → "Process Job DSLs":**
     - Choose **"Look on Filesystem"**
     - **DSL Scripts:** `.ci/jobs.groovy`
   - **Save.**

3. **Run it:** open `seed` → **Build Now**.
   - First run usually **fails with "script not approved"** — that's expected.
   - Go to **Manage Jenkins → In-process Script Approval** → **Approve** the pending Job DSL script.
   - **Build Now** again → it generates all 13 pipeline jobs.

## Result

Thirteen Pipeline jobs appear on the dashboard — `api-gateway`, `auth`, `user`, `catalog`, `search`,
`cart`, `order`, `payment`, `inventory`, `notification`, `recommendation`, `frontend`,
`thumbnail-job` — each wired to `services/<svc>/Jenkinsfile` on `main`, triggered by GitHub push.

## Day-to-day

- **Add a service:** add its name to the `services` list in `jobs.groovy`, commit, re-run `seed`.
- **Change the branch/trigger for all jobs:** edit `jobs.groovy` once, re-run `seed`.
- **First build of each generated job:** click **Build Now** once (the Jenkinsfile's
  `triggeredBy 'UserIdCause'` lets it run even with no file changes) so it pushes the first image.

> Tip: don't trigger all 13 at once on a small (1 GB) Jenkins box — the 3 executors + low RAM will
> thrash. Build a few at a time, or size up to a t3.small/medium.
