"""Shared Postgres connection for matcher batch jobs.

Connects via the Supabase transaction pooler (DB_URL from .env).
pgvector registered so embed.py can write vector(384) columns directly.
"""
import os
from dotenv import load_dotenv
import psycopg
from pgvector.psycopg import register_vector

load_dotenv(dotenv_path=os.path.join(os.path.dirname(__file__), "..", "..", ".env"))


def get_conn() -> psycopg.Connection:
    # WHY prepare_threshold=None: Supabase transaction pooler routes each round
    # trip to a different backend. psycopg3 prepares statements by default —
    # prepare lands on backend A, execute hits backend B → DuplicatePreparedStatement.
    # Same root cause as the Go side, fixed there with QueryExecModeSimpleProtocol.
    conn = psycopg.connect(os.environ["DB_URL"], prepare_threshold=None)
    register_vector(conn)
    return conn
