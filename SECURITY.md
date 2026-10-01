# Security

QS-PodScript's local mode only listens on 127.0.0.1. The shared server mode
(`qs-podscript server`) is meant to run behind a reverse proxy with HTTPS and
has logins, API keys for helpers' computers and editor rights per podcast.

If you find a security problem – especially in the server mode (logins,
rights, the helpers' API, the pass-through from local installations) –
please **don't open a public issue**. Report it privately via GitHub:
**Security → Report a vulnerability** on this repository
(https://github.com/baumbatz/qs-podscript/security/advisories/new).

Please include the version, how to reproduce it and what an attacker could
do. You'll get an answer as soon as possible; fixes go into a new release
with a note in DONE.md.
