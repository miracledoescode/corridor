"""Tests for venue_pairs — which side of a venue pair drives the k-NN scan.

Getting the direction wrong is not a correctness bug, it's a ~75x
performance bug: the driver side is scanned row by row while the other side
is served by the HNSW index.
"""
import pytest

from matcher.pair_llm import venue_pairs


@pytest.mark.parametrize(
    "name, sizes, want",
    [
        (
            "smaller venue drives",
            [(1, 10_000), (2, 130)],
            [(2, 1)],
        ),
        (
            "input order does not matter",
            [(2, 130), (1, 10_000)],
            [(2, 1)],
        ),
        (
            "three venues pair up smallest-first",
            [(1, 10_000), (2, 130), (3, 50)],
            [(3, 2), (3, 1), (2, 1)],
        ),
        (
            "equal sizes still produce one pair",
            [(1, 100), (2, 100)],
            [(1, 2)],
        ),
        ("single venue has nothing to pair with", [(1, 10_000)], []),
        ("no venues", [], []),
    ],
)
def test_venue_pairs(name, sizes, want):
    assert venue_pairs(sizes) == want, name


def test_every_venue_combination_appears_exactly_once():
    # A missed pair means those two venues never get compared at all.
    sizes = [(1, 500), (2, 130), (3, 50), (4, 9_000)]
    pairs = venue_pairs(sizes)

    assert len(pairs) == 6  # 4 choose 2
    unordered = {frozenset(p) for p in pairs}
    assert len(unordered) == 6


def test_driver_is_always_the_smaller_side():
    sizes = [(1, 500), (2, 130), (3, 50), (4, 9_000)]
    by_id = dict(sizes)

    for driver, other in venue_pairs(sizes):
        assert by_id[driver] <= by_id[other], (
            f"venue {driver} ({by_id[driver]}) should not drive a scan "
            f"against smaller venue {other} ({by_id[other]})"
        )
