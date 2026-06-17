# Lesson 02 — Installing & Running Jenkins

> Goal: get a **real Jenkins running on your machine**, log in, and install the handful of
> plugins we need — so the next lessons have something to click. Theory first (just enough),
> then a full step-by-step.

Everything for this lesson is in this one file. Do the **"Do this"** steps as you read.

---

## Part A — Theory (just enough to install wisely)

### A.1 Ways to run Jenkins

| Method | What it is | Good for |
|---|---|---|
| **Docker container** | Run the official `jenkins/jenkins` image | ✅ **What we'll use** — clean, disposable, no Java/OS mess |
| Native package | Install via `apt`/`yum`/`brew` onto the OS | Long-lived servers |
| WAR file | `java -jar jenkins.war` | Quick throwaway tests |
| Kubernetes (Helm) | Run Jenkins *in* a cluster | Production / advanced |

We use **Docker** because:
- No polluting your machine with Java + Jenkins system files.
- Start/stop/delete cleanly — perfect for learning (break it, throw it away, restart).
- It mirrors how you'll run things in this project (containers everywhere).

> You're on **WSL2 (Ubuntu)**. We'll run Jenkins via Docker inside WSL. You need **Docker**
> working in your WSL environment (Docker Desktop with WSL integration, or Docker Engine
> installed directly in WSL). We confirm that in Step 0.

### A.2 Two important concepts for the install

1. **Persistence (volumes).** A container is ephemeral — delete it and everything inside is gone.
   Jenkins stores all its config, jobs, and history in a directory called **`JENKINS_HOME`**
   (`/var/jenkins_home` inside the container). We attach a **Docker volume** to that path so your
   Jenkins survives container restarts. Forget this and you lose all your work on every restart.

2. **The Docker-in-Jenkins problem (preview).** Our pipelines will need to run `docker build`.
   But Jenkins itself is *inside* a container — can a container run Docker? Yes, with extra setup
   (mounting the Docker socket or "Docker-in-Docker"). We **don't** solve that now — Lesson 05
   handles it. For Lessons 02–04 we only need Jenkins itself running.

### A.3 What the first-run looks like (so nothing surprises you)

1. Start the container → Jenkins boots (takes ~30–60s the first time).
2. Jenkins prints a **one-time admin password** to the container log (and to a file inside it).
3. You open `http://localhost:8080`, paste that password to **unlock** Jenkins.
4. You choose **"Install suggested plugins"** → it downloads a sane default set (~2–4 min).
5. You **create your admin user**.
6. You land on the Jenkins **dashboard**. Done.

That's the whole first-run. Now let's do it.

---

## Part B — Hands-on, step by step

### Step 0 — Confirm Docker works (in WSL)

Open your WSL terminal and run:

```bash
docker --version
docker run --rm hello-world
```

- First command should print a Docker version.
- Second should download a tiny image and print **"Hello from Docker!"**.

**If these fail:** Docker isn't set up in WSL yet.
- Easiest path: install **Docker Desktop** on Windows, then enable **Settings → Resources → WSL
  Integration** for your Ubuntu distro. Reopen the terminal and retry.
- Don't continue until `docker run --rm hello-world` works.

> ✅ Checkpoint: you can run a container.

---

### Step 1 — Create a persistent volume for Jenkins

This is the directory that survives restarts (see A.2).

```bash
docker volume create jenkins_home
```

Verify:

```bash
docker volume ls | grep jenkins_home
```

> ✅ Checkpoint: a volume named `jenkins_home` exists.

---

### Step 2 — Start the Jenkins container

Run this single command:

```bash
docker run -d \
  --name jenkins \
  -p 8080:8080 \
  -p 50000:50000 \
  -v jenkins_home:/var/jenkins_home \
  --restart unless-stopped \
  jenkins/jenkins:lts
```

