"""Remaining-count distributions for in-play counting markets.

Cards, corners, shots, fouls. Goals are modelled separately and properly, by
the Poisson/Dixon-Coles score matrix in `inplay_model`: goals decide the match
result, so they need a joint distribution over both sides, not just a total.
The statistics here need only a total, and a count already observed plus a rate
is enough for that.

WHY NOT JUST SCALE THE PRE-MATCH EXPECTATION. A pre-match expectation of ten
corners says nothing about a match that has produced nine by minute 30. The
match's own observed rate is evidence, and late in a game it is better evidence
than the prior. But early it is almost pure noise -- one corner in the first
four minutes implies twenty-two over ninety -- so a model that simply believes
the observed rate publishes nonsense for the first fifteen minutes, which is
exactly when a reader is most likely to look.

So the rate is a Gamma-Poisson conjugate update, which is the standard answer to
"blend a prior rate with an observed one, weighting the observation by how long
I have been watching":

    prior:      rate ~ Gamma(alpha = mu * s, beta = s)
                where mu is the prior rate per minute and s is the prior's
                strength expressed in MINUTES of equivalent observation.
    posterior:  rate ~ Gamma(alpha + k, beta + m)  after k events in m minutes.

The weight on the observation is m / (s + m), so it earns influence as the match
goes on without anyone writing a schedule of weights. `s` is the only knob and
it has a plain reading: s = 30 means "the prior is worth half an hour of play".

The predictive distribution for the REMAINING minutes is then Negative Binomial,
not Poisson, and that difference is the reason this is written out rather than
approximated:

    N_remaining ~ NegBin(shape = alpha + k, p = (beta + m) / (beta + m + r))

A Poisson at the posterior mean would throw away the uncertainty in the rate
itself and understate the tails. The ladder publishes exactly the band edges
where that matters -- `generate_ladder` keeps a line only while its probability
is inside [epsilon, 1-epsilon] -- so understating the tail silently drops lines
that are live. The over-dispersion is not a refinement here; it decides which
markets exist.
"""

from __future__ import annotations

from dataclasses import dataclass
import math


# The statistics this module can model, and the market key each publishes under.
#
# Keys are deliberately the same shape as the goals ladder (`<market>_<line>`),
# because settlement parses the line off the market key and supersede is keyed
# on (fixture_id, market): a ladder whose line is not in the key would have each
# line supersede the last.
CARDS = "total_cards"
CORNERS = "total_corners"
SHOTS = "total_shots"
SHOTS_ON_TARGET = "shots_on_target"
FOULS = "total_fouls"

COUNTING_STATISTICS = (CARDS, CORNERS, SHOTS, SHOTS_ON_TARGET, FOULS)


@dataclass(frozen=True)
class CountPrior:
    """A prior rate for one counting statistic, and how much it is worth.

    `full_match_mean` is the expected count over a whole match, not per minute:
    that is the unit every source of such a number uses (a league average, a
    pre-match model, a historical team rate), and converting here keeps the
    conversion in one place.
    """

    statistic: str
    full_match_mean: float
    # How many minutes of play the prior is worth. 30 means the match's own
    # observed rate overtakes the prior at the hour mark.
    strength_minutes: float = 30.0

    def __post_init__(self) -> None:
        if self.statistic not in COUNTING_STATISTICS:
            raise ValueError(f"unknown counting statistic: {self.statistic}")
        if not math.isfinite(self.full_match_mean) or self.full_match_mean <= 0.0:
            raise ValueError("full_match_mean must be positive and finite")
        if not math.isfinite(self.strength_minutes) or self.strength_minutes <= 0.0:
            raise ValueError("strength_minutes must be positive and finite")

    @property
    def rate_per_minute(self) -> float:
        return self.full_match_mean / FULL_MATCH_MINUTES


@dataclass(frozen=True)
class CountObservation:
    """What the feeds say this statistic stands at, and where that came from.

    `source` is carried because the two feeds have different trust: cards come
    from the live event list, which is in every bulk poll, while corners come
    from the batched statistics poll, which is a minute behind at best. A market
    published off a one-minute-old count must be attributable to it.
    """

    statistic: str
    observed: int
    source: str

    def __post_init__(self) -> None:
        if self.statistic not in COUNTING_STATISTICS:
            raise ValueError(f"unknown counting statistic: {self.statistic}")
        if self.observed < 0:
            raise ValueError("observed count cannot be negative")


FULL_MATCH_MINUTES = 90.0

