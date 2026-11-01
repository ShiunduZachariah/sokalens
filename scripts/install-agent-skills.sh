#!/usr/bin/env bash
set -euo pipefail

# Run from the project root:
#   ./scripts/install-agent-skills.sh core
#   ./scripts/install-agent-skills.sh discovery
#   ./scripts/install-agent-skills.sh web
#   ./scripts/install-agent-skills.sh accounts
#   ./scripts/install-agent-skills.sh monetization
#   ./scripts/install-agent-skills.sh mobile
#   ./scripts/install-agent-skills.sh hardening
#   ./scripts/install-agent-skills.sh all

PHASE="${1:-core}"
AGENT_NAME="${SKILLS_AGENT_NAME:-codex}"

skill_add() {
  local package="$1"
  local skill="$2"
  npx skills add "$package" --skill "$skill" --agent "$AGENT_NAME" -y
}

skill_add_package() {
  local package="$1"
  npx skills add "$package" --agent "$AGENT_NAME" -y
}

install_core() {
  skill_add "https://github.com/vercel-labs/skills" "find-skills"

  skill_add "https://github.com/obra/superpowers" "brainstorming"
  skill_add "https://github.com/obra/superpowers" "writing-plans"
  skill_add "https://github.com/obra/superpowers" "executing-plans"
  skill_add "https://github.com/obra/superpowers" "verification-before-completion"
  skill_add "https://github.com/obra/superpowers" "test-driven-development"
  skill_add "https://github.com/obra/superpowers" "systematic-debugging"

  skill_add "https://github.com/supabase/agent-skills" "supabase-postgres-best-practices"
  skill_add "https://github.com/xixu-me/skills" "github-actions-docs"
}

install_discovery() {
  skill_add "https://github.com/coreyhaines31/marketingskills" "product-marketing-context"
  skill_add "https://github.com/coreyhaines31/marketingskills" "launch-strategy"
  skill_add "https://github.com/coreyhaines31/marketingskills" "pricing-strategy"
}

install_web() {
  skill_add "https://github.com/vercel-labs/next-skills" "next-best-practices"
  skill_add "https://github.com/vercel-labs/next-skills" "next-cache-components"
  skill_add "https://github.com/vercel-labs/agent-skills" "vercel-react-best-practices"

  skill_add "https://github.com/coreyhaines31/marketingskills" "seo-audit"
  skill_add "https://github.com/coreyhaines31/marketingskills" "programmatic-seo"
  skill_add "https://github.com/coreyhaines31/marketingskills" "schema-markup"
}

install_accounts() {
  skill_add "https://github.com/better-auth/skills" "better-auth-best-practices"
  skill_add "https://github.com/coreyhaines31/marketingskills" "analytics-tracking"
  skill_add "https://github.com/currents-dev/playwright-best-practices-skill" "playwright-best-practices"
}

install_monetization() {
  skill_add "https://github.com/coreyhaines31/marketingskills" "paid-ads"
  skill_add "https://github.com/coreyhaines31/marketingskills" "pricing-strategy"
  skill_add "https://github.com/coreyhaines31/marketingskills" "paywall-upgrade-cro"
}

install_mobile() {
  skill_add "https://github.com/manutej/luxor-claude-marketplace" "mobile-design"
  skill_add "https://github.com/nextlevelbuilder/ui-ux-pro-max-skill" "ui-ux-pro-max"
  skill_add "https://github.com/vercel-labs/agent-skills" "vercel-react-native-skills"
}

install_hardening() {
  skill_add_package "https://cli.sentry.dev"
  skill_add "https://github.com/squirrelscan/skills" "audit-website"
  skill_add "https://github.com/currents-dev/playwright-best-practices-skill" "playwright-best-practices"
}

case "$PHASE" in
  core)
    install_core
    ;;
  discovery)
    install_core
    install_discovery
    ;;
  web)
    install_core
    install_web
    ;;
  accounts)
    install_core
    install_accounts
    ;;
  monetization)
    install_core
    install_monetization
    ;;
  mobile)
    install_core
    install_mobile
    ;;
  hardening)
    install_core
    install_hardening
    ;;
  all)
    install_core
    install_discovery
    install_web
    install_accounts
    install_monetization
    install_mobile
    install_hardening
    ;;
  *)
    echo "Unknown phase: $PHASE"
    echo "Use: core | discovery | web | accounts | monetization | mobile | hardening | all"
    exit 1
    ;;
esac

echo "Done. Restart Codex if newly installed skills do not appear."
