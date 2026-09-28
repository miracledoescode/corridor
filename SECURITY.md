# Security Policy

## Reporting a vulnerability

Please report security issues privately, **not** as a public GitHub issue.

Use [GitHub's private vulnerability reporting](https://github.com/miracledoescode/corridor/security/advisories/new)
(Security → Report a vulnerability). That keeps the report visible only to the
maintainers until a fix exists.

Expect an acknowledgement within a few days. Corridor is maintained by one
person, so please allow reasonable time for a fix before public disclosure.

## If you find an exposed credential

This matters more than usual here, because Corridor is configured entirely
through environment variables and a contributor can leak one by accident — a
`.env` committed instead of `.env.example`, a `DB_URL` pasted into an issue, a
venue API key in a log excerpt attached to a bug report.

If you spot one:

1. **Do not open a public issue** quoting it, and do not include the value in
   a PR comment — that just publishes it a second time.
2. Report it privately via the link above, saying **where** it is (file, line,
   commit, or issue number) rather than pasting the secret itself.
3. If it is yours, rotate it immediately at the provider. Removing the commit
   does not un-leak it: anything pushed to a public repository should be
   treated as compromised the moment it lands, and git history keeps it
   reachable even after a later deletion.

## Scope

In scope:

- Anything allowing SQL injection, authentication bypass, or unauthorised
  data access in `corridord` or the matcher.
- Credential handling: leaks through logs, error messages, or API responses.
  Note the Telegram bot token travels in the request *path*, so anything that
  echoes a raw URL into a log is a real finding.
- Dependency vulnerabilities with a plausible path to exploitation here.

Out of scope:

- Vulnerabilities in the prediction-market venues themselves. Report those to
  the venue.
- Findings that depend on an operator deliberately misconfiguring their own
  self-hosted instance.
- Automated scanner output with no demonstrated impact. A scanner rule firing
  is not by itself a vulnerability — please explain the actual exploit path.

## A note for operators

Corridor is self-hosted. You supply your own database, your own venue
credentials, and your own Telegram bot token, and they never leave your
infrastructure. The security of a deployment is the operator's, and the
things most worth getting right are: keep `.env` out of version control,
apply row-level security (`migrations/002_enable_rls.sql`), and do not expose
the API to the internet without putting authentication in front of it — it
ships with none.
