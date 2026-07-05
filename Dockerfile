ARG BUILDPLATFORM
ARG TARGETPLATFORM
ARG TARGETARCH

FROM --platform=$BUILDPLATFORM node:22-alpine AS web-build
WORKDIR /app/web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile
COPY VERSION /app/VERSION
COPY web ./
RUN pnpm run typecheck
RUN NEXT_PUBLIC_APP_VERSION="$(cat /app/VERSION)" pnpm run build

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS go-build
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/gpt2api-image ./cmd/server

FROM alpine:3.20 AS app
WORKDIR /app
RUN adduser -D -H app && mkdir -p /app/data /app/web_dist && chown -R app:app /app
COPY --from=go-build /out/gpt2api-image /app/gpt2api-image
COPY --from=web-build /app/web/out /app/web_dist
COPY config.example.json /app/config.example.json
COPY VERSION /app/VERSION
USER app
EXPOSE 80
ENV GPT2API_IMAGE_ADDR=:80
CMD ["/app/gpt2api-image"]
