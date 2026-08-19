FROM golang:1.26 AS builder
ENV GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /leadjudge ./cmd/leadjudge
RUN CGO_ENABLED=0 go build -o /leadtool ./cmd/leadtool

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /leadjudge /leadjudge
COPY --from=builder /leadtool /leadtool
EXPOSE 49660
ENTRYPOINT ["/leadjudge"]
