# Security policy

Eddy sits in front of Kubernetes clusters, so we treat security reports as a priority.

## Reporting a vulnerability

Do not open a public issue. Use GitHub's private vulnerability reporting
("Security" tab → "Report a vulnerability") on this repository.

Include the Eddy version, your install method (Helm values with secrets removed),
and steps to reproduce. We aim to acknowledge reports within 3 working days and
to ship a fix or mitigation within 30 days for high-severity issues.

## Supported versions

Only the latest minor release receives security fixes.

## Security model

The design principles (agents dial out, Kubernetes RBAC via impersonation, no
Secret data leaves a cluster, read-only AI) are documented in
[docs/security.md](docs/security.md).
