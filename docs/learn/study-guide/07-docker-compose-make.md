# 07: Docker, Compose and Make

> **In plain words:** **Docker** packs a program and everything it needs into a sealed box (a *container*) that runs
> the same on any computer. **Compose** starts several boxes together and connects them. **Make** gives you short
> commands so you don't have to type long ones.

---

## 1. Why containers?

The classic problem: "it works on my machine". Your laptop has Go 1.26, someone else has 1.21, the server has none.

A **container** solves this the way shipping containers solved cargo. Before them, every ship was loaded by hand
with differently shaped crates. After: one standard box, any crane, any ship, any truck.

Two words people mix up:

| Word | Analogy | Meaning |
|---|---|---|
| **Image** | A recipe card / a frozen meal | A read-only package: the program + its files. Built once. |
| **Container** | The meal, heated and on a plate | A *running* copy of an image. You can run many from one image. |

A container is **not** a virtual machine. A VM fakes a whole computer with its own operating system (heavy, slow to start).
A container shares your computer's kernel and just gets its own isolated files, network and process list (light, starts in a blink).
On a Mac, Docker Desktop runs one small Linux VM and all containers share it.

That's also why **Go isn't installed on your Mac**: everything builds *inside* a container that has Go.
To run Go commands we use: `docker run --rm -v "$PWD":/src -w /src golang:1.26-alpine go <cmd>`
(`--rm` = delete the container after, `-v` = share this folder into it, `-w` = start in that folder).

## 2. The Dockerfile, line by line ([Dockerfile](../../../Dockerfile))

A Dockerfile is the recipe for an image. Ours has **two stages**: a big kitchen to cook in, and a small lunchbox to carry the result.

### Stage 1: build (the kitchen)

```dockerfile
FROM golang:1.26-alpine AS build
```
Start from an image that already has Go installed (on Alpine, a tiny Linux). Name this stage `build`.

```dockerfile
WORKDIR /src
```
`cd /src` inside the image (creating it if needed).

```dockerfile
COPY go.mod go.sum ./
RUN go mod download
```
Copy **only** the dependency list and download libraries. Why not copy everything first? **Layer caching.**
Docker saves the result of each step. If the inputs to a step haven't changed, it reuses the saved result.
Our code changes all the time; `go.mod` rarely does. So with this order, editing `main.go` skips the slow download.
Copy everything first, and every tiny code change would re-download all libraries.

```dockerfile
COPY . .
ARG SERVICE
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./services/${SERVICE}
```
- Copy the rest of the code.
- `ARG SERVICE`: a build-time variable. Compose passes `SERVICE=checkout` (or payment, inventory).
- `go build ... ./services/${SERVICE}`: compile that one service into a single file `/out/app`.
  - `CGO_ENABLED=0`: don't link any C code, so the binary is **static** (needs nothing else to run, not even system libraries).
  - `-trimpath`: strip your folder paths from the binary (cleaner, more reproducible).
  - `-ldflags="-s -w"`: strip debug info, so the file is smaller.

This is how **one Dockerfile builds three different services.**

### Stage 2: run (the lunchbox)

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/app /app
USER nonroot
ENTRYPOINT ["/app"]
```
- Start fresh from **distroless**: an image with almost nothing in it. No shell, no package manager, no `ls`.
  Only what a static binary needs (certificates, timezone data, a user list).
- Copy just the finished binary out of the `build` stage. The Go compiler, source code and libraries stay behind.
- Run as a **non-root** user. If someone broke into the program, they'd have no admin rights and no shell to use.
- `ENTRYPOINT`: when the container starts, run `/app`.

Result: the build image is hundreds of MB; the final image is a few MB plus our binary.
Smaller to ship, faster to start, much less for an attacker to use.

### `.dockerignore`

```
.git
docs
*.md
web/node_modules
```
When building, Docker first sends your folder (the "build context") to the Docker engine. This file excludes things
the build doesn't need. Bonus: editing these guides doesn't invalidate the build cache.

## 3. docker-compose.yml, line by line ([docker-compose.yml](../../../docker-compose.yml))

Compose describes several containers as one app.

```yaml
name: miniobs
```
The project name. Containers get named `miniobs-checkout-1`, etc.

```yaml
services:
  checkout:
    build: { context: ., args: { SERVICE: checkout } }
