# Jenkins Learning Track

From zero to a working pipeline that builds our microservice images and pushes them to Amazon ECR.

**For this project, Jenkins does ONE job:** build a service's Docker image and push it to ECR.
Deployment stays manual (`kubectl apply`). Keep that scope in mind — we are not learning all of
Jenkins, just the slice we need, done well.

## Lessons

| # | Lesson | What you'll learn | Status |
|---|---|---|---|
| 01 | [Theory](./01-theory.md) | What CI/CD is, what Jenkins is, core concepts & vocabulary | 🟢 ready |
| 02 | [Installation](./02-installation.md) | Run Jenkins (Docker locally), first login, plugins | 🟢 ready |
| 03 | First Job | A freestyle job + your first Pipeline ("Hello World") | ⚪ planned |
| 04 | Jenkinsfile & Pipeline-as-Code | Declarative pipeline, stages, steps | ⚪ planned |
| 05 | Credentials & Agents | Storing secrets, build agents, Docker-in-CI | ⚪ planned |
| 06 | Build & Push to ECR | The real project pipeline for the `catalog` service | ⚪ planned |
| 07 | Multi-service Pipeline | One reusable pipeline for all 12 services | ⚪ planned |

## Golden rule

Read the theory, then **do the "Do this" section** at the end of each lesson on a real Jenkins
instance. Reading alone won't stick — Jenkins is a "learn by clicking and breaking" tool.

> Start → [`01-theory.md`](./01-theory.md)
