# --- Stage 1: Build Environment ---
FROM golang:1.22-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy dependency manifests first to leverage Docker layer caching
COPY go.mod go.sum ./
RUN go.mod download

# Copy the rest of the source code files
COPY . .

# Compile the Go application into a static binary optimized for production Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o flagship-engine .

# --- Stage 2: Pristine Runtime Environment ---
FROM alpine:3.19 AS runtime

# Install CA certificates so your app can securely make HTTPS calls to the AWS SSM API
RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy only the compiled binary from the builder stage
COPY --from=builder /app/flagship-engine .

# Expose the internal control port your app is listening on
EXPOSE 8080

# Run the binary
CMD ["./flagship-engine"]