```
Build an image from the Dockerfile in `.` (this folder), passing `SERVICE=checkout`. Same for the other two.

```yaml
    ports: ["8081:8081"]
```
**`host:container`.** Connect port 8081 on your Mac to port 8081 inside the container.
Without this, the service would only be reachable from other containers, not from your terminal.

```yaml
    environment:
      PORT: "8081"
      PAYMENT_URL: http://payment:8082
```
Environment variables for the program (read by `envOr` in Go). Notice the hostname is **`payment`**, not an IP.

**Compose networking:** Compose puts all the services on a private network with its own DNS (name lookup), where
**each service's name is its hostname**. Inside checkout's container, `payment` resolves to the payment container.
It's like an office phone directory: dial "payment" and you get the right desk, wherever they're sitting.

```yaml
      # Phase 2: OTEL_EXPORTER_OTLP_ENDPOINT: http://ingest:4318
```
The line that will switch on span exporting (see [04](04-traces-spans-propagation.md#the-important-if-why-no-spans-are-sent-yet)).

```yaml
    depends_on: [payment]
```
Start payment *before* checkout. Careful: this only controls **start order**. It doesn't wait until payment is
actually *ready* to answer. For our tiny services that start in milliseconds, that's fine. For slow things like
databases (Phase 2), we'll need health checks.

```yaml
    restart: unless-stopped
```
If the program crashes, Docker restarts it. Unless you stopped it yourself.

### What happens on `make up`

```
docker compose up --build -d
  1. build 3 images (same Dockerfile, different SERVICE)
  2. create the private network "miniobs_default"
  3. start inventory (no dependencies)
  4. start payment   (depends on inventory)
  5. start checkout  (depends on payment)
  -d = "detached": run in the background and give you your terminal back
```

## 4. The Makefile ([Makefile](../../../Makefile))

`make` is an old (1976!) tool for running named recipes. Each **target** is a name followed by commands:

```make
order:            ## place one order (ITEM=sku-mouse make order)
	@curl -s -i -X POST localhost:8081/checkout -H 'Content-Type: application/json' \
		-d '{"item_id":"$(ITEM)","quantity":1}'; echo
```

| Bit | Meaning |
|---|---|
| `order:` | the target name: `make order` |
| `## ...` | just a comment for humans |
| the indent | **must be a tab**, not spaces (Make's most famous gotcha) |
| `@` | don't print the command itself, just its output |
| `$(ITEM)` | a Make variable |
| `ITEM ?= sku-keyboard` | default value, unless you set one: `ITEM=sku-mouse make order` |
| `$$p` | a literal `$p` for the shell (`$$` escapes Make's `$`) |
| `.PHONY: ...` | "these aren't files": stops Make from skipping a target if a file with that name exists |

`curl` flags used: `-s` silent (no progress bar), `-i` include response headers (that's how you see `X-Trace-Id`),
`-X` choose the method, `-H` add a header, `-d` send a body.

## 5. Handy Docker commands

| Command | Does |
|---|---|
| `make up` / `make down` | start everything / stop and remove containers |
| `make ps` | list running containers |
| `make logs` | follow all logs live (Ctrl-C to stop following) |
| `docker compose logs inventory` | one service's logs |
| `docker compose logs --no-log-prefix \| grep <trace_id>` | find one request across all services |
| `docker compose restart inventory` | restart one (also clears its fault and resets stock) |
| `docker compose up --build -d inventory` | rebuild and restart one after a code change |

## Check yourself

1. Why is `COPY go.mod go.sum` done before `COPY . .`?
2. What's the difference between an image and a container?
3. Inside the checkout container, why does `http://payment:8082` work?
4. Does `depends_on` guarantee payment is ready to answer when checkout starts?
5. Why does the final image have no shell, and why is that good?
