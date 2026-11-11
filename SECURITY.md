# Security policy

Blur handles potentially sensitive personal data. Treat privacy and security
issues as high priority and avoid including captured content, credentials, or
personal data in public reports.

## Supported versions

Only the latest minor release receives security fixes. Blur does not yet ship a
released desktop application; until it does, the `main` branch is the only
supported line.

## Reporting a vulnerability

Please report suspected vulnerabilities privately through GitHub Security
Advisories for the `hazcod/blur` repository:
https://github.com/hazcod/blur/security/advisories/new

Include the affected version or commit, platform, steps to reproduce, and the
potential impact. Do not publish details before a fix has been coordinated. Do
not open a public issue for a vulnerability.

## Scope

Relevant reports include unauthorized access to local memory, unintended data
egress, insecure credential handling, unsafe capture or injection behavior, and
vulnerabilities in build or release workflows. Reports about planned behavior
are welcome as design feedback.
