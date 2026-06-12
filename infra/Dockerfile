# Backend image: static Go binary on a distroless base. Build context is the
# repo root (see docker-compose.yml). Migrations are baked in so the image can
# apply them at deploy time if desired.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /out/exchange ./services/exchange/cmd/exchange

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/exchange /exchange
COPY --from=build /src/infra/db /infra/db
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/exchange"]
