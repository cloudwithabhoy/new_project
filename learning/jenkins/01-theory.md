# Lesson 01 — Jenkins Theory

> Goal of this lesson: understand **what** Jenkins is, **why** it exists, and the **vocabulary**
> you'll see everywhere — before touching a single button. No installation yet. Just concepts.

---

## 1. The problem CI/CD solves

Imagine the workflow **without** any automation, for our `catalog` service:

1. You change the code.
2. You manually run tests on your laptop.
3. You manually run `docker build`.
4. You manually `docker push` to ECR.
5. You manually `kubectl apply`.

Problems:
- It's **manual and error-prone** — easy to forget a step or push an untested image.
- It only works **on your machine** ("works on my laptop").
- There's **no record** of what was built, when, by whom, or whether tests passed.
- It **doesn't scale** — now imagine doing steps 1–4 for **12 services**.

**CI (Continuous Integration)** automates steps 2–4: every time code changes, a server
automatically **builds and tests** it in a clean, consistent environment, and produces an
artifact (for us: a Docker image in ECR).

**CD (Continuous Delivery/Deployment)** would automate step 5 too. **We are deliberately NOT
doing CD** — you'll deploy manually with `kubectl` to learn Kubernetes by hand. So in this
project, **Jenkins = CI only**.

```
Code change ──► [ Jenkins ] ──► test ──► docker build ──► push to ECR
                                                              │
                                          (you, manually) ────┴──► kubectl apply
```

---

## 2. What is Jenkins?

**Jenkins is an open-source automation server.** In plain terms: a program that runs on a
server, watches for triggers (like "new code was pushed"), and then runs a sequence of steps
you defined (like "build this image"). That's it at its core — a very flexible "when X happens,
do Y, Z..." engine, built for software builds.

Key facts:
- Written in **Java**, runs as a web app — you interact with it through a **web UI** in your browser.
- **Extremely plugin-driven.** The core is small; almost everything (Git, Docker, AWS, Slack…)
  comes from **plugins**. This is its biggest strength and its biggest source of confusion.
- Very old and very widely used — tons of tutorials, but also tons of *outdated* tutorials
  (watch for "freestyle jobs" everywhere; modern Jenkins favors **pipelines** — more below).

> Mental model: Jenkins is a **robot that follows your recipe** whenever something triggers it.
> You spend your time writing good recipes (pipelines), not clicking buttons.

---

## 3. Core architecture: Controller & Agents

Two roles:

- **Controller** (older docs say "master"): the brain. Hosts the web UI, stores configuration,
  schedules work, shows results. You log into the controller.
- **Agent** (older docs say "slave"/"node"): a worker that actually *runs* the build steps.
  Agents can be other machines, containers, or cloud VMs.

```
        ┌───────────────┐        assigns work        ┌──────────────┐
 You ──►│  Controller   │ ─────────────────────────► │   Agent(s)   │
 (web)  │  (brain/UI)   │ ◄───────────────────────── │  (do builds) │
        └───────────────┘        reports results     └──────────────┘
```

**Why separate them?** So builds run in isolation and you can scale out (many agents = many
parallel builds). For learning, you'll start with the controller **also acting as an agent**
(everything on one machine) — that's fine to begin with.

For us later: building Docker images needs a place with Docker available — that's an agent
concern we'll handle in Lesson 05.

---

## 4. The vocabulary you must know

