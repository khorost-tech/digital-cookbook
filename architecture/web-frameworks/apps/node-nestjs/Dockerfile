FROM node:24.20.0-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci -q --no-audit --no-fund
COPY . .
RUN npx tsc -p .
ENTRYPOINT ["node", "dist/main.js"]
