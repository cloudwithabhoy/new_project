# Phase 18 — Developer network access

## Goal
Let any developer, from anywhere, reach all three regions' private resources — EKS API, bastions, databases —
over one secure VPN connection, while the control plane stays off the public internet.

## Why this phase
The regions are live with private EKS API endpoints, but developers still need to reach them to do their
jobs — and the control plane must never be exposed publicly. This phase delivers network reachability only;
*who* may do *what* is access management (Phase 19), which is meaningless until the reach exists.
- **Builds on:** Phase 09 (three regions, each its own VPC + EKS cluster).
- **Unlocks:** Phase 19 (IAM/RBAC access management, which decides what reachable developers can do).

## Scope
**In scope:** AWS Client VPN dial-in, a Transit Gateway hub-and-spoke joining the 3 regional VPCs, private
EKS API endpoints, security groups/NACLs for VPN reach, one kubeconfig with per-cluster contexts, and an
optional SSM Session Manager shell/break-glass path.
**Out of scope:** authorization — *who can do what* — which is IAM/RBAC access management (Phase 19).

## What it needs to do
- One AWS Client VPN connection lets any developer reach every region's private subnets.
- A Transit Gateway hub-and-spoke with inter-region peering stitches the 3 regional VPCs into one mesh.
- Each cluster's EKS API endpoint is private and reachable only from inside the connected VPCs — never on
  the public internet.
- One kubeconfig carries a context per cluster; switching regions needs no re-dial.
- Security groups/NACLs allow the VPN client CIDR to the EKS API, bastions, and DB ports.
- Split-tunnel routes only the three VPC CIDRs through the tunnel, not the developer's whole internet.

## Architecture

```
   developers (anywhere in the world)
            │
            ▼
   ┌──────────────────┐
   │  AWS Client VPN  │   (NEW)  one connection for any dev
   └─────────┬────────┘
             ▼
   ┌─────────────────────────────┐
   │     AWS Transit Gateway     │   hub connecting all region VPCs
   └───┬────────────┬────────────┬┘
       ▼            ▼            ▼
  Mumbai VPC   London VPC   Singapore VPC
  (PRIVATE EKS API + bastions — never public)
```
**What's new in this step:** Client VPN + Transit Gateway — one connection lets any developer reach every
region's private resources; the EKS API stays private (RBAC then governs *what* they can do — Step 19).

## The design
One Client VPN to dial in plus a Transit Gateway hub-and-spoke joining the 3 regional VPCs, so a single connection reaches all regions' private subnets.

```
            developer (anywhere)
                   │  one VPN tunnel
                   ▼
        ┌──── AWS Client VPN endpoint ────┐
        │   (cert/SSO auth, split-tunnel) │
        └───────────────┬─────────────────┘
                        │ attaches to a VPC
                        ▼
     ┌──────────── Transit Gateway mesh (peered cross-region) ────────────┐
     │  TGW ap-south-1 ══ TGW eu-west-2 ══ TGW ap-southeast-1             │
     └────┬──────────────────┬──────────────────────┬────────────────────┘
          ▼                  ▼                       ▼
   Mumbai VPC          UK VPC                  Singapore VPC
   private EKS API     private EKS API         private EKS API
   bastion · DB        bastion · DB            bastion · DB

   kubectl: ONE kubeconfig, a context per cluster
   (aws eks update-kubeconfig --region <r> --name <cluster> --alias <ctx>)
```

## How it works / why this approach
A private EKS API is only resolvable/reachable from inside the connected VPCs, so the control plane is never
on the public internet — developers reach it because the VPN puts them inside the network. The kubeconfig
still uses IAM (`aws eks get-token`), and RBAC decides actions (Phase 19): VPN is the reachability layer,
IAM/RBAC is the authorization layer. AWS Client VPN is managed OpenVPN — developers authenticate with
certificates or SSO/SAML, get an IP, and route into the attached VPC; split-tunnel means only the three VPC
CIDRs go through the tunnel. The VPN attaches to one VPC, and inter-region TGW peering stitches the three
regional routers into one mesh, so a developer's packets hop VPN → local TGW → peered TGW → target region's
private subnet. Connect once, reach every region's EKS API / bastion / DB. One `aws eks update-kubeconfig`
per region with an `--alias` gives one kubeconfig with a context per cluster; switch with
`kubectl config use-context`, no re-dial.

Why not the alternatives:
- **Public EKS API + IP allowlist:** brittle (devs roam, IPs churn, VPNs/CGNAT), and it still puts the
  control plane on the internet. Private + VPN removes the exposure entirely.
- **Per-region isolated VPNs:** a dev would juggle three tunnels and still couldn't cross from one region
  into another — that directly fails the "reach every region" requirement. TGW gives one hub-and-spoke
  fabric instead.
- **Inter-region VPC peering instead of TGW:** works for 3 VPCs, but peering is non-transitive and grows as
  a full mesh (N·(N−1)/2 links, route tables per VPC). TGW is one hub that scales as regions are added.
- **Bastion / SSM Session Manager instead of VPN (for shell):** for shell access to a node or DB, SSM
  Session Manager needs no VPN, no open ports, and no bastion to patch — the better path for ad-hoc shells
  and a good break-glass. But `kubectl` against a private API still needs network reachability, so VPN+TGW
  stays the primary model and SSM is the complementary shell option.

## How to build it
1. **Lock EKS APIs private** in all three clusters (private endpoint; drop public access or CIDR-lock it).
2. **Transit Gateway per region**, attach each regional VPC, then **TGW inter-region peering** across the
   three; add routes so each VPC's table reaches the other two CIDRs.
3. **AWS Client VPN endpoint** (cert or SSO auth, split-tunnel), associate it with one VPC's subnet, and
   add authorization/routes for all **three** VPC CIDRs through TGW.
4. **Security groups / NACLs**: allow the VPN client CIDR to the EKS API, bastions, and DB ports.
5. **kubeconfig**: `aws eks update-kubeconfig --region <r> --name <cluster> --alias <ctx>` for each of the
   three regions; verify context switching.
6. **(Optional) SSM Session Manager** on bastions/nodes as the no-VPN shell + break-glass path.

## Done when
- With the VPN down, `kubectl` to any cluster and the private API endpoints fail (not exposed).
- With the VPN up, a single connection reaches all three regions: `kubectl get nodes` works for each context, and you can open an SSM/bastion shell and hit a DB in each region.
- A developer in any location runs through Mumbai, UK, and Singapore contexts without re-connecting.

---
> Interview one-liner: *"I keep every cluster's EKS API private and put developers on the network with AWS Client VPN, then join the three regional VPCs with a Transit Gateway mesh so one VPN connection reaches all regions' private resources; one kubeconfig with a context per cluster gives kubectl everywhere, IAM/RBAC governs what they can actually do, and SSM Session Manager covers shell access — I never expose the control plane to the internet or make devs juggle a tunnel per region."*
