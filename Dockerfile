# Go собирает под любую архитектуру сам, поэтому сборка идёт на платформе сборщика без эмуляции
FROM --platform=$BUILDPLATFORM golang:1.27 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go index.html ./
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /dito .

# в distroless/static есть сертификаты (для wttr.in) и tzdata (для часовых поясов)
FROM gcr.io/distroless/static-debian12
COPY --from=build /dito /dito
ENV DITO_CONFIG=/data/dito.json DITO_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/dito"]
CMD ["clock"]
