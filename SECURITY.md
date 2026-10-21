# Security policy

## Reporting a vulnerability

Report vulnerabilities privately with the "Report a vulnerability" button on this repository's Security tab, using GitHub's [private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability). Do not open a public issue.

Include the affected version, a description of the issue and its impact, and steps to reproduce. Never include a real API key.

We aim to acknowledge reports within 3 business days and to agree on a disclosure date with you. Fixes ship as patch versions and are announced in a GitHub Security Advisory.

## Supported versions

Before 1.0, only the latest minor release receives security fixes.

## Handling API keys

The SDK sends your Hyperbolic API key only to the configured Hyperbolic endpoint, only over HTTPS, and never logs it. Plain HTTP is allowed only for loopback test servers. If you believe a key was exposed, revoke it in the [Hyperbolic dashboard](https://app.hyperbolic.ai) immediately.
