# Telegram Operations Bot

Codex and DeepSeek cannot be invited to a Telegram group as ordinary human accounts. They can participate through one SokaLens-owned Telegram bot backed by a small controller service that explicitly routes messages to the selected model.

## Recommended interaction

- `/status` returns ingestion, prediction, quota, deployment, and incident health without invoking an AI model.
- `/codex <request>` starts a bounded Codex task and returns its task/run identifier.
- `/deepseek <request>` starts a bounded DeepSeek diagnostic task.
- `/compare <request>` runs both independently and posts a labelled comparison; neither answer automatically authorizes a production mutation.
- Daily digest and urgent alerts are posted automatically by the operations controller.

## Required inputs

- Telegram BotFather token, stored as `TELEGRAM_BOT_TOKEN` on the VPS.
- Target private group/chat ID, stored as `TELEGRAM_CHAT_ID`.
- Telegram user IDs allowed to issue agent commands, stored as an allowlist.
- Decision on whether commands are read-only or may create GitHub branches and pull requests. Production deploy authority remains with green GitHub CI.

Do not paste the bot token into source, Git, screenshots, or public chat. Rotate it immediately if exposed. The bot should reject messages from every chat and user not explicitly allowlisted, rate-limit agent invocations, redact secrets from output, and record task IDs rather than dumping raw internal logs.
