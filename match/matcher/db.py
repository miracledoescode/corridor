"""Shared Postgres connection for matcher batch jobs.

Connects via the Supabase transaction pooler (DB_URL from .env).
pgvector registered so embed.py can write vector(384) columns directly.
"""
import os
from dotenv import load_dotenv
import psycopg
from pgvector.psycopg import register_vector

load_dotenv(dotenv_path=os.path.join(os.path.dirname(__file__), "..", "..", ".env"))


def get_conn(autocommit: bool = False) -> psycopg.Connection:
    # WHY prepare_threshold=None: Supabase transaction pooler routes each round
    # trip to a different backend. psycopg3 prepares statements by default —
    # prepare lands on backend A, execute hits backend B → DuplicatePreparedStatement.
    # Same root cause as the Go side, fixed there with QueryExecModeSimpleProtocol.
    #
    # WHY autocommit is an option: CREATE INDEX CONCURRENTLY cannot run inside
    # a transaction block (see index.py). Everything else wants the default.
    conn = psycopg.connect(
        os.environ["DB_URL"], prepare_threshold=None, autocommit=autocommit
    )
    register_vector(conn)
    return conn
