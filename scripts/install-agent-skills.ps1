param(
  [ValidateSet("core", "discovery", "web", "accounts", "monetization", "mobile", "hardening", "all")]
  [string] $Phase = "core",
  [string] $AgentName = "codex"
)

$ErrorActionPreference = "Stop"

function Add-Skill {
  param(
    [string] $Package,
    [string] $Skill
  )

  npx skills add $Package --skill $Skill --agent $AgentName --yes
}

function Add-SkillPackage {
  param([string] $Package)

  npx skills add $Package --agent $AgentName --yes
}

function Install-Core {
  Add-Skill "https://github.com/vercel-labs/skills" "find-skills"

  Add-Skill "https://github.com/obra/superpowers" "brainstorming"
  Add-Skill "https://github.com/obra/superpowers" "writing-plans"
  Add-Skill "https://github.com/obra/superpowers" "executing-plans"
  Add-Skill "https://github.com/obra/superpowers" "verification-before-completion"
  Add-Skill "https://github.com/obra/superpowers" "test-driven-development"
  Add-Skill "https://github.com/obra/superpowers" "systematic-debugging"

  Add-Skill "https://github.com/supabase/agent-skills" "supabase-postgres-best-practices"
  Add-Skill "https://github.com/xixu-me/skills" "github-actions-docs"
}

function Install-Discovery {
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "product-marketing-context"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "launch-strategy"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "pricing-strategy"
}

function Install-Web {
  Add-Skill "https://github.com/vercel-labs/next-skills" "next-best-practices"
  Add-Skill "https://github.com/vercel-labs/next-skills" "next-cache-components"
  Add-Skill "https://github.com/vercel-labs/agent-skills" "vercel-react-best-practices"

  Add-Skill "https://github.com/coreyhaines31/marketingskills" "seo-audit"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "programmatic-seo"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "schema-markup"
}

function Install-Accounts {
  Add-Skill "https://github.com/better-auth/skills" "better-auth-best-practices"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "analytics-tracking"
  Add-Skill "https://github.com/currents-dev/playwright-best-practices-skill" "playwright-best-practices"
}

function Install-Monetization {
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "paid-ads"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "pricing-strategy"
  Add-Skill "https://github.com/coreyhaines31/marketingskills" "paywall-upgrade-cro"
}

function Install-Mobile {
  Add-Skill "https://github.com/manutej/luxor-claude-marketplace" "mobile-design"
  Add-Skill "https://github.com/nextlevelbuilder/ui-ux-pro-max-skill" "ui-ux-pro-max"
  Add-Skill "https://github.com/vercel-labs/agent-skills" "vercel-react-native-skills"
}

function Install-Hardening {
  Add-SkillPackage "https://cli.sentry.dev"
  Add-Skill "https://github.com/squirrelscan/skills" "audit-website"
  Add-Skill "https://github.com/currents-dev/playwright-best-practices-skill" "playwright-best-practices"
}

switch ($Phase) {
  "core" { Install-Core }
  "discovery" { Install-Core; Install-Discovery }
  "web" { Install-Core; Install-Web }
  "accounts" { Install-Core; Install-Accounts }
  "monetization" { Install-Core; Install-Monetization }
  "mobile" { Install-Core; Install-Mobile }
  "hardening" { Install-Core; Install-Hardening }
  "all" {
    Install-Core
    Install-Discovery
    Install-Web
    Install-Accounts
    Install-Monetization
    Install-Mobile
    Install-Hardening
  }
}

Write-Output "Done. Restart Codex if newly installed skills do not appear."
