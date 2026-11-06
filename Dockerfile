# One Dockerfile for every Go service. Pick which one with:
#   docker build --build-arg SERVICE=inventory .

# ---- Stage 1: build ----
FROM golang:1.26-alpine AS build
WORKDIR /src

# Copy only the dependency list first. Docker caches each step, so as long as
# go.mod/go.sum don't change, it skips re-downloading libraries on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG SERVICE
# CGO_ENABLED=0 builds a fully static binary, so it runs on an empty base image.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./services/${SERVICE}

# ---- Stage 2: run ----
# distroless/static: no shell, no package manager, just enough to run a static binary.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot
ENTRYPOINT ["/app"]
