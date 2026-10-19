FROM node:22-bookworm-slim AS console
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build
FROM golang:1.26.10-bookworm AS backend
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=console /src/cmd/desk/static ./cmd/desk/static
RUN CGO_ENABLED=0 go build -trimpath -o /desk ./cmd/desk && CGO_ENABLED=0 go build -trimpath -o /erpsim ./cmd/erpsim
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=backend /desk /erpsim /app/
COPY migrations/ ./migrations/
USER nonroot:nonroot
EXPOSE 8090
ENTRYPOINT ["/app/desk"]
