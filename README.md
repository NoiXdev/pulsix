<p align="center"><a href="https://www.noix.dev/projekte/pulsix" target="_blank"><img src="brand/logo/pulsix-banner.svg" width="600" alt="Pulsix"></a></p>

<p align="center">
  <a href="https://github.com/NoiXdev/pulsix/actions/workflows/ci.yml"><img src="https://github.com/NoiXdev/pulsix/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://docs.noix.dev/pulsix/admin/introduction/"><img src="https://img.shields.io/badge/docs-docs.noix.dev-00E5FF.svg" alt="Documentation"></a>
</p>

## About Pulsix

Pulsix is a self-hostable platform for managing and monitoring servers. It brings
together three things that usually live in separate tools:

- **Server management** with Ansible: an inventory with groups, typed variables and
  playbook runs, authored and executed from the web interface.
- **Monitoring**: a lightweight agent on each host plus remote checks from the
  network, time-series graphs, and a check engine with inheritable rules.
- **Alerting and status pages**: notification routing by schedule and channel,
  escalation, maintenance windows, and public status pages with incident updates.

One installation can serve several organizations, each with its own hosts, users,
inventory, monitoring and status pages.

| Component | Where it runs | Role |
|---|---|---|
| Manager | Your infrastructure, as a container | Web interface, API, scheduling, alert evaluation, status pages |
| Worker | One or more per zone (site, network, region) | Runs playbooks, performs remote checks, terminates agent connections |
| Agent | On every monitored host | Collects metrics and runs local checks, connects outbound only |

## Status

Pulsix is under active development. The first public release is planned as v1.0;
until then, versions are early-adopter releases and may change between minor
versions. The [changelog](https://docs.noix.dev/pulsix/reference/changelog/) lists
what changed.

## Getting started

The documentation lives at **[docs.noix.dev/pulsix](https://docs.noix.dev/pulsix/admin/introduction/)**:

- [Requirements](https://docs.noix.dev/pulsix/admin/installation/requirements/) and
  [installation with Docker Compose](https://docs.noix.dev/pulsix/admin/installation/docker-compose/)
- [First setup](https://docs.noix.dev/pulsix/admin/installation/first-setup/)
- [User manual](https://docs.noix.dev/pulsix/user/getting-started/)
- [Reference](https://docs.noix.dev/pulsix/reference/agent-configuration/), including the API

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## Code of Conduct

This project follows the [Contributor Covenant v2.1](CODE_OF_CONDUCT.md).

## Security

Report vulnerabilities privately — see [SECURITY.md](SECURITY.md).

## License

Pulsix is open-source software licensed under the [MIT license](LICENSE).
