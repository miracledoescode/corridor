"""Tests for _link_event — the step that fills markets.event_id.

The case that matters most is two markets that ALREADY belong to different
events. Getting that wrong leaves the two halves of a confirmed match
permanently split, silently, which defeats the whole point of Phase 2.
"""
import pytest

from matcher.resolution_diff import _link_event


class _Result:
    def __init__(self, rows):
        self._rows = rows

    def fetchall(self):
        return self._rows

    def fetchone(self):
        return self._rows[0] if self._rows else None


class FakeConn:
    """Minimal psycopg stand-in: records SQL, replays canned market rows."""

    def __init__(self, market_rows, new_event_id=99):
        self.market_rows = market_rows
        self.new_event_id = new_event_id
        self.calls = []

    def execute(self, sql, params=None):
        normalized = " ".join(sql.split())
        self.calls.append((normalized, params))
        if normalized.startswith("SELECT id, event_id, title"):
            return _Result(self.market_rows)
        if "INSERT INTO events" in normalized:
            return _Result([(self.new_event_id,)])
        return _Result([])

    def sql_run(self):
        return [sql for sql, _ in self.calls]

    def repoints(self):
        """UPDATEs that move markets off a losing event onto the canonical one."""
        return [
            params
            for sql, params in self.calls
            if "UPDATE markets SET event_id" in sql and "WHERE event_id = ANY" in sql
        ]

    def backfills(self):
        """UPDATEs that only fill markets with no event yet."""
        return [
            params
            for sql, params in self.calls
            if "UPDATE markets SET event_id" in sql and "event_id IS NULL" in sql
        ]


def test_related_is_never_linked():
    # RELATED means "same topic, different question" — linking them would
    # merge two genuinely different events into one.
    conn = FakeConn([(1, None, "A"), (2, None, "B")])
    _link_event(conn, 1, 2, "RELATED")
    assert conn.calls == []


def test_creates_event_when_neither_market_has_one():
    conn = FakeConn([(1, None, "World Cup winner"), (2, None, "WC winner")], new_event_id=42)
    _link_event(conn, 1, 2, "EXACT")

    assert any("INSERT INTO events" in sql for sql in conn.sql_run())
    assert conn.backfills() == [(42, [1, 2])]
    assert conn.repoints() == []


def test_reuses_existing_event_without_creating_another():
    conn = FakeConn([(1, 7, "A"), (2, None, "B")])
    _link_event(conn, 1, 2, "EXACT")

    assert not any("INSERT INTO events" in sql for sql in conn.sql_run())
    assert conn.backfills() == [(7, [1, 2])]
    assert conn.repoints() == []


def test_merges_two_different_existing_events():
    # The regression this guards: market 1 landed in event 5 and market 2 in
    # event 9 on earlier runs (each matched to some third market first). The
    # old code picked one id and then updated only rows WHERE event_id IS NULL
    # — matching neither row — so the pair stayed split forever.
    conn = FakeConn([(1, 5, "A"), (2, 9, "B")])
    _link_event(conn, 1, 2, "EXACT")

    assert not any("INSERT INTO events" in sql for sql in conn.sql_run())
    # Lowest id wins; the markets sitting on event 9 get repointed onto it.
    assert conn.repoints() == [(5, [9])]


def test_merge_picks_lowest_event_id_regardless_of_argument_order():
    conn = FakeConn([(1, 9, "A"), (2, 5, "B")])
    _link_event(conn, 1, 2, "EXACT")

    assert conn.repoints() == [(5, [9])]


def test_same_event_on_both_sides_does_not_repoint():
    conn = FakeConn([(1, 5, "A"), (2, 5, "B")])
    _link_event(conn, 1, 2, "EXACT")

    assert conn.repoints() == []
    assert not any("INSERT INTO events" in sql for sql in conn.sql_run())


@pytest.mark.parametrize("confidence", ["EXACT", "CHECK_TERMS"])
def test_non_related_tiers_all_link(confidence):
    # CHECK_TERMS still describes the same real-world event — it just needs a
    # human before it's tradable — so it must still get an event_id.
    conn = FakeConn([(1, None, "A"), (2, None, "B")])
    _link_event(conn, 1, 2, confidence)

    assert conn.backfills(), f"{confidence} should still link an event"
