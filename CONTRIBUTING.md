# Contributing to SokaLens

Thanks for taking the time. This page explains how a change gets from your
pull request into the running product, because it works differently from most
repositories.

## How this repository works

SokaLens is developed and deployed from a private repository. The public
repository is a published snapshot of the production `main` branch, refreshed
as a single new commit each time the snapshot is updated. Its GitHub Actions
are switched off; checks run in the private repository.

So your pull request is **not merged on GitHub**. Instead:

1. You open a pull request against `main` here. Review happens in public, on
   your pull request.
2. Once it is approved, the maintainer applies your commits to the private
   repository as patches. Your name and commit messages are kept.
3. The change goes through the private CI, the QA environment and a release to
   production.
4. The next snapshot refresh brings it back here, with your pull request
   credited in the commit message, and your pull request is closed with a link
   to that commit.

Because the snapshot can move on while you work, keep your branch based on the
latest `main` and rebase if asked.

## Before you start

- For anything larger than a small fix, open an issue first so the approach can
  be agreed before you spend time on it.
- Read [README.md](README.md) for the architecture, and
  [AGENTS.md](AGENTS.md) for the engineering rules; they apply to human
  contributors too. In particular:
  - in-play work follows
    [the in-play data contract](docs/integrations/in-play-data-contract.md);
  - applied migrations under `infra/db/migrations` are never edited, only added
    to;
  - security gates are never weakened to make a change pass.

## Making a change

Run the stack and the checks locally as described in
[Running it locally](README.md#running-it-locally) and
[Tests and checks](README.md#tests-and-checks). At minimum, before you open the
pull request:

```bash
task test
task lint
```

Keep each pull request to one concern, and describe what changed, why, and how
you verified it.

Changes to workflows, scripts, Dockerfiles or dependency manifests get the
closest review: those files run on the project's own infrastructure.

## What not to include

- Credentials, tokens, server addresses or `.env` files. Use the `*.example`
  files for new settings.
- Real people's names, email addresses or account details. Test data uses
  `example.com` and `.test` addresses.
- Third-party data you do not have the right to redistribute, including match
  data from providers.

## Security issues

Do not open a public issue for a vulnerability. Use GitHub's
**Report a vulnerability** button on the repository's Security tab.

## Licence

SokaLens is licensed under the [GNU AGPL v3.0](LICENSE). By contributing, you
agree that your contribution is licensed under the same terms. The SokaLens
name and logo are not licensed for reuse.
