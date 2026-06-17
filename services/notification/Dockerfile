# syntax=docker/dockerfile:1

# ---- Build stage ----
# Install dependencies into an isolated prefix so the runtime image stays lean and
# contains no build toolchain.
FROM python:3.12-slim AS build
WORKDIR /build

ENV PIP_NO_CACHE_DIR=1 PIP_DISABLE_PIP_VERSION_CHECK=1

COPY requirements.txt ./
# --prefix keeps everything under /install so we can copy just that into runtime.
RUN pip install --prefix=/install -r requirements.txt

# ---- Runtime stage ----
FROM python:3.12-slim
WORKDIR /app

ENV PYTHONUNBUFFERED=1 PYTHONDONTWRITEBYTECODE=1

# Copy the installed site-packages + console scripts from the build stage.
COPY --from=build /install /usr/local
COPY app ./app

# Run as a non-root user (good Kubernetes security practice).
RUN useradd --uid 10001 --no-create-home --shell /usr/sbin/nologin appuser
USER 10001

EXPOSE 8089
ENTRYPOINT ["python", "-m", "app"]
