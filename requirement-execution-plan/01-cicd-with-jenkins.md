# Phase 01 — CI/CD with Jenkins (build + push to ECR)

## Goal
Every microservice gets a reproducible image built and pushed to ECR, tagged by git SHA — so the images
are ready before any cluster exists.

## Why this phase
Nothing can deploy until its image is built and stored somewhere durable, and every later phase pulls from
ECR. So this comes first.
- **Builds on:** the bootstrap runbook (IAM, EC2, Jenkins set up).
- **Unlocks:** Phase 02 (a cluster to deploy those images into).

## Scope
**In scope:** the ECR repo `ecom-repo`; Jenkins with one path-triggered pipeline per service
(`docker build → tag <svc>-<sha> → push to ECR`); a one-time run of all 13 services so every image lands
in ECR.
**Out of scope:** the EKS cluster and any deploy (Phase 02+); automated deploy — deploying stays manual via
`kubectl` throughout.

## What it needs to do
- An ECR repo `ecom-repo` holds every service's images.
- Jenkins runs one path-triggered pipeline per service, generated from the Job DSL seed
  ([`../.ci/jobs.groovy`](../.ci/jobs.groovy)).
- Each pipeline runs `lint · test · build`, then `docker build → tag <svc>-<sha> → push to ECR`.
- All 13 services are built and pushed at least once, so every image is in ECR.
- Every image is tagged by its git SHA (`<svc>-<sha>`), so you can trace it back to the commit.
- The pipeline is re-runnable and works the same for any service.
- Pushes use the AWS credential configured in Jenkins — no long-lived keys in source.
- CI only produces images; it never runs `kubectl`. Deploy stays manual.

## Architecture

```
   git push ─► ┌──────────┐ ─► lint · test · build ─► ┌──────────────────────────────┐
               │ Jenkins  │                            │ ECR  ecom-repo:<svc>-<sha>   │   (NEW: CI/CD)
               └──────────┘  (one Pipeline per service)└──────────────────────────────┘
   13 images built & pushed   ·   deploy stays MANUAL (kubectl)   ·   no cluster yet
```
**What's new in this step:** a working build pipeline — every service's image lands in ECR, tagged by an
immutable git SHA. Nothing is deployed yet; that's the next phases.

## Done when
- Pushing a commit auto-builds and pushes that service's image to ECR, tagged by SHA.
- The pipeline re-runs and works for any of the 13 services.
- All 13 images are present in ECR after the one-time run.
- CI doesn't run `kubectl` — deploying stays manual.

## How to build it
The exact setup — IAM user, EC2 instance, Jenkins install + plugins + credentials, and the AWS CLI /
kubectl / eksctl tooling — lives in the runbook, broken into small parts:
- [`step-by-step-implementation/1.1-create-iam-user.md`](../step-by-step-implementation/1.1-create-iam-user.md)
- [`step-by-step-implementation/1.2-setup-ec2.md`](../step-by-step-implementation/1.2-setup-ec2.md)
- [`step-by-step-implementation/1.3-set-up-jenkins.md`](../step-by-step-implementation/1.3-set-up-jenkins.md)
- [`step-by-step-implementation/1.4-install-aws-tools.md`](../step-by-step-implementation/1.4-install-aws-tools.md)

In short: create the **ECR** repo `ecom-repo`; stand up **Jenkins** with one **path-triggered** pipeline
per service (generated from the **Job DSL seed** [`../.ci/jobs.groovy`](../.ci/jobs.groovy)) that runs
`docker build → tag <svc>-<sha> → push to ECR`; run all 13 once so every image is in ECR. **Deploy stays
manual** — CI never runs `kubectl`.

---
> Interview one-liner: *"CI is one Jenkins pipeline per microservice — lint, test, build, push an immutable `<svc>-<sha>` image
> to ECR. Deployment is deliberately separate and manual; CI's only job is to produce traceable artifacts."*
