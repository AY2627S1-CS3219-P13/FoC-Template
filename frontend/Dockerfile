FROM node:22-bookworm-slim AS build
WORKDIR /app
ARG NEXT_PUBLIC_USER_API_URL=http://localhost:8080
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run lint && npm run typecheck && npm run build

FROM node:22-bookworm-slim
WORKDIR /app
ENV NODE_ENV=production
COPY --chown=node:node --from=build /app ./
USER node
EXPOSE 3000
CMD ["npm", "run", "start"]
