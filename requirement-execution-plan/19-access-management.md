# Phase 19 — Access management (SSO / AD groups → RBAC & IAM)

## Goal
People get access to AWS and the EKS clusters through **corporate AD groups**, not per-user logins — least-privilege, env-scoped, and auditable. "Give Asha only dev" is one action: add her to a group.

## Why this phase
Once people can reach the clusters, you need a safe, central way to hand out access. Driving everything off AD groups means joiners/leavers are handled once in HR's directory, and removing someone from a group takes away both their AWS and Kubernetes access with nothing left behind.
- **Builds on:** Phase 18 (developers can reach the regions).
- **Unlocks:** safe multi-team operation of the whole estate.

## Scope
**In scope:** federating Azure AD / Okta into IAM Identity Center (SAML + SCIM); permission sets per role/env mapped from AD groups; EKS access entries / `aws-auth` mapping those roles into clusters; namespace-scoped RBAC for env/team isolation and ClusterRoles for platform admins.
**Out of scope:** redesigning the per-region cluster stacks (Phases 01-08); application-level authorization inside the services.

## What it needs to do
- The corporate IdP (Azure AD / Okta) federates into **IAM Identity Center** via SAML, with users and AD groups synced via SCIM.
- Each AD group maps to a **permission set** (a scoped IAM role); login yields temporary STS credentials, never long-lived keys.
- Each permission-set role is mapped into clusters via **EKS access entries** (or `aws-auth`) — and only into the clusters it should reach.
- Kubernetes RBAC binds normal roles with namespace-scoped `Role` + `RoleBinding` (on `shop`/`data`) and platform admins with `ClusterRole` + `ClusterRoleBinding`.
- Removing a user from an AD group takes away both AWS and Kubernetes access.
- Each role/env gets exactly one permission set plus one RBAC binding — no shared admin role.
- Every privileged action is attributable to a person: CloudTrail records the SSO identity for AWS calls, EKS audit logs record the user for kube API calls.
- A group mapped only into the dev cluster can't even authenticate to staging/prod.

## Architecture

```
   Azure AD / Okta  ──(SAML / SCIM)──►  ┌───────────────────────────┐
   (AD GROUPS)                          │  AWS IAM Identity Center   │   (NEW)
                                        │  group ─► permission set   │
                                        └─────────────┬─────────────┘
              ┌───────────────────────────────────────┼───────────────────────────┐
              ▼                                        ▼                           ▼
        IAM role (AWS perms)            EKS access entry / aws-auth          CloudTrail audit
                                                 ▼
                                        Kubernetes RBAC:
                                          RoleBinding ─► namespace-scoped   (env isolation)
                                          ClusterRole ─► platform-admins only
   "give X only the dev environment"  =  AD group ─► permission set ─► RoleBinding scoped to dev
```
**What's new in this step:** SSO via AD groups → IAM Identity Center permission sets → EKS RBAC —
group-based, env-scoped, least-privilege, and auditable (deprovision = remove from the AD group).

## The design
Identity lives in the IdP; AWS access and Kubernetes access are both **derived** from AD-group membership.
```
  Azure AD / Okta (groups: dev-ro, prod-platform-admin, ...)
        │  SAML/SCIM federation
        ▼
  AWS IAM Identity Center  ── group ──► PERMISSION SET (= an IAM role + scoped policy)
        │  user assumes role (STS, temporary creds)
        ▼
  EKS access entry / aws-auth  ── maps the IAM role ──►  Kubernetes RBAC
        ├─ Role + RoleBinding  → scoped to ONE namespace/cluster  (env/team isolation)
        └─ ClusterRole         → platform admins only
```
"One environment" vs "full access" is just **which binding** the group's role lands in: a `RoleBinding`
in the dev cluster's `shop` ns (one env) vs a `ClusterRoleBinding` across all clusters (platform admin).

## How it works / why this approach
- **Identity = the IdP.** Azure AD / Okta is the single source of truth for who works here. It federates into AWS IAM Identity Center (formerly AWS SSO) via SAML; users + groups sync via SCIM. Joiners/leavers are handled once, in HR's directory.
- **AWS access = permission sets, mapped from groups.** A permission set is an IAM role with a scoped policy, e.g. `dev-readonly`, `staging-deployer`, `prod-platform-admin`. You assign a group to a permission set in an account. Login yields temporary STS credentials — no long-lived keys.
- **Kubernetes access = IAM role → RBAC.** Each permission-set role is mapped into a cluster via an EKS access entry (or the legacy `aws-auth` ConfigMap) to a Kubernetes group. RBAC then decides what that group can do: a `Role` + `RoleBinding` scoped to a namespace (`shop`/`data`) for env/team isolation; a `ClusterRole` + `ClusterRoleBinding` only for platform admins.
- **Env-scoping, concretely.** Environments map to clusters/namespaces. "Dev only" = the group's role is mapped only in the dev cluster and bound by a `RoleBinding` in `shop`/`data` there — it has no mapping in staging/prod, so it can't even authenticate to them. "Full access" = the platform-admin group's role is mapped in all clusters with a `ClusterRoleBinding`.

Why this and not the alternatives:
- **Per-user IAM users** — no central lifecycle; every joiner/leaver is manual, drift is guaranteed, and there's no group abstraction to reason about. Doesn't scale past a tiny team.
- **One shared admin role** — no least-privilege and no audit: CloudTrail shows the role, not the person, so you can't tell who did what.
- **Long-lived static keys** — leak and rotation risk; the thing you're trying to kill.
- **SSO + AD groups** gives central lifecycle (deprovision = remove from the group), least-privilege (one permission set + RBAC binding per role/env), and a real audit trail.

## How to build it
1. **Federate the IdP**: connect Azure AD / Okta to **IAM Identity Center** (SAML), enable **SCIM** so
   users + AD groups sync automatically.
2. **Define permission sets** per role/env (`dev-readonly`, `staging-deployer`, `prod-platform-admin`,
   …) with **scoped IAM policies**; **assign each AD group → permission set** in the right account(s).
3. **Map roles into each cluster**: create **EKS access entries** (or `aws-auth` entries) tying each
   permission-set IAM role to a Kubernetes group — and **only** in the clusters that role should reach.
4. **Author RBAC**: namespace-scoped `Role` + `RoleBinding` (per env/team, on `shop`/`data`) for normal
   roles; `ClusterRole` + `ClusterRoleBinding` for platform admins.
5. **Prove env-scoping + audit** (exit check).

## Done when
- A user in the **dev** AD group logs in via SSO, gets `kubectl` access to the **dev** cluster's `shop` ns **only**, and is **denied** on staging/prod (no access entry there).
- A **platform-admin** group member has cluster-wide access across **all** clusters.
- **Removing** a user from the AD group revokes both AWS and Kubernetes access (no leftover IAM user/key).
- A privileged action is **attributable to the person** in CloudTrail / EKS audit logs.

---
> Interview one-liner: *"Identity stays in Azure AD/Okta, federated into IAM Identity Center; AD groups map to permission sets
> (scoped IAM roles), and those roles map via EKS access entries into Kubernetes RBAC — namespace-scoped
> Roles for env/team isolation, ClusterRoles only for platform admins — so 'dev access only' is one
> group's role bound to just the dev cluster, deprovisioning is removing someone from the AD group, and
> every action is least-privilege and attributable in CloudTrail."*