# Below this many minutes of play, the match's own rate is not used on its own.
#
# With a prior it does not matter -- the conjugate update already discounts a
# short observation. Without one, there is nothing to discount against, and the
# observed rate alone is indefensible: a single corner in the fourth minute
# implies twenty-two. A market is simply not published in that case, which the
# contract already handles as an absent market rather than a wrong one.
MIN_MINUTES_WITHOUT_PRIOR = 25.0

# Where the remaining-count distribution is truncated.
#
# The tail is infinite and its far end cannot change a published line: once the
# cumulative mass is this close to 1, every remaining line is outside the
# ladder's band anyway. Truncating keeps the dictionary small enough to pass
# around per fixture per tick.
TAIL_MASS_EPSILON = 1e-6
MAX_REMAINING_COUNT = 60


def remaining_count_distribution(
    *,
    observed: int,
    elapsed_minutes: float,
    remaining_minutes: float,
    prior: CountPrior | None = None,
) -> dict[int, float]:
    """`{n: P(exactly n more)}` for the rest of the match.

    Returns `{}` when there is no defensible rate to project -- no prior and too
    little play to infer one, or no time left. An empty distribution means no
    market, which is the honest outcome and the one the contract expects: a
    fixture with no corner data shows no corner market.
    """
    if observed < 0:
        raise ValueError("observed count cannot be negative")
    if elapsed_minutes < 0.0 or remaining_minutes < 0.0:
        raise ValueError("minutes cannot be negative")
    if remaining_minutes <= 0.0:
        # Full time, or as good as. Everything is settled by the count itself.
        return {}

    if prior is not None:
        shape = prior.rate_per_minute * prior.strength_minutes + observed
        rate_scale = prior.strength_minutes + elapsed_minutes
    else:
        if elapsed_minutes < MIN_MINUTES_WITHOUT_PRIOR:
            return {}
        if observed == 0:
            # Zero events in a credible stretch of play is real information,
            # but it leaves a Gamma posterior with zero shape, which has no
            # predictive distribution. Rather than invent a prior here, decline:
            # a statistic at zero by the half hour is one the provider may
            # simply not be reporting for this league, and that is
            # indistinguishable from a genuinely quiet match. Publishing either
            # reading as a prediction would be a guess.
            return {}
        shape = float(observed)
        rate_scale = elapsed_minutes

    if shape <= 0.0:
        return {}

    probability_success = rate_scale / (rate_scale + remaining_minutes)
    return _negative_binomial(shape=shape, probability_success=probability_success)


def _negative_binomial(*, shape: float, probability_success: float) -> dict[int, float]:
    """NegBin pmf with a real-valued shape, truncated at the tail.

    The shape is real rather than integral because it is a Gamma posterior's
    shape, so the combinatorial form does not apply and the gamma function does.
    Computed in logs: `shape` can reach the tens and `probability_success**shape`
    underflows well before the probabilities themselves become negligible.
    """
    if not 0.0 < probability_success <= 1.0:
        raise ValueError("probability_success must be in (0, 1]")

    log_p = math.log(probability_success)
    log_q = math.log1p(-probability_success) if probability_success < 1.0 else -math.inf

    distribution: dict[int, float] = {}
    cumulative = 0.0
    log_gamma_shape = math.lgamma(shape)
    for count in range(MAX_REMAINING_COUNT + 1):
        if log_q == -math.inf:
            probability = 1.0 if count == 0 else 0.0
        else:
            log_probability = (
                math.lgamma(count + shape)
                - log_gamma_shape
                - math.lgamma(count + 1)
                + shape * log_p
                + count * log_q
            )
            probability = math.exp(log_probability)
        distribution[count] = probability
        cumulative += probability
        if cumulative >= 1.0 - TAIL_MASS_EPSILON:
            break

    # Renormalise over what was kept. The truncated tail is below
    # TAIL_MASS_EPSILON, but the ladder reads `1 - P(under)` as the over
    # probability, and a distribution that sums to 0.999999 would bias every
    # line by that much in the same direction.
    total = sum(distribution.values())
    if total <= 0.0:
        return {}
    return {count: probability / total for count, probability in distribution.items()}


def over_probability(*, line: float, observed: int, remaining: dict[int, float]) -> float:
    """P(final count > line), given the count so far and what remains.

    Shared with the ladder rather than reimplemented beside it, because the
    off-by-one here is the easy mistake: a line of 8.5 with 6 observed needs
    THREE more, not two, and `needed` is a strict threshold on the remaining
    count.
    """
    needed = line - observed
    under = sum(probability for count, probability in remaining.items() if count < needed)
    return max(0.0, min(1.0, 1.0 - under))
