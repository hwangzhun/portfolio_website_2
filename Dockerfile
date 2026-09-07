FROM node:22-bookworm-slim AS dependencies

WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --omit=dev && npm cache clean --force

FROM node:22-bookworm-slim AS production

ENV NODE_ENV=production \
    API_PORT=8787 \
    DATA_DIR=/app/data \
    UPLOAD_DIR=/app/data/uploads

WORKDIR /app
COPY --from=dependencies /app/node_modules ./node_modules
COPY package.json ./
COPY index.html video.html ./
COPY assets ./assets
COPY language ./language
COPY server ./server

RUN mkdir -p /app/data/uploads && chown -R node:node /app

USER node
EXPOSE 8787
CMD ["node", "server/index.js"]
