# Build stage: compile the Go program
FROM golang:1.27-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o product-service .

# Runtime stage: only the compiled program
FROM gcr.io/distroless/static-debian12
COPY --from=build /app/product-service /product-service
EXPOSE 8080
ENTRYPOINT ["/product-service"]