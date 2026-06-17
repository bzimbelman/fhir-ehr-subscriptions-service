# Security Policy

## Reporting a vulnerability

The `fhir-subscriptions-foss` project takes security issues seriously,
especially given that deployments handle protected health information.

If you believe you have found a security vulnerability, please **do not** open
a public GitHub issue. Instead, report it privately by either:

- Opening a private advisory through GitHub Security Advisories on this
  repository (preferred), or
- Emailing `security@fhir-subscriptions-foss.example` (placeholder until the
  project's security mailing list is provisioned).

In your report, please include:

- A description of the issue and its potential impact.
- Steps to reproduce, including any required configuration.
- The version or commit you observed it on.
- Whether you intend to publicly disclose, and on what timeline.

We will acknowledge receipt within five business days, work with you to
understand and reproduce the issue, and aim to ship a fix or mitigation within
30 days for high-severity findings. We will coordinate disclosure with you and
credit the reporter unless you ask us not to.

## Supported versions

While the project is pre-alpha, only the `main` branch is supported. Once the
project tags a first release, this section will list the supported version
window.

## Scope

In scope: any vulnerability in code under this repository, including the
adapter SPI base classes, the channel SPI base classes, the storage layer,
authentication and authorization paths, and the operator-facing surface
(probes, configuration, audit log).

Out of scope: vulnerabilities in third-party dependencies are reported to those
projects directly; this project will respond by upgrading the dependency once a
fix is available.
