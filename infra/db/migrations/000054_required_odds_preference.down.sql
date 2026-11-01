ALTER TABLE user_profiles
  DROP CONSTRAINT IF EXISTS user_profiles_odds_format_valid;

ALTER TABLE user_profiles
  DROP COLUMN IF EXISTS odds_format_selected_at;