What each part means (read this — it's good Docker practice too):

| Flag | Meaning |
|---|---|
| `-d` | Detached — run in the background |
| `--name jenkins` | Name the container so you can refer to it |
| `-p 8080:8080` | Map the web UI port to your machine (`localhost:8080`) |
| `-p 50000:50000` | Port for **agents** to connect to the controller (needed later) |
| `-v jenkins_home:/var/jenkins_home` | Attach our persistent volume to JENKINS_HOME |
| `--restart unless-stopped` | Auto-restart the container on reboot/crash |
| `jenkins/jenkins:lts` | The official image, **LTS** = Long-Term Support (stable). Prefer LTS over `latest`. |

Check it's running:

```bash
docker ps
```

You should see the `jenkins` container with status `Up`.

> ✅ Checkpoint: `docker ps` shows Jenkins running.

---

### Step 3 — Get the one-time unlock password

Jenkins prints an initial admin password on first boot. Grab it from the container:

```bash
docker exec jenkins cat /var/jenkins_home/secrets/initialAdminPassword
```

Copy the long alphanumeric string it prints.

> If it says the file doesn't exist yet, Jenkins is still booting — wait ~30s and retry.
> You can also watch boot progress with: `docker logs -f jenkins` (Ctrl-C to stop watching).

> ✅ Checkpoint: you have the unlock password copied.

---

### Step 4 — Unlock Jenkins in the browser

1. Open **http://localhost:8080** in your browser.
2. You'll see **"Unlock Jenkins"**. Paste the password from Step 3. Click **Continue**.

> If the page won't load: give it another 30–60s (first boot is slow), then refresh.

> ✅ Checkpoint: you're past the unlock screen.

---

### Step 5 — Install suggested plugins

1. You'll see **"Customize Jenkins"** with two options.
2. Click **"Install suggested plugins"** (the left box).
3. Wait while it installs the default set (Git, Pipeline, etc.) — ~2–4 minutes.

> Why "suggested" and not "select plugins"? The suggested set already includes Git and Pipeline —
> everything we need to start. We'll add ECR/Docker-specific plugins later, on demand.

> ✅ Checkpoint: all plugins show green checkmarks; it advances automatically.

---

### Step 6 — Create your admin user

Fill the form (don't skip this — don't keep using the temp password):

- **Username:** something you'll remember (e.g. `admin`)
- **Password:** a real password
- **Full name / Email:** your choice

Click **Save and Continue** → **Save and Finish** → **Start using Jenkins**.

> ✅ Checkpoint: you see the Jenkins **dashboard** ("Welcome to Jenkins!").

---

### Step 7 — Confirm the install & poke around

On the dashboard:
- Left menu → **Manage Jenkins** → this is your control center for the next lessons.
- **Manage Jenkins → System Information** — confirms it's alive and shows versions.
- **Manage Jenkins → Plugins** — where you'll add ECR/Docker plugins in Lesson 05.

> ✅ Checkpoint: you can navigate the dashboard and open **Manage Jenkins**.

---

## Part C — Operating your Jenkins (daily commands)

Keep these handy:

```bash
# Stop Jenkins (config is safe — it's in the volume)
docker stop jenkins

# Start it again
docker start jenkins

# Watch logs (e.g. to see the unlock password or debug boot)
docker logs -f jenkins

# Open a shell INSIDE the Jenkins container (advanced/debugging)
docker exec -it jenkins bash

# Completely remove the container (config SURVIVES in jenkins_home volume)
docker rm -f jenkins
# ...then re-run the Step 2 command to recreate it — your jobs are still there.

# DANGER: delete config too (start fresh). Only if you want a clean slate.
docker rm -f jenkins && docker volume rm jenkins_home
```

> Key insight: because config lives in the **`jenkins_home` volume**, you can delete and recreate
> the *container* freely without losing your jobs. You only lose data if you delete the *volume*.

---

## Part D — Troubleshooting

| Symptom | Cause / Fix |
|---|---|
| `localhost:8080` won't load | Still booting — wait 60s, refresh. Check `docker logs jenkins`. |
| Password file "not found" | Booted too fast to read it — wait, retry Step 3. |
| Port 8080 already in use | Something else uses 8080. Change the mapping to `-p 8081:8080` and use `localhost:8081`. |
| `docker: command not found` | Docker not installed/integrated in WSL — revisit Step 0. |
| Plugins fail to download | Network/proxy issue — retry; check internet from WSL (`curl https://google.com`). |
| Lost everything after restart | You ran without `-v jenkins_home:/var/jenkins_home`. Always include the volume. |

---

## ✅ Check yourself

1. Why do we attach a **volume** to `/var/jenkins_home`?
2. What's the difference between deleting the **container** vs deleting the **volume**?
3. Where did the **initial admin password** come from?
4. What does the **LTS** tag mean, and why prefer it?
5. Why didn't we solve "Docker inside Jenkins" yet?

---

## 🛠️ Do this (before Lesson 03)

- [ ] Complete Steps 0–7 — get to the Jenkins dashboard with your own admin user.
- [ ] Run `docker stop jenkins` then `docker start jenkins`, reload the UI, and confirm your
      login still works — proving persistence works.
- [ ] Open **Manage Jenkins → Plugins → Installed** and just *look* at what "suggested plugins"
      gave you (notice **Git** and **Pipeline** are there).

> Next → **Lesson 03 — Your First Job** (a freestyle job so you recognize the old style, then
> your first real **Pipeline** that prints "Hello World" and runs a shell step). Say
> **"next jenkins lesson"** when your dashboard is up and I'll write it.
