# Security Policy

## Supported Versions

Only the latest minor release receives security fixes. Older versions are
unsupported — please upgrade before reporting issues against them. Before v1.0,
fixes land on `main` and in the next release.

| Version | Supported |
| ------- | --------- |
| latest  | yes       |
| older   | no        |

## Reporting a Vulnerability

**Please do not open a public GitHub issue for security vulnerabilities.**

Report privately via one of:

- GitHub's [private vulnerability reporting](https://github.com/NoiXdev/pulsix/security/advisories/new) (preferred).
- Email: `security@noix.dev`

Include:

- A description of the issue and its potential impact.
- Reproduction steps or proof-of-concept.
- The affected version (commit SHA or release tag) and component (manager, worker,
  agent).
- Your name/handle for credit (optional).

## Response Timeline

- Acknowledgement of your report within **3 business days**.
- Initial assessment and severity rating within **7 business days**.
- Fix and coordinated disclosure timeline communicated after triage.

We follow coordinated disclosure: please give us reasonable time to ship a fix
before publishing details.

## Scope

In scope:

- The Pulsix manager, worker and agent code in this repository.
- The container images and agent binaries published from this repository.
- The separation between organizations of one installation, and between a
  worker or agent and the manager.

Out of scope:

- Third-party dependencies (please report upstream, then notify us if a patched
  version is available).
- Playbooks, roles and collections you run through Pulsix: they act on your hosts
  with the access you give them.
- Self-hosted deployments that have been modified or misconfigured.
- Findings that require physical access, social engineering, or compromised
  developer machines.
