# Build a static binary, then ship it in a minimal image.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /eavesong ./cmd/eavesong
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /eavesong /eavesong
COPY --from=build --chown=65532:65532 /data /data
ENV EAVESONG_ADDR=0.0.0.0:8080 EAVESONG_DB=/data/eavesong.db
VOLUME /data
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/eavesong"]
CMD ["serve"]
