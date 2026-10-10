FROM node:24-bookworm AS build
WORKDIR /app
COPY package.json package-lock.json tsconfig.json ./
# npm ci ставит РОВНО то, что в локе: сборка воспроизводима, а не
# «то, что резолвилось на момент запуска»
RUN npm ci --no-audit --no-fund
COPY src ./src
RUN npx tsc

FROM node:24-bookworm
WORKDIR /app
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/package.json ./package.json
COPY --from=build /app/lib ./lib
ENTRYPOINT ["node", "lib/worker.js"]
