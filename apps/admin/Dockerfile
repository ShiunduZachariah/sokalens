FROM node:24-alpine AS build
WORKDIR /app
COPY apps/admin/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY apps/admin/ ./
ARG NEXT_PUBLIC_SENTRY_DSN
ENV NEXT_PUBLIC_SENTRY_DSN=$NEXT_PUBLIC_SENTRY_DSN
# Persist npm's cache and Next's own build cache between builds. Without the
# second one Next recompiles every route on every deploy.
# Keep dependency-sensitive Next artifacts separate from the other app.
RUN --mount=type=cache,target=/root/.npm \
    --mount=type=cache,id=sokalens-admin-next,target=/app/.next/cache,sharing=locked \
    npm run build

FROM node:24-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=3001
ENV HOSTNAME=0.0.0.0
# Standalone output: the traced server plus only the dependencies it imports.
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
EXPOSE 3001
CMD ["node", "server.js"]
