-- Bring the one pre-existing priority score into the scale 000062 introduced.
--
-- Before 000062, exactly one league in 1,240 had a non-zero priority_score:
-- Kenyan Premier League, at 90. Nothing read it for the prediction feed, so it
-- was inert. 000062 made the feed order by this column, which would have
-- placed that league above Serie A (88), Bundesliga (86) and Ligue 1 (84), and
-- level with La Liga (90), for every reader on earth.
--
-- That is the same mistake the web's competition ordering already carries a
-- comment about: a substring match once scored Kenya's Premier League as
-- England's, and Gor Mahia led the homepage worldwide.
--
-- 60 keeps it ranked -- comfortably ahead of the unranked catalogue, so it
-- still surfaces above the lower divisions that were crowding the feed -- while
-- sitting below the competitions with a global audience. This is a judgement
-- about reach, not merit, and it is a single number to change if the intent is
-- to lead with Kenyan football.
--
-- Additive: one UPDATE of an existing non-null column. No schema change.

UPDATE leagues
SET priority_score = 60
WHERE name = 'Kenyan Premier League'
  AND country_code = 'KE'
  AND priority_score = 90;
