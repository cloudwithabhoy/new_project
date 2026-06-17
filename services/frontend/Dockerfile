# frontend — Node 20, non-root, production-only deps.
# Single stage is fine: there is no build step (vanilla JS, no bundler).
FROM node:20-slim

ENV NODE_ENV=production
WORKDIR /app

# Install deps first for layer caching. npm ci needs package-lock.json; if you
# haven't generated one yet, run `npm install` locally to create it.
COPY package.json package-lock.json* ./
RUN npm ci --omit=dev || npm install --omit=dev

# App source.
COPY server.js ./
COPY public ./public

# Run as the built-in non-root `node` user (uid 1000).
USER node

EXPOSE 3000

CMD ["node", "server.js"]
