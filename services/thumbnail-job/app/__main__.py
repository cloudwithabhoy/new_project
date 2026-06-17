"""Container entrypoint: `python -m app`.

Runs the asyncio Kafka consumer loop. `worker.run()` installs SIGTERM/SIGINT
handlers itself, so Kubernetes rollouts drain the in-flight message and exit 0.
"""
from .worker import main

if __name__ == "__main__":
    main()
