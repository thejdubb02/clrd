# clrd: one static binary on distroless. Build: docker build -t clrd .
FROM golang:1.24 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The build context has no .git, so the commit and build date shown at
# /api/v1/version come in as build args (docs/self-host.md); empty is fine.
ARG CLRD_COMMIT=
ARG CLRD_DATE=
RUN go build -trimpath -ldflags="-s -w -X github.com/thejdubb02/clrd.Commit=${CLRD_COMMIT} -X github.com/thejdubb02/clrd.Built=${CLRD_DATE}" -o /out/clrd ./server/cmd/clrd \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/clrd /usr/local/bin/clrd
COPY --from=build --chown=65532:65532 /out/data /data
VOLUME /data
EXPOSE 8080
ENV CLRD_LISTEN=:8080 CLRD_DB=/data/clrd.db
HEALTHCHECK --interval=30s --timeout=5s CMD ["clrd", "health"]
ENTRYPOINT ["clrd"]
CMD ["serve"]