| Term | Plain meaning |
|---|---|
| **Job / Project** | A configured unit of work ("build the catalog service"). |
| **Build / Run** | One execution of a job. Each gets a number (#1, #2, …) and a log. |
| **Freestyle job** | The *old* way — configure a job by clicking through web forms. Simple but not version-controlled. We'll see it once, then move on. |
| **Pipeline** | The *modern* way — your build process defined **as code** in a file. This is what we'll actually use. |
| **Jenkinsfile** | The file (named exactly `Jenkinsfile`) that contains a Pipeline definition, stored **in your Git repo** next to the code. |
| **Stage** | A named phase of a pipeline (e.g. `Test`, `Build`, `Push`). Shows as a column in the UI. |
| **Step** | A single action inside a stage (e.g. run a shell command). |
| **Trigger** | What starts a build (a Git push, a schedule, a manual click). |
| **Plugin** | An add-on that gives Jenkins new abilities (Docker, Git, AWS ECR, etc.). |
| **Credential** | A securely-stored secret (AWS keys, passwords) that pipelines reference by ID, never in plaintext. |
| **Agent / Node** | A machine/container that runs the build (see §3). |
| **Workspace** | A directory on the agent where your repo is checked out and the build runs. |
| **Artifact** | The output of a build worth keeping. For us: a Docker image (stored in ECR, not Jenkins). |

---

## 5. Freestyle vs Pipeline (and why we choose Pipeline)

**Freestyle job** — you configure everything by clicking in the web UI:
- ✅ Beginner-friendly, visual.
- ❌ Config lives *inside Jenkins*, not in Git → not version-controlled, not reviewable, hard to
  reproduce, easy to lose.

**Pipeline (Pipeline-as-Code)** — your build is a script in a `Jenkinsfile` in your repo:
- ✅ **Version-controlled** alongside the code it builds.
- ✅ **Reviewable** in pull requests, reproducible, portable.
- ✅ Supports complex logic (parallel stages, conditionals, loops).
- ❌ Slightly steeper to learn (it's code).

> **For this project we use Pipelines + a `Jenkinsfile`.** It's the industry standard and it fits
> our monorepo perfectly — each service can have (or share) a Jenkinsfile. We'll *see* a freestyle
> job once in Lesson 03 just so you recognize it, then never look back.

---

## 6. Declarative vs Scripted Pipeline

There are two syntaxes for a Jenkinsfile. You'll see both online:

- **Declarative** — newer, structured, opinionated. Starts with `pipeline { ... }`. Easier to
  read, has built-in error handling. **This is what we'll use.**
- **Scripted** — older, full Groovy programming. Starts with `node { ... }`. More powerful but
  more rope to hang yourself with.

A tiny **declarative** example (don't run it yet — just read the shape):

```groovy
pipeline {
    agent any                         // run on any available agent
    stages {
        stage('Test') {               // first phase
            steps {
                sh 'echo "running tests..."'   // a shell step
            }
        }
        stage('Build') {              // second phase
            steps {
                sh 'echo "docker build..."'
            }
        }
    }
}
```

Read that top-to-bottom: a **pipeline** made of **stages** (`Test`, `Build`), each containing
**steps** (`sh` shell commands), running on an **agent**. That's 90% of Jenkins right there.

---

## 7. How a build actually flows (end to end)

1. **Trigger** fires (you push code, or click "Build Now").
2. Controller finds a free **agent** and assigns the job.
3. Agent creates a **workspace** and **checks out** your Git repo into it.
4. Agent reads the **Jenkinsfile** and runs each **stage → step** in order.
5. Each step's output streams to the **build log** (you watch it live in the UI).
6. If any step fails (non-zero exit), the build is marked **FAILED** and usually stops.
7. On success, results/artifacts are recorded. For us: the image now lives in **ECR**.

---

## 8. How this maps to OUR project

- One Jenkins pipeline per service (or one reusable pipeline parameterized by service name).
- Each pipeline's stages will be roughly: **Checkout → Lint/Test → Docker Build → Push to ECR**.
- Secrets (AWS credentials to push to ECR) live in Jenkins **Credentials**, referenced by ID.
- The `Jenkinsfile` lives in the repo (`services/<name>/Jenkinsfile` or a shared one).
- **No deploy stage** — that's your manual `kubectl apply` step, on purpose.

You'll build exactly this in Lesson 06, starting with the `catalog` service.

---

## 9. Common beginner confusions (clear these now)

- **"Master/slave" vs "Controller/agent"** — same thing, renamed. New docs say controller/agent.
- **"Why so many plugins?"** — Jenkins core is intentionally minimal; capabilities are opt-in.
  You only install what you need (Git, Docker, ECR, Pipeline).
- **Freestyle tutorials everywhere** — most old tutorials use freestyle jobs. Prefer Pipeline.
- **Groovy** — the language Jenkinsfiles are written in. You don't need to *learn Groovy*; you
  need a handful of patterns, which we'll cover by example.
- **Jenkins ≠ the thing that deploys** — it *can*, but in this project it stops at "push image".

---

## ✅ Check yourself

You should now be able to answer, in your own words:

1. What's the difference between **CI** and **CD**, and which is Jenkins doing for us?
2. What do the **controller** and **agent** each do?
3. Why do we prefer a **Pipeline + Jenkinsfile** over a **freestyle job**?
4. What are a **stage** and a **step**?
5. Where do **secrets** (like AWS keys) belong in Jenkins?

If any answer is fuzzy, re-skim that section — the hands-on lessons assume this vocabulary.

---

## 🛠️ Do this (before Lesson 02)

You don't install anything yet. Just:
- [ ] Re-read §4 (vocabulary) once — these words appear in every lesson.
- [ ] Look at the example Jenkinsfile in §6 and identify the agent, the stages, and the steps.
- [ ] Confirm you understand **why we have no deploy stage**.

> Next → **Lesson 02 — Installation** (run Jenkins locally in Docker, first login, install the
> handful of plugins we need). Say **"next jenkins lesson"** when you're ready and I'll write it.
