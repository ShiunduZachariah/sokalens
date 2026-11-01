FROM node:24-alpine AS build
WORKDIR /app
COPY apps/web/package*.json ./
RUN --mount=type=cache,target=/root/.npm npm ci
COPY apps/web/ ./
ARG NEXT_PUBLIC_SENTRY_DSN
ARG NEXT_PUBLIC_TURNSTILE_SITE_KEY
ENV NEXT_PUBLIC_SENTRY_DSN=$NEXT_PUBLIC_SENTRY_DSN
ENV NEXT_PUBLIC_TURNSTILE_SITE_KEY=$NEXT_PUBLIC_TURNSTILE_SITE_KEY
# Persist npm's cache and Next's own build cache between builds. Without the
# second one Next logs "No build cache found" and recompiles every route on
# every deploy.
# Keep dependency-sensitive Next artifacts separate from the other app.
RUN --mount=type=cache,target=/root/.npm \
    --mount=type=cache,id=sokalens-web-next,target=/app/.next/cache,sharing=locked \
    npm run build

FROM node:24-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
ENV PORT=3000
ENV HOSTNAME=0.0.0.0
# Standalone output: the traced server plus only the dependencies it imports.
# The previous image copied the entire node_modules tree, dev tooling included.
COPY --from=build /app/public ./public
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
EXPOSE 3000
CMD ["node", "server.js"]
