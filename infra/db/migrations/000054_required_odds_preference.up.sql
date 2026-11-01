ALTER TABLE user_profiles
  ADD COLUMN odds_format_selected_at TIMESTAMPTZ;

ALTER TABLE user_profiles
  ADD CONSTRAINT user_profiles_odds_format_valid
  CHECK (odds_format IN ('decimal', 'fractional', 'american'));
