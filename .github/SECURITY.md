# Security Policy

## Supported versions

Only the latest tagged release receives security fixes.

## Reporting a vulnerability

Please report suspected vulnerabilities privately using GitHub's
**Report a vulnerability** button on the repository's Security tab
(private vulnerability reporting is enabled). Do not open a public issue for
security reports.

We aim to acknowledge reports within a few business days and will coordinate a
fix and disclosure timeline with you.

## Automated scanning

This repository runs GitHub's security tooling on every push and pull request:

- **CodeQL** — static analysis (security-and-quality queries)
- **govulncheck** — Go vulnerability database, including the standard library
- **Dependency review** — blocks pull requests that add vulnerable dependencies
- **Dependabot** — alerts and automated dependency updates
- **Secret scanning + push protection** — blocks committing credentials

No credentials are stored in this repository or required to build it.
