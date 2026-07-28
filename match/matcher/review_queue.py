"""review_queue.py — Step 5: human review CLI.

Shows unreviewed market_matches one at a time.
Keypress: y = approve, n = reject (deletes row), s = skip, q = quit.
"""
import sys
from .db import get_conn


def run() -> None:
    with get_conn() as conn:
        rows = conn.execute(
            """
            SELECT
                mm.id,
                mm.confidence,
                mm.resolution_diff,
                va.slug, a.title, a.resolution_criteria,
                vb.slug, b.title, b.resolution_criteria
            FROM market_matches mm
            JOIN markets a  ON a.id  = mm.market_a
            JOIN venues  va ON va.id = a.venue_id
            JOIN markets b  ON b.id  = mm.market_b
            JOIN venues  vb ON vb.id = b.venue_id
            WHERE mm.reviewed_by_human = false
            ORDER BY mm.confidence, mm.id
            """
        ).fetchall()

    if not rows:
        print("review_queue: nothing to review")
        return

    print(f"review_queue: {len(rows)} unreviewed matches\n")
    print("Keys: [y] approve  [n] reject  [s] skip  [q] quit\n")

    approved = rejected = skipped = 0

    with get_conn() as conn:
        for row in rows:
            match_id, confidence, diff_note, va, title_a, res_a, vb, title_b, res_b = row

            print(f"─── [{confidence}] ───────────────────────────────────────")
            print(f"  A ({va}): {title_a}")
            print(f"     {res_a or 'no resolution criteria'}")
            print(f"  B ({vb}): {title_b}")
            print(f"     {res_b or 'no resolution criteria'}")
            print(f"  diff: {diff_note}")
            print()

            while True:
                ch = _getch()
                if ch == "y":
                    conn.execute(
                        "UPDATE market_matches SET reviewed_by_human = true WHERE id = %s",
                        (match_id,),
                    )
                    conn.commit()
                    approved += 1
                    print("  → approved\n")
                    break
                elif ch == "n":
                    conn.execute("DELETE FROM market_matches WHERE id = %s", (match_id,))
                    conn.commit()
                    rejected += 1
                    print("  → rejected\n")
                    break
                elif ch == "s":
                    skipped += 1
                    print("  → skipped\n")
                    break
                elif ch == "q":
                    print(f"\nDone. approved={approved} rejected={rejected} skipped={skipped}")
                    return

    print(f"\nDone. approved={approved} rejected={rejected} skipped={skipped}")


def _getch() -> str:
    if sys.platform == "win32":
        import msvcrt
        return msvcrt.getwch()
    else:
        import tty, termios
        fd = sys.stdin.fileno()
        old = termios.tcgetattr(fd)
        try:
            tty.setraw(fd)
            return sys.stdin.read(1)
        finally:
            termios.tcsetattr(fd, termios.TCSADRAIN, old)


if __name__ == "__main__":
    run()
