FROM node:22-bookworm-slim AS development
WORKDIR /app
ENV NEXT_TELEMETRY_DISABLED=1
COPY package.json package-lock.json ./
RUN npm ci
COPY . .

FROM development AS build
# Public variables are inlined at build time, so the value must reach `next build`.
ARG NEXT_PUBLIC_USER_API_URL=""
ENV NEXT_PUBLIC_USER_API_URL=$NEXT_PUBLIC_USER_API_URL
RUN npm run build

FROM node:22-bookworm-slim AS runtime
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=0.0.0.0 PORT=3000
COPY --from=build --chown=node:node /app/.next/standalone ./
COPY --from=build --chown=node:node /app/.next/static ./.next/static
USER node
EXPOSE 3000
CMD ["node", "server.js"]
