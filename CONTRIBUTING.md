# Contributing to FusionDB

Thank you for your interest in FusionDB.

FusionDB is proprietary software. External contributions are accepted on a case-by-case basis at the sole discretion of the author.

## Reporting Issues

Open a GitHub Issue with:
- Go version (`go version`)
- OS and architecture
- FusionDB version
- Minimal reproduction case
- Any structured log output (from stderr)

## Security Vulnerabilities

Do **not** open a public issue for security vulnerabilities. Email directly:

**jharvey72@mirage-recon.com**

Include a description of the vulnerability, steps to reproduce, and any known impact. You will receive a response within 72 hours.

## Before Opening a Pull Request

Contact the author at jharvey72@mirage-recon.com before investing time in a contribution. Unsolicited pull requests that have not been discussed in advance may be closed without review.

## Code Style

- Follow standard Go formatting (`gofmt`, `go vet`)
- All exported functions require doc comments
- Tests must cover the happy path and at least one failure case per new function
- No new dependencies without prior approval
