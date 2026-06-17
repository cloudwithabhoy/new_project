"""Container entrypoint: `python -m app`.

Runs uvicorn with our logging (log_config=None) and no built-in access log — we
emit our own structured request line in the middleware. uvicorn handles SIGTERM
and drives the lifespan shutdown, giving us graceful draining of in-flight HTTP
requests *and* the async Kafka consumer for free.
"""
import uvicorn

from .config import settings
from .main import app

if __name__ == "__main__":
    uvicorn.run(
        app,
        host="0.0.0.0",
        port=settings.port,
        log_config=None,
        access_log=False,
    )
