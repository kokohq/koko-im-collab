# Use a pinned Go image to reduce supply-chain drift and pick up security fixes.
FROM golang:1.25-alpine3.22 AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy go mod and go.sum files (go.sum can be empty if no deps)
COPY api/go.mod api/go.sum ./

# Download dependencies
RUN go mod download

# Copy the source code
COPY api/ .

# Build the application
RUN go build -o main .

# Use a pinned Alpine image instead of latest for deterministic security patching.
FROM alpine:3.22

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Set the working directory
WORKDIR /root/

# Copy the binary from the builder stage
COPY --from=builder /app/main .

# Expose the port the app runs on
EXPOSE 8080

# Command to run the application
CMD ["./main"